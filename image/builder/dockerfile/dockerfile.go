package dockerfile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/docker/docker/builder/dockerfile/parser"
	"github.com/docker/docker/builder/dockerfile/shell"
	"github.com/mgoltzsche/cntnr/pkg/idutils"
	"github.com/mgoltzsche/cntnr/pkg/log"
	"github.com/pkg/errors"
)

type ImageBuilder interface {
	BuildName(string)
	AddEnv(map[string]string) error
	AddExposedPorts([]string) error
	AddLabels(map[string]string) error
	AddVolumes([]string) error
	AddFiles(srcDir string, srcPattern []string, dest string, user *idutils.User) error
	CopyFiles(srcDir string, srcPattern []string, dest string, user *idutils.User) error
	CopyFilesFromImage(srcImage string, srcPattern []string, dest string, user *idutils.User) error
	FromImage(name string) error
	Run(args []string, addEnv map[string]string) error
	SetAuthor(string) error
	SetCmd([]string) error
	SetEntrypoint([]string) error
	SetStopSignal(string) error
	SetUser(string) error
	SetWorkingDir(string) error
}

type DockerfileBuilder struct {
	ops        []func(ImageBuilder) error
	ctxDir     string
	buildArgs  map[string]string
	envMap     map[string]bool
	runEnvMap  map[string]string
	varScope   map[string]string
	shell      []string
	lex        *shell.Lex
	imageCount int
	warn       log.Logger
}

func LoadDockerfile(src []byte, ctxDir string, args map[string]string, warn log.Logger) (b *DockerfileBuilder, err error) {
	r, err := parser.Parse(bytes.NewReader(src))
	if err != nil {
		return b, errors.New("load dockerfile: " + err.Error())
	}
	for _, warning := range r.Warnings {
		warn.Println(warning)
	}
	if args == nil {
		args = map[string]string{}
	}
	lex := shell.NewLex(r.EscapeToken)
	sh := []string{"/bin/sh", "-c"}
	b = &DockerfileBuilder{ctxDir: ctxDir, buildArgs: args, shell: sh, lex: lex, warn: warn}
	b.resetState()
	for _, n := range r.AST.Children {
		if err = b.readNode(n); err != nil {
			return
		}
	}
	return
}

func (s *DockerfileBuilder) resetState() {
	s.envMap = map[string]bool{}
	s.runEnvMap = map[string]string{}
	s.varScope = map[string]string{}
}

func (s *DockerfileBuilder) Apply(b ImageBuilder) (err error) {
	for _, op := range s.ops {
		if err = op(b); err != nil {
			break
		}
	}
	return errors.Wrap(err, "apply dockerfile")
}

func (b *DockerfileBuilder) readNode(node *parser.Node) (err error) {
	instr := node.Value
	switch instr {
	case "from":
		err = b.from(node)
	case "copy":
		err = b.copy(node, opCopy)
	case "add":
		err = b.copy(node, opAdd)
	case "label":
		err = b.label(node)
	case "maintainer":
		err = b.maintainer(node)
	case "arg":
		err = b.arg(node)
	case "env":
		err = b.env(node)
	case "workdir":
		err = b.workdir(node)
	case "user":
		err = b.user(node)
	case "shell":
		err = b.useShell(node)
	case "run":
		err = b.run(node)
	case "expose":
		err = b.exposePorts(node)
	case "volume":
		err = b.volume(node)
	case "entrypoint":
		err = b.entrypoint(node)
	case "cmd":
		err = b.cmd(node)
	case "stopsignal":
		err = b.stopsignal(node)
		// TODO: HEALTHCHECK
		// onbuild ignored here because not supported by OCI image format and it provides unnecessary complexity
	default:
		l, _ := readInstructionNode(node)
		fmt.Printf("%+v  %s\n", l, node.Dump())
		err = errors.Errorf("unsupported instruction %q", instr)
	}
	return errors.Wrapf(err, "line %d: %s", node.StartLine, instr)
}

func (s *DockerfileBuilder) add(op func(ImageBuilder) error) {
	s.ops = append(s.ops, op)
}

// See https://docs.docker.com/engine/reference/builder/#from
func (s *DockerfileBuilder) from(n *parser.Node) (err error) {
	v, err := readInstructionNode(n)
	if err == nil {
		if err = s.subst(v); err == nil {
			if len(v) != 1 && len(v) != 3 || v[0] == "" || (len(v) == 3 && v[1] != "as") {
				return errors.Errorf("from: expected 'image [as name]' but was %+v", v)
			}
			s.resetState()
			image := v[0]
			s.add(func(b ImageBuilder) error {
				err = b.FromImage(image)
				if err == nil {
					name := strconv.Itoa(s.imageCount)
					s.imageCount++
					b.BuildName(name)
					if len(v) == 3 {
						b.BuildName(v[2])
					}
				}
				return err
			})
		}
	}
	return
}

type addOp func(b ImageBuilder, fromImage string, buildDir string, srcPattern []string, dest string, usr *idutils.User) error

