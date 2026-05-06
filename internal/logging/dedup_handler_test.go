// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/bwagner5/triad/pkg/trace"
)

// countSubstring returns the number of non-overlapping occurrences of
// sub in s.
func countSubstring(s, sub string) int {
	if sub == "" {
		return 0
	}
	return strings.Count(s, sub)
}

// newDedupTestLogger builds a logger with the same handler chain as
// production (dedup → redact → text).
func newDedupTestLogger(buf *bytes.Buffer) *slog.Logger {
	text := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: trace.LevelVar})
	return slog.New(newDedupHandler(newRedactingHandler(text)))
}

func TestDedupHandlerCollapsesRepeatedUI(t *testing.T) {
	t.Cleanup(trace.ResetForTest)
	var buf bytes.Buffer
	log := newDedupTestLogger(&buf)

	// Simulate the three layers: logging.Init → triad preRun → watch.Run.
	log = log.With(slog.String("ui", "cli"), slog.String("cli_version", "v1.0.8-next"))
	log = log.With(slog.String("ui", "interactive"), slog.String("cmd", "lightsailctl app local watch"))
	log = log.With(slog.String("ui", "watch"))
	log.Info("watcher starting",
		slog.String("app", "hello-world-api"),
		slog.String("env", "dev"))

	out := buf.String()
	if n := countSubstring(out, "ui="); n != 1 {
		t.Errorf("expected exactly 1 ui= attr; got %d in %q", n, out)
	}
	if !strings.Contains(out, "ui=watch") {
		t.Errorf("expected ui=watch (last-wins); got %q", out)
	}
	// Non-duplicate attrs must survive intact.
	for _, want := range []string{"cli_version=v1.0.8-next", `cmd="lightsailctl app local watch"`, "app=hello-world-api", "env=dev"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
}

func TestDedupHandlerRecordAttrsBeatBaseAttrs(t *testing.T) {
	t.Cleanup(trace.ResetForTest)
	var buf bytes.Buffer
	log := newDedupTestLogger(&buf)

	log = log.With(slog.String("ui", "cli"))
	log.Info("msg", slog.String("ui", "override-on-record"))

	out := buf.String()
	if n := countSubstring(out, "ui="); n != 1 {
		t.Errorf("expected 1 ui=; got %d in %q", n, out)
	}
	if !strings.Contains(out, "ui=override-on-record") {
		t.Errorf("record-scoped attr should win over base; got %q", out)
	}
}

func TestDedupHandlerNonDuplicateKeysPassThrough(t *testing.T) {
	t.Cleanup(trace.ResetForTest)
	var buf bytes.Buffer
	log := newDedupTestLogger(&buf)

	log = log.With(slog.String("a", "1"), slog.String("b", "2"))
	log.Info("msg", slog.String("c", "3"))

	out := buf.String()
	for _, want := range []string{"a=1", "b=2", "c=3"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
}

func TestDedupHandlerDoesNotDedupAcrossGroups(t *testing.T) {
	t.Cleanup(trace.ResetForTest)
	var buf bytes.Buffer
	log := newDedupTestLogger(&buf)

	// Once a group is open, dedup stops — keys inside the group are
	// scoped to that group and collisions there are the caller's
	// concern.
	log = log.With(slog.String("ui", "cli")).WithGroup("req")
	log.Info("msg", slog.String("ui", "inside-group"))

	out := buf.String()
	// Top-level ui=cli must survive because it was stamped before the
	// group opened.
	if !strings.Contains(out, "ui=cli") {
		t.Errorf("expected top-level ui=cli to survive; got %q", out)
	}
	// The in-group ui is written as req.ui=... and must not be
	// collapsed with the top-level ui.
	if !strings.Contains(out, "req.ui=inside-group") {
		t.Errorf("expected grouped req.ui=inside-group; got %q", out)
	}
}

func TestDedupHandlerCooperatesWithRedaction(t *testing.T) {
	t.Cleanup(trace.ResetForTest)
	var buf bytes.Buffer
	log := newDedupTestLogger(&buf)

	// When a sensitive key is stamped twice, only the last value
	// should appear — and it must be redacted.
	log = log.With(slog.String("password", "first"))
	log.Info("auth", slog.String("password", "second-longer"))

	out := buf.String()
	if strings.Contains(out, "first") || strings.Contains(out, "second-longer") {
		t.Errorf("leaked sensitive value: %q", out)
	}
	if n := countSubstring(out, "password="); n != 1 {
		t.Errorf("expected 1 password=; got %d in %q", n, out)
	}
	// The surviving (last) value has length 13 ("second-longer").
	if !strings.Contains(out, "<redacted len=13>") {
		t.Errorf("expected redaction marker for last value; got %q", out)
	}
}

func TestMergeLastWinsPreservesLastPosition(t *testing.T) {
	base := []slog.Attr{
		slog.String("ui", "cli"),
		slog.String("cli_version", "v1"),
	}
	extra := []slog.Attr{
		slog.String("cmd", "deploy"),
		slog.String("ui", "watch"),
		slog.String("app", "hello"),
	}
	got := mergeLastWins(base, extra)

	// Expected order: cli_version, cmd, ui (last occurrence at its
	// last-seen position), app.
	wantKeys := []string{"cli_version", "cmd", "ui", "app"}
	if len(got) != len(wantKeys) {
		t.Fatalf("len(got)=%d want %d: %+v", len(got), len(wantKeys), got)
	}
	for i, want := range wantKeys {
		if got[i].Key != want {
			t.Errorf("got[%d].Key = %q, want %q", i, got[i].Key, want)
		}
	}
	// The ui attr's surviving value must be "watch".
	for _, a := range got {
		if a.Key == "ui" && a.Value.String() != "watch" {
			t.Errorf("ui value = %q, want %q", a.Value.String(), "watch")
		}
	}
}

func TestMergeLastWinsEmptyInputs(t *testing.T) {
	if got := mergeLastWins(nil, nil); got != nil {
		t.Errorf("expected nil for empty inputs; got %+v", got)
	}
	base := []slog.Attr{slog.String("a", "1")}
	if got := mergeLastWins(base, nil); len(got) != 1 || got[0].Key != "a" {
		t.Errorf("base-only merge wrong: %+v", got)
	}
	extra := []slog.Attr{slog.String("b", "2")}
	if got := mergeLastWins(nil, extra); len(got) != 1 || got[0].Key != "b" {
		t.Errorf("extra-only merge wrong: %+v", got)
	}
}
