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
	"os/exec"
	"strconv"

	"github.com/mgoltzsche/ctnr/pkg/log"
	"github.com/mgoltzsche/ctnr/pkg/log/logrusadapt"
	"github.com/spf13/cobra"

	//homedir "github.com/mitchellh/go-homedir"
	//"github.com/spf13/viper"
	"os"
	"path/filepath"

	"github.com/containers/image/types"
	image "github.com/mgoltzsche/ctnr/image"
	istore "github.com/mgoltzsche/ctnr/image/store"
	storepkg "github.com/mgoltzsche/ctnr/store"
	"github.com/mitchellh/go-homedir"
	"github.com/sirupsen/logrus"
)

var (
	flagRootless    = os.Geteuid() != 0
	flagPRootPath   = findPRootBinary()
	flagVerbose     bool
	flagCfgFile     string
	flagStoreDir    string
	flagStateDir    string
	flagImagePolicy string

	store            storepkg.Store
	lockedImageStore image.ImageStoreRW
	loggers          log.Loggers
	logger           *logrus.Logger
)

// RootCmd represents the base command when called without any subcommands
var RootCmd = &cobra.Command{
	Use:   "ctnr",
	Short: "a lightweight container engine",
	Long: `ctnr is a lightweight OCI container engine built around runc.
It supports container and image management and aims to run in every linux environment.`,
	PersistentPreRun: preRun,
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	RootCmd.AddCommand(runCmd)
	RootCmd.AddCommand(execCmd)
	RootCmd.AddCommand(killCmd)
	RootCmd.AddCommand(listCmd)
	RootCmd.AddCommand(imageCmd)
	RootCmd.AddCommand(imageBuildCmd)
	RootCmd.AddCommand(bundleCmd)
	RootCmd.AddCommand(composeCmd)
	RootCmd.AddCommand(netCmd)
	RootCmd.AddCommand(commitCmd)
	RootCmd.AddCommand(gcCmd)
	if err := RootCmd.Execute(); err != nil {
		loggers.Error.Println(err)
		os.Exit(1)
	}
}

func init() {
	//cobra.OnInitialize(initConfig)

	// Here you will define your flags and configuration settings.
	// Cobra supports persistent flags, which, if defined here,
	// will be global for your application.
	//RootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.ctnr.yaml)")
	logrus.SetLevel(logrus.DebugLevel)
	logger = logrus.New()
	logger.Level = logrus.DebugLevel
	loggers.Info = logrusadapt.NewInfoLogger(logger)
	loggers.Warn = logrusadapt.NewWarnLogger(logger)
	loggers.Error = logrusadapt.NewErrorLogger(logger)
	loggers.Debug = log.NewNopLogger()

	uid := os.Geteuid()
	homeDir, err := homedir.Dir()
	if err == nil {
		flagStoreDir = filepath.Join(homeDir, ".ctnr")
	}
	flagStateDir = "/run/ctnr"
	if uid != 0 {
		flagStateDir = "/run/user/" + strconv.Itoa(uid) + "/ctnr"
	}
	flagImagePolicy = "reject"
	policyFile := "/etc/containers/policy.json"
	if _, err = os.Stat(policyFile); err == nil {
		flagImagePolicy = policyFile
	}
	f := RootCmd.PersistentFlags()
	f.BoolVar(&flagVerbose, "verbose", false, "enables verbose log output")
	f.BoolVar(&flagRootless, "rootless", flagRootless, "enables image and container management as unprivileged user")
	f.StringVar(&flagPRootPath, "proot-path", flagPRootPath, "proot binary location")
	f.StringVar(&flagStoreDir, "store-dir", flagStoreDir, "directory to store images and containers")
	f.StringVar(&flagStateDir, "state-dir", flagStateDir, "directory to store OCI container states (should be tmpfs)")
	f.StringVar(&flagImagePolicy, "image-policy", flagImagePolicy, "image trust policy configuration file or 'insecure'")
}

func preRun(cmd *cobra.Command, args []string) {
	if flagVerbose {
		loggers.Debug = logrusadapt.NewDebugLogger(logger)
	}

	// init store
	// TODO: provide CLI options
	ctx := &types.SystemContext{
		RegistriesDirPath:           "",
		DockerCertPath:              "",
		DockerInsecureSkipTLSVerify: true,
		OSTreeTmpDirPath:            "ostree-tmp-dir",
		// TODO: add docker auth
		//DockerAuthConfig: dockerAuth,
	}
	if flagRootless && ctx.DockerCertPath == "" {
		ctx.DockerCertPath = "./docker-certs"
	}

	var (
		imagePolicy istore.TrustPolicyContext
		err         error
	)
	if flagImagePolicy == "reject" {
		imagePolicy = istore.TrustPolicyReject()
	} else if flagImagePolicy == "insecure" {
		imagePolicy = istore.TrustPolicyInsecure()
	} else if flagImagePolicy != "" {
		imagePolicy = istore.TrustPolicyFromFile(flagImagePolicy)
	} else {
		exitOnError(cmd, usageError("empty value for --image-policy option"))
	}
	store, err = storepkg.NewStore(flagStoreDir, flagRootless, ctx, imagePolicy, loggers)
	exitOnError(cmd, err)
}

func findPRootBinary() string {
	paths := []string{"/usr/bin/proot", "/usr/local/bin/proot"}
	self, err := os.Executable()
	if err == nil {
		paths = append([]string{filepath.Dir(self) + "/proot"}, paths...)
	}
	for _, path := range paths {
		if _, err = os.Stat(path); err == nil {
			return path
		}
	}
	if proot, err := exec.LookPath("proot"); err == nil {
		return proot
	}
	return ""
}

// initConfig reads in config file and ENV variables if set.
/*func initConfig() {
	if flagCfgFile != "" {
		// Use config file from the flag.
		viper.SetConfigFile(flagCfgFile)
	} else {
		// Find home directory.
		home, err := homedir.Dir()
		if err != nil {
			exitError(1, "%s", err)
		}

		// Search config in home directory with name ".ctnr" (without extension).
		viper.AddConfigPath(home)
		viper.SetConfigName(".ctnr")
	}

	viper.AutomaticEnv() // read in environment variables that match

	// If a config file is found, read it in.
	if err := viper.ReadInConfig(); err == nil {
		fmt.Println("Using config file:", viper.ConfigFileUsed())
	}
}*/
