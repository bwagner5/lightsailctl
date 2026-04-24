// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// Package main is where lightsailctl command begins.
package main

import (
	"context"
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
)

const cliName = "lightsailctl"

func main() {
	log.SetFlags(0)

	// Preserve the AWS CLI plugin contract: when invoked with --plugin,
	// dispatch to the existing plugin handler and exit.
	pluginPattern := regexp.MustCompile(`^--?plugin$`)
	if len(os.Args) > 1 && pluginPattern.MatchString(os.Args[1]) {
		pluginMain(os.Args[0]+" "+os.Args[1], os.Args[2:])
		return
	}

	g := &cli.Globals{}
	reg := registry.New()
	reg.Register(app.Resource(os.Getenv("AWS_REGION")))
	root := cli.Build(cliName, "Amazon Lightsail CLI", reg, g)
	root.Version = internal.Version().String()

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
		return tui.Run(cmd.Context(), reg, tui.Options{Name: cliName, Version: internal.Version().String()})
	}
	root.RunE = runTUI
	root.AddCommand(&cobra.Command{
		Use:     "tui",
		Short:   "launch the full-screen TUI",
		GroupID: "interface",
		RunE:    runTUI,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := root.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}

// May be set by tests to something else.
var pluginMain = plugin.Main
