package main

import (
	"encoding/json"
	"io"
	"io/ioutil"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	winio "github.com/Microsoft/go-winio"
	"github.com/Microsoft/hcsshim/internal/appargs"
	"github.com/Microsoft/hcsshim/internal/hcs"
	"github.com/Microsoft/hcsshim/internal/schema2"
	"github.com/Microsoft/hcsshim/internal/schemaversion"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
	"golang.org/x/sys/windows"
)

func containerPipePath(id string) string {
	return safePipePath("runhcs-shim-" + id)
}

func newFile(context *cli.Context, param string) *os.File {
	fd := uintptr(context.Int(param))
	if fd == 0 {
		return nil
	}
	return os.NewFile(fd, "")
}

var shimCommand = cli.Command{
	Name:   "shim",
	Usage:  `launch the process and proxy stdio (do not call it outside of runhcs)`,
	Hidden: true,
	Flags: []cli.Flag{
		&cli.IntFlag{Name: "stdin", Hidden: true},
		&cli.IntFlag{Name: "stdout", Hidden: true},
		&cli.IntFlag{Name: "stderr", Hidden: true},
		&cli.BoolFlag{Name: "exec", Hidden: true},
	},
	Before: appargs.Validate(argID),
	Action: func(context *cli.Context) error {
		logrus.SetOutput(os.Stderr)
		fatalWriter.Writer = os.Stdout

		id := context.Args().First()
		c, err := getContainer(id, true)
		if err != nil {
			return err
		}

		// Asynchronously wait for the container to exit.
		containerExitCh := make(chan error)
		go func() {
			containerExitCh <- c.hc.Wait()
		}()

		// Get File objects for the open stdio files passed in as arguments.
		stdin := newFile(context, "stdin")
		stdout := newFile(context, "stdout")
		stderr := newFile(context, "stderr")

		exec := context.Bool("exec")
		terminateOnFailure := false

		errorOut := io.WriteCloser(os.Stdout)

		var spec *specs.Process

		if exec {
			// Read the process spec from stdin.
			specj, err := ioutil.ReadAll(os.Stdin)
			if err != nil {
				return err
			}
			os.Stdin.Close()

			spec = new(specs.Process)
			err = json.Unmarshal(specj, spec)
			if err != nil {
				return err
			}

		} else {
			// Stdin is not used.
			os.Stdin.Close()

			// Listen on the named pipe associated with this container.
			l, err := winio.ListenPipe(c.ShimPipePath(), nil)
			if err != nil {
				return err
			}

			// Alert the parent process that initialization has completed
			// successfully.
			errorOut.Write(shimSuccess)
			errorOut.Close()
			fatalWriter.Writer = ioutil.Discard

			// When this process exits, clear this process's pid in the registry.
			defer func() {
				stateKey.Set(id, keyShimPid, 0)
			}()

			defer func() {
				if terminateOnFailure {
					if err = c.hc.Terminate(); hcs.IsPending(err) {
						<-containerExitCh
					}
				}
			}()
			terminateOnFailure = true

			// Wait for a connection to the named pipe, exiting if the container
			// exits before this happens.
			var pipe net.Conn
			pipeCh := make(chan error)
			go func() {
				var err error
				pipe, err = l.Accept()
				pipeCh <- err
			}()

			select {
			case err = <-pipeCh:
				if err != nil {
					return err
				}
			case err = <-containerExitCh:
				if err != nil {
					return err
				}
				return cli.NewExitError("", 1)
			}

			// The next set of errors goes to the open pipe connection.
			errorOut = pipe
			fatalWriter.Writer = pipe

			// The process spec comes from the original container spec.
			spec = c.Spec.Process
		}

		// Create the process in the container.
		var pc *schema2.ProcessConfig
		if c.Spec.Linux == nil {
			environment := make(map[string]string)
			for _, v := range spec.Env {
				s := strings.SplitN(v, "=", 2)
				if len(s) == 2 && len(s[1]) > 0 {
					environment[s[0]] = s[1]
				}
			}
			pc = &schema2.ProcessConfig{
				SchemaVersion:    schemaversion.SchemaV20(),
				WorkingDirectory: spec.Cwd,
				EmulateConsole:   spec.Terminal,
				Environment:      environment,
			}
			for i, arg := range spec.Args {
				e := windows.EscapeArg(arg)
				if i == 0 {
					pc.CommandLine = e
				} else {
					pc.CommandLine += " " + e
				}
			}
		} else {
			pc = &schema2.ProcessConfig{
				SchemaVersion: schemaversion.SchemaV20(),
			}
			if exec {
				pc.OCIProcess = spec
			}
		}

		pc.CreateStdInPipe = stdin != nil
		pc.CreateStdOutPipe = stdout != nil
		pc.CreateStdErrPipe = stderr != nil

		p, err := c.hc.CreateProcess(pc)
		if err != nil {
			return err
		}

		cstdin, cstdout, cstderr, err := p.Stdio()
		if err != nil {
			return err
		}

		if !exec {
			err = stateKey.Set(c.ID, keyInitPid, p.Pid())
			if err != nil {
				return err
			}
		}

		terminateOnFailure = false

		// Alert the connected process that the process was launched
		// successfully.
		errorOut.Write(shimSuccess)
		errorOut.Close()
		fatalWriter.Writer = ioutil.Discard

		// Relay stdio.
		var wg sync.WaitGroup
		if cstdin != nil {
			go func() {
				io.Copy(cstdin, stdin)
				cstdin.Close()
				p.CloseStdin()
			}()
		}

		if cstdout != nil {
			wg.Add(1)
			go func() {
				io.Copy(stdout, cstdout)
				stdout.Close()
				cstdout.Close()
				wg.Done()
			}()
		}

		if cstderr != nil {
			wg.Add(1)
			go func() {
				io.Copy(stderr, cstderr)
				stderr.Close()
				cstderr.Close()
				wg.Done()
			}()
		}

		err = p.Wait()
		wg.Wait()

		// Attempt to get the exit code from the process.
		code := 1
		if err == nil {
			code, err = p.ExitCode()
			if err != nil {
				code = 1
			}
		}

		if !exec {
			// Shutdown the container, waiting 5 minutes before terminating is
			// forcefully.
			const shutdownTimeout = time.Minute * 5
			waited := false
			err = c.hc.Shutdown()
			if hcs.IsPending(err) {
				select {
				case err = <-containerExitCh:
					waited = true
				case <-time.After(shutdownTimeout):
					err = hcs.ErrTimeout
				}
			}
			if hcs.IsAlreadyStopped(err) {
				err = nil
			}

			if err != nil {
				err = c.hc.Terminate()
				if waited {
					err = c.hc.Wait()
				} else {
					err = <-containerExitCh
				}
			}
		}

		return cli.NewExitError("", code)
	},
}
