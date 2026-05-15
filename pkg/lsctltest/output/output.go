// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package output

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/lightsailctl/pkg/lsctltest/testkit"
)

// ANSI color codes.
const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	blue   = "\033[34m"
	cyan   = "\033[36m"
	dim    = "\033[2m"
)

// Stream is the live streaming reporter. W is typically os.Stderr.
type Stream struct {
	W io.Writer
}

// Plan prints the config and the list of tests to be run.
func (s *Stream) Plan(env testkit.Env, tests []testkit.RegisteredTest) {
	w := s.W
	_, _ = fmt.Fprintf(w, "\n%s%slsctltest%s\n", bold, blue, reset)
	_, _ = fmt.Fprintf(w, "  %sbinary%s        %s\n", dim, reset, env.Binary)
	_, _ = fmt.Fprintf(w, "  %sbinary-agent%s  %s\n", dim, reset, env.BinaryAgent)
	_, _ = fmt.Fprintf(w, "  %sregion%s        %s\n", dim, reset, env.Region)
	_, _ = fmt.Fprintf(w, "  %sbundle%s        %s\n", dim, reset, env.Bundle)
	if env.UserData != "" {
		_, _ = fmt.Fprintf(w, "  %suser-data%s     %s\n", dim, reset, env.UserData)
	}
	_, _ = fmt.Fprintf(w, "  %skeep%s          %v\n", dim, reset, env.Keep)
	_, _ = fmt.Fprintf(w, "  %sdry-run%s       %v\n", dim, reset, env.DryRun)
	_, _ = fmt.Fprintf(w, "  %sverbose%s       %v\n", dim, reset, env.Verbose)
	_, _ = fmt.Fprintf(w, "  %stests%s         %d\n\n", dim, reset, len(tests))

	_, _ = fmt.Fprintf(w, "%s%sTests%s\n", bold, yellow, reset)
	for i, rt := range tests {
		_, _ = fmt.Fprintf(w, "  %s%d.%s %s%s%s\n", dim, i+1, reset, bold, rt.Name, reset)
	}
	_, _ = fmt.Fprintln(w)
}

// TestStart prints a banner for a starting test.
func (s *Stream) TestStart(name string) {
	_, _ = fmt.Fprintf(s.W, "\n%s%s=== %s ===%s\n", bold, blue, name, reset)
}

// StepStart announces a step as it begins, before any work has happened.
// This is the key improvement: the user sees "▶ creating instance foo"
// with the full command _before_ the long wait starts.
func (s *Stream) StepStart(name, cmd string) {
	_, _ = fmt.Fprintf(s.W, "\n%s▶ %s%s\n", cyan, name, reset)
	if cmd != "" {
		_, _ = fmt.Fprintf(s.W, "  %s$ %s%s\n", dim, cmd, reset)
	}
}

// StepDone prints the step's result and any output it produced.
func (s *Stream) StepDone(step testkit.Step) {
	// Indent and stream the command output so it's visibly attached to
	// this step and doesn't bleed into the next one.
	if step.Output != "" {
		indentLines(s.W, step.Output, "    ")
	}
	if step.Err != nil {
		_, _ = fmt.Fprintf(s.W, "%s❌ %s: %v%s\n", red, step.Name, step.Err, reset)
		return
	}
	_, _ = fmt.Fprintf(s.W, "%s✅ %s%s %s(%s)%s\n",
		green, step.Name, reset,
		dim, roundDur(step.Elapsed), reset)
}

// TestDone prints a per-test summary line.
func (s *Stream) TestDone(r testkit.Result) {
	if r.Passed {
		_, _ = fmt.Fprintf(s.W, "\n%s%s=== %s PASSED (%s) ===%s\n",
			bold, green, r.Name, roundDur(r.Elapsed), reset)
		return
	}
	_, _ = fmt.Fprintf(s.W, "\n%s%s=== %s FAILED ===%s\n", bold, red, r.Name, reset)
	if r.FailMsg != "" {
		_, _ = fmt.Fprintf(s.W, "%s%s%s\n", red, r.FailMsg, reset)
	}
}

// Summary prints the passed/failed totals at the end.
func (s *Stream) Summary(results []testkit.Result) {
	passed, failed := 0, 0
	for _, r := range results {
		if r.Passed {
			passed++
		} else {
			failed++
		}
	}
	color := green
	if failed > 0 {
		color = red
	}
	_, _ = fmt.Fprintf(s.W, "\n%s%s%d passed, %d failed%s\n",
		bold, color, passed, failed, reset)
}

// Debug prints a verbose-mode trace of a silent CLI call: label, command,
// output, and any error. Everything is dimmed so it doesn't compete with
// the primary step stream.
func (s *Stream) Debug(name, cmd, output string, err error) {
	w := s.W
	_, _ = fmt.Fprintf(w, "%s  · %s\n", dim, name)
	if cmd != "" {
		_, _ = fmt.Fprintf(w, "    $ %s\n", cmd)
	}
	if output != "" {
		indentLines(w, strings.TrimRight(output, "\n"), "      ")
	}
	if err != nil {
		_, _ = fmt.Fprintf(w, "    err: %v\n", err)
	}
	_, _ = fmt.Fprint(w, reset)
}

// indentLines writes text to w with each line prefixed by `prefix`.
// Ensures a trailing newline so the next step's banner starts cleanly.
func indentLines(w io.Writer, text, prefix string) {
	sc := bufio.NewScanner(strings.NewReader(text))
	// Allow very long output lines (default scanner buffer is 64k).
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		_, _ = fmt.Fprintf(w, "%s%s\n", prefix, sc.Text())
	}
}

func roundDur(d time.Duration) time.Duration {
	if d >= time.Second {
		return d.Round(100 * time.Millisecond)
	}
	return d.Round(time.Millisecond)
}
