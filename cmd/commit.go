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

	"github.com/spf13/cobra"
)

var (
	commitCmd = &cobra.Command{
		Use:   "commit [flags] CONTAINER [IMAGENAME]",
		Short: "Creates a new image from the current container",
		Long:  `Creates a new image from the current container.`,
		Run:   handleError(runCommit),
	}
	flagAuthor  string
	flagComment string
)

func init() {
	commitCmd.Flags().StringVarP(&flagAuthor, "author", "a", "", "Sets the new layer's author")
	commitCmd.Flags().StringVarP(&flagComment, "comment", "c", "", "Sets the new layer's comment")
}

func runCommit(cmd *cobra.Command, args []string) (err error) {
	if len(args) < 1 || len(args) > 2 {
		return usageError("Invalid argument")
	}
	b, err := store.Bundle(args[0])
	if err != nil {
		return
	}
	imgBuilder, err := store.ImageBuilderFromBundle(b, flagAuthor)
	if err != nil {
		return
	}
	defer func() {
		if e := imgBuilder.Close(); e != nil && err == nil {
			err = e
		}
	}()
	name := ""
	if len(args) > 1 {
		name = args[1]
	}
	img, err := imgBuilder.CommitLayer(name)
	if err != nil {
		return
	}
	fmt.Println(img.ID())
	return
}