// Copyright © 2017 Max Goltzsche
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	exterrors "github.com/mgoltzsche/ctnr/pkg/errors"
	"github.com/spf13/cobra"
)

var (
	killCmd = &cobra.Command{
		Use:   "kill [flags] CONTAINERID",
		Short: "Kills a running container",
		Long:  `Kills a running container.`,
		Run:   wrapRun(runKill),
	}
	flagSignal os.Signal = syscall.SIGTERM
	flagAll    bool
)

func init() {
	killCmd.Flags().VarP(&fSignal{&flagSignal}, "signal", "s", "Signal to be sent to container process")
	killCmd.Flags().BoolVarP(&flagAll, "all", "a", false, "Send the specified signal to all processes inside the container")
}

func runKill(cmd *cobra.Command, args []string) (err error) {
	if len(args) == 0 {
		return usageError("At least one container ID argument expected")
	}

	containers, err := newContainerManager()
	if err != nil {
		return err
	}

	for _, id := range args {
		if e := containers.Kill(id, flagSignal, flagAll); e != nil {
			loggers.Debug.Println("Failed to kill container:", e)
			err = exterrors.Append(err, e)
		}
	}
	return
}

type fSignal struct {
	v *os.Signal
}

func (c fSignal) Set(v string) (err error) {
	*c.v, err = parseSignal(v)
	return
}

func (c fSignal) Type() string {
	return "SIGNAL"
}

func (c fSignal) String() string {
	if c.v == nil {
		return ""
	}
	return (*c.v).String()
}

func parseSignal(rawSignal string) (syscall.Signal, error) {
	s, err := strconv.Atoi(rawSignal)
	if err == nil {
		return syscall.Signal(s), nil
	}
	signal, ok := signalMap[strings.TrimPrefix(strings.ToUpper(rawSignal), "SIG")]
	if !ok {
		return -1, fmt.Errorf("unknown signal %q", rawSignal)
	}
	return signal, nil
}