func opAdd(b ImageBuilder, fromImage string, buildDir string, srcPattern []string, dest string, usr *idutils.User) error {
	if fromImage != "" {
		return errors.New("ADD command does not support --from option. Use COPY command instead")
	}
	return b.AddFiles(buildDir, srcPattern, dest, usr)
}

func opCopy(b ImageBuilder, fromImage string, buildDir string, srcPattern []string, dest string, usr *idutils.User) error {
	if fromImage == "" {
		return b.CopyFiles(buildDir, srcPattern, dest, usr)
	} else {
		return b.CopyFilesFromImage(fromImage, srcPattern, dest, usr)
	}
}

// See https://docs.docker.com/engine/reference/builder/#copy
// and https://docs.docker.com/engine/reference/builder/#add
func (s *DockerfileBuilder) copy(n *parser.Node, op addOp) (err error) {
	chown := "--chown"
	from := "--from"
	v, err := readInstructionNode(n, &chown, &from)
	if err == nil {
		flags := []string{chown, from}
		err = s.subst(flags)
		if err == nil {
			chown = flags[0]
			from = flags[1]
			if err = s.subst(v); err == nil {
				srcPattern := v
				dest := ""
				if len(v) > 1 {
					srcPattern = v[0 : len(v)-1]
					dest = v[len(v)-1]
				}
				var usr *idutils.User
				if chown != "" {
					u := idutils.ParseUser(chown)
					usr = &u
				}
				s.add(func(b ImageBuilder) error {
					return op(b, from, s.ctxDir, srcPattern, dest, usr)
				})
			}
		}
	}
	return
}

// See https://docs.docker.com/engine/reference/builder/#label
func (s *DockerfileBuilder) label(n *parser.Node) (err error) {
	v, err := readInstructionNode(n)
	if err == nil {
		if err = s.subst(v); err == nil {
			m, err := s.toMap(v)
			if err == nil {
				s.add(func(b ImageBuilder) error {
					for k, v := range m {
						if k == "maintainer" {
							if err := b.SetAuthor(v); err != nil {
								return err
							}
							delete(m, k)
						}
					}
					if len(m) > 0 {
						return b.AddLabels(m)
					}
					return nil
				})
			}
		}
	}
	return
}

// See https://docs.docker.com/engine/reference/builder/#maintainer
func (s *DockerfileBuilder) maintainer(n *parser.Node) (err error) {
	v, err := readInstructionNode(n)
	if err == nil {
		s.add(func(b ImageBuilder) error {
			return b.SetAuthor(strings.Join(v, " "))
		})
	}
	return
}

// See https://docs.docker.com/engine/reference/builder/#arg
func (s *DockerfileBuilder) arg(n *parser.Node) (err error) {
	l, err := readInstructionNode(n)
	if err != nil {
		return
	}
	var k, v string
	if len(l) >= 2 {
		k = l[0]
		v = strings.Join(l[1:], " ")
	} else {
		k = l[0]
	}
	if barg, ok := s.buildArgs[k]; ok {
		v = barg
	} else if v == "" {
		s.warn.Printf("undefined build arg %q", k)
	}
	// Apply in subsequent var substitutions if env value not already defined
	if s.envMap[k] {
		s.warn.Printf("arg %q is shadowed by env var", k)
	} else if v != "" {
		s.runEnvMap[k] = v
		s.varScope[k] = v
	}
	return
}

// See https://docs.docker.com/engine/reference/builder/#env
func (s *DockerfileBuilder) env(n *parser.Node) (err error) {
	v, err := readInstructionNode(n)
	if err == nil {
		if err = s.subst(v); err == nil {
			m, err := s.toMap(v)
			if err == nil {
				s.add(func(b ImageBuilder) error {
					return b.AddEnv(m)
				})
				for k, v := range m {
					s.envMap[k] = true
					s.varScope[k] = v
				}
			}
		}
	}
	return
}

// See https://docs.docker.com/engine/reference/builder/#shell
func (s *DockerfileBuilder) useShell(n *parser.Node) (err error) {
	v, err := readInstructionNode(n)
	if err == nil {
		s.shell = v
	}
	return
}

// See https://docs.docker.com/engine/reference/builder/#user
func (s *DockerfileBuilder) user(n *parser.Node) (err error) {
	v, err := readInstructionNode(n)
	if err != nil {
		return
	}
	if len(v) != 1 {
		return errors.New("invalid argument count: " + n.Dump())
	}
	if err = s.subst(v); err == nil {
		s.add(func(b ImageBuilder) error {
			return b.SetUser(v[0])
		})
	}
	return
}

// See https://docs.docker.com/engine/reference/builder/#workdir
func (s *DockerfileBuilder) workdir(n *parser.Node) (err error) {
	v, err := readInstructionNode(n)
	if err != nil {
		return
	}
	if len(v) != 1 {
		return errors.New("invalid argument count: " + n.Dump())
	}
	if err = s.subst(v); err == nil {
		s.add(func(b ImageBuilder) error {
			return b.SetWorkingDir(v[0])
		})
	}
	return
}

