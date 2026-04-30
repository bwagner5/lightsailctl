// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// Package main is where lightsailctl command begins.
//
// Structure follows Mat Ryer's run-function pattern: main() only wires
// os.* into Run(ctx, args, getenv, stdout, stderr) error. main + Run
// are the only places in this binary that read environment variables;
// everything else gets typed values as arguments.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"regexp"

	"github.com/bwagner5/triad/pkg/registry"
	"github.com/bwagner5/triad/pkg/ui/cli"
	"github.com/bwagner5/triad/pkg/ui/tui"
	"github.com/spf13/cobra"

	"github.com/aws/lightsailctl/internal"
	"github.com/aws/lightsailctl/internal/plugin"
	"github.com/aws/lightsailctl/pkg/app"
	"github.com/aws/lightsailctl/pkg/instance"
)

const cliName = "lightsailctl"

func main() {
	log.SetFlags(0)

	// Preserve the AWS CLI plugin contract: when invoked with --plugin,
	// dispatch to the existing plugin handler and exit. This fast-path
	// bypasses Run because the plugin protocol has its own stdio shape.
	pluginPattern := regexp.MustCompile(`^--?plugin$`)
	if len(os.Args) > 1 && pluginPattern.MatchString(os.Args[1]) {
		pluginMain(os.Args[0]+" "+os.Args[1], os.Args[2:])
		return
	}

	if err := Run(context.Background(), os.Args, os.Getenv, os.Stdout, os.Stderr); err != nil {
		// The saga/CLI renderer already printed the human-facing error.
		// Just exit non-zero so CI sees a failure.
		os.Exit(1)
	}
}

// Run is the testable program entry point. This is the ONLY function in
// lightsailctl (outside of triad's internal flag-default resolution) that
// reads env vars — every other layer receives typed values.
func Run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	// Resolve all environment input here.
	region := getenv(cli.FlagToEnvVar(cliName, "region"))
	regionHints := dedupeNonEmpty(getenv("AWS_REGION"), getenv("AWS_DEFAULT_REGION"))

	g := &cli.Globals{Getenv: getenv}
	reg := registry.New()
	// Pass &g.NonInteractive so the deploy saga's first-run "offer CI"
	// tail step can skip itself under -y. See ResourceWithOptions.
	reg.Register(app.ResourceWithOptions(&region, regionHints, &g.NonInteractive))
	reg.Register(instance.Resource(&region, regionHints))

	root := cli.Build(cliName, "Amazon Lightsail CLI", reg, g)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(args[1:])
	root.Version = internal.Version().String()

	root.PersistentFlags().StringVar(&region, "region", region,
		"AWS region (blank = query all regions) [$"+cli.FlagToEnvVar(cliName, "region")+"]")

	// Top-level `lightsailctl deploy` runs `app deploy`.
	root.AddCommand(cli.AliasOp(reg, g, "app", "deploy", "deploy",
		"deploy current dir to an app/env"))

	// Hang `app local` off the triad-built `app` command. `local` is a
	// nested command group that triad doesn't express natively; these
	// verbs run on the Lightsail instance and are invoked over SSH.
	for _, c := range root.Commands() {
		if c.Name() == "app" {
			c.AddCommand(app.LocalCommand())
			break
		}
	}

	runTUI := func(cmd *cobra.Command, _ []string) error {
		return tui.Run(cmd.Context(), reg, tui.Options{
			Name:      cliName,
			Version:   internal.Version().String(),
			Context:   app.ContextLabel(&region),
			GlobalOps: []registry.Operation{app.RegionSwitchOp(&region)},
		})
	}
	root.RunE = runTUI
	root.AddCommand(&cobra.Command{
		Use:     "tui",
		Short:   "launch the full-screen TUI",
		GroupID: "interface",
		RunE:    runTUI,
	})

	err := root.ExecuteContext(ctx)
	if err != nil {
		// Cobra's SilenceErrors is on, so commands that return plain
		// errors (e.g. List failures) never get printed. The saga
		// renderer prints its own errors inline, but we still need a
		// fallback for everything else.
		fmt.Fprintln(stderr, err)
	}
	return err
}

// dedupeNonEmpty returns a deduplicated slice of the non-empty inputs,
// preserving first-seen order.
func dedupeNonEmpty(vals ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range vals {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// May be set by tests to something else.
var pluginMain = plugin.Main
