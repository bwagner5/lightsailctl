// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/lightsailctl/internal/plugin"
)

func setArgs(args []string) {
	os.Args = args
}

func setPluginMain(f func(string, []string)) {
	pluginMain = f
}

func TestMainCallsPluginMain(t *testing.T) {
	defer setArgs(os.Args)
	defer setPluginMain(plugin.Main)

	var gotProgname []string
	var gotArgs [][]string
	pluginMain = func(progname string, args []string) {
		gotProgname = append(gotProgname, progname)
		gotArgs = append(gotArgs, args)
	}

	os.Args = []string{"program", "-plugin", "--foo", "55"}
	main()
	os.Args = []string{"program", "--plugin", "--bar", "42"}
	main()

	if want := []string{"program -plugin", "program --plugin"}; !reflect.DeepEqual(gotProgname, want) {
		t.Errorf("got: %v", gotProgname)
		t.Logf("want: %v", want)
	}

	if want := [][]string{{"--foo", "55"}, {"--bar", "42"}}; !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("got: %v", gotArgs)
		t.Logf("want: %v", want)
	}
}

// TestRunHelp exercises Ryer's Run entry point end-to-end without touching
// the real environment. Parallel-safe.
func TestRunHelp(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	getenv := func(string) string { return "" } // hermetic
	err := Run(context.Background(), []string{"lightsailctl", "--help"}, getenv, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run --help: %v (stderr=%s)", err, stderr.String())
	}
	out := stdout.String() + stderr.String()
	for _, want := range []string{"lightsailctl", "app", "deploy", "--region"} {
		if !strings.Contains(out, want) {
			t.Errorf("--help output missing %q:\n%s", want, out)
		}
	}
}

// TestRunRegionEnvVar proves LIGHTSAILCTL_REGION flows through the injected
// getenv — no shell state required.
func TestRunRegionEnvVar(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	getenv := func(k string) string {
		if k == "LIGHTSAILCTL_REGION" {
			return "us-east-2"
		}
		return ""
	}
	err := Run(context.Background(), []string{"lightsailctl", "--help"}, getenv, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The --region flag's default should reflect the injected env value.
	out := stdout.String() + stderr.String()
	if !strings.Contains(out, "us-east-2") {
		t.Errorf("expected --region default to be us-east-2, got:\n%s", out)
	}
}