// See https://docs.docker.com/engine/reference/builder/#run
func (s *DockerfileBuilder) run(n *parser.Node) (err error) {
	v, err := s.readInstructionNodeCmd(n)
	if err != nil {
		return
	}
	args := map[string]string{}
	for k, v := range s.runEnvMap {
		args[k] = v
	}
	s.add(func(b ImageBuilder) error {
		return b.Run(v, args)
	})
	return
}

// See https://docs.docker.com/engine/reference/builder/#expose
func (s *DockerfileBuilder) exposePorts(n *parser.Node) (err error) {
	v, err := readInstructionNode(n)
	if err != nil {
		return
	}
	if err = s.subst(v); err != nil {
		return
	}
	s.add(func(b ImageBuilder) error {
		return b.AddExposedPorts(v)
	})
	return
}

// See https://docs.docker.com/engine/reference/builder/#volume
func (s *DockerfileBuilder) volume(n *parser.Node) (err error) {
	v, err := readInstructionNode(n)
	if err != nil {
		return
	}
	if err = s.subst(v); err == nil {
		s.add(func(b ImageBuilder) error {
			return b.AddVolumes(v)
		})
	}
	return
}

// See https://docs.docker.com/engine/reference/builder/#entrypoint
func (s *DockerfileBuilder) entrypoint(n *parser.Node) (err error) {
	v, err := s.readInstructionNodeCmd(n)
	if err == nil {
		s.add(func(b ImageBuilder) error {
			return b.SetEntrypoint(v)
		})
	}
	return
}

// See https://docs.docker.com/engine/reference/builder/#cmd
func (s *DockerfileBuilder) cmd(n *parser.Node) (err error) {
	v, err := s.readInstructionNodeCmd(n)
	if err == nil {
		s.add(func(b ImageBuilder) error {
			return b.SetCmd(v)
		})
	}
	return
}

// See https://docs.docker.com/engine/reference/builder/#stopsignal
func (s *DockerfileBuilder) stopsignal(n *parser.Node) (err error) {
	v, err := readInstructionNode(n)
	if err != nil {
		return
	}
	if len(v) != 1 {
		return errors.New("invalid argument count: " + n.Dump())
	}
	if err = s.subst(v); err == nil {
		s.add(func(b ImageBuilder) error {
			return b.SetStopSignal(v[0])
		})
	}
	return
}

// See https://docs.docker.com/engine/reference/builder/#environment-replacement
// and https://docs.docker.com/engine/reference/builder/#arg
func (s *DockerfileBuilder) subst(v []string) (err error) {
	env := make([]string, 0, len(s.varScope))
	for k, v := range s.varScope {
		env = append(env, k+"="+v)
	}
	for i, e := range v {
		if v[i], err = s.lex.ProcessWord(e, env); err != nil {
			return
		}
	}
	return
}

func readInstructionNode(node *parser.Node, flags ...*string) (r []string, err error) {
	r = []string{}
	for n := node.Next; n != nil; n = n.Next {
		r = append(r, n.Value)
	}
	if len(r) == 0 {
		err = errors.New("incomplete instruction: " + node.Dump())
	} else {
		err = readFlags(node, flags...)
	}
	return
}

func (s *DockerfileBuilder) readInstructionNodeCmd(n *parser.Node) (r []string, err error) {
	if r, err = readInstructionNode(n); err == nil {
		if !isJsonNotation(n) {
			r = append(s.shell, strings.Join(r, " "))
		}
	}
	return
}

func (s *DockerfileBuilder) toMap(v []string) (m map[string]string, err error) {
	m = map[string]string{}
	for i := 0; i+1 < len(v); i += 2 {
		m[unquote(v[i])] = unquote(v[i+1])
	}
	return
}

func unquote(v string) string {
	if r, e := strconv.Unquote(v); e == nil {
		return r
	}
	return v
}

func readFlags(n *parser.Node, flags ...*string) error {
	m := map[string]*string{}
	for _, f := range flags {
		if *f == "" {
			panic("empty flag value provided")
		}
		m[*f] = f
		*f = ""
	}
	for _, f := range n.Flags {
		kv := strings.SplitN(f, "=", 2)
		key := kv[0]
		if vp, ok := m[key]; ok {
			if len(kv) == 2 {
				*vp = kv[1]
			}
		} else {
			return errors.Errorf("unsupported flag %q", key)
		}
	}
	return nil
}

var jsonRegex = regexp.MustCompile("^[A-Za-z]+\\s*\\[[^\\]]+\\]\\s*$")

func isJsonNotation(n *parser.Node) bool {
	line := strings.TrimSpace(n.Original)
	args := strings.TrimSpace(line[strings.Index(line, " "):])
	err := json.Unmarshal([]byte(args), &[]string{})
	return err == nil
}
