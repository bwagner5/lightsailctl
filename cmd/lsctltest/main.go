package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/aws/lightsailctl/pkg/lsctltest"
	"github.com/aws/lightsailctl/pkg/lsctltest/output"
	"github.com/aws/lightsailctl/pkg/lsctltest/testkit"

	// Import test packages so their init() functions register tests.
	_ "github.com/aws/lightsailctl/pkg/lsctltest/tests"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	var env testkit.Env
	cmd := &cobra.Command{
		Use:           "lsctltest",
		Short:         "lightsailctl integration test runner",
		Long:          "Runs lightsailctl integration tests against real AWS resources.",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if env.Binary == "" {
				env.Binary = defaultBinary()
			}
			if env.BinaryAgent == "" {
				env.BinaryAgent = defaultBinaryAgent()
			}
			if !env.DryRun {
				if _, err := os.Stat(env.Binary); err != nil {
					return fmt.Errorf("--binary %q not found: run 'make snapshot' first", env.Binary)
				}
				if _, err := os.Stat(env.BinaryAgent); err != nil {
					return fmt.Errorf("--binary-agent %q not found: run 'make snapshot' first", env.BinaryAgent)
				}
			}
			rep := &output.Stream{W: os.Stderr}
			return lsctltest.Run(cmd.Context(), env, rep)
		},
	}
	f := cmd.Flags()
	f.StringVar(&env.Binary, "binary", "",
		"lightsailctl binary to test against (default: auto-detect from dist/)")
	f.StringVar(&env.BinaryAgent, "binary-agent", "",
		"linux/amd64 lightsailctl binary to transfer to remote instance (default: dist/lightsailctl_linux_amd64_v1/lightsailctl)")
	f.StringVar(&env.Region, "region", "us-east-1", "AWS region")
	f.StringVar(&env.UserData, "user-data", "", "init script for new Lightsail instances")
	f.StringVar(&env.Bundle, "bundle", "large_3_0", "Lightsail bundle for new instances")
	f.BoolVar(&env.Keep, "keep", false, "skip teardown (leave resources in place)")
	f.BoolVar(&env.DryRun, "dry-run", false, "announce steps without executing")
	f.BoolVar(&env.Verbose, "verbose", false, "also stream polling / silent CLI calls")
	return cmd
}

// defaultBinary returns the dist/ path for the host OS/arch.
// Goreleaser layout: lightsailctl_{os}_{arch}_{variant}
func defaultBinary() string {
	arch := runtime.GOARCH
	var variant string
	switch arch {
	case "amd64":
		variant = "amd64_v1"
	case "arm64":
		variant = "arm64_v8.0"
	default:
		variant = arch
	}
	return filepath.Join("dist", fmt.Sprintf("lightsailctl_%s_%s", runtime.GOOS, variant), "lightsailctl")
}

func defaultBinaryAgent() string {
	return filepath.Join("dist", "lightsailctl_linux_amd64_v1", "lightsailctl")
}
