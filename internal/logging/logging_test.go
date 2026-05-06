// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package logging

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwagner5/triad/pkg/trace"
)

// newTestLogger builds a logger backed by a buffer, using the same
// redacting handler chain as production so tests exercise the real code.
func newTestLogger(buf *bytes.Buffer) *slog.Logger {
	text := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: trace.LevelVar})
	return slog.New(newRedactingHandler(text))
}

func TestRedactingHandlerMatchingKey(t *testing.T) {
	t.Cleanup(trace.ResetForTest)
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	log.Info("auth", "password", "topkek")
	out := buf.String()
	if strings.Contains(out, "topkek") {
		t.Errorf("leaked sensitive value: %q", out)
	}
	if !strings.Contains(out, "<redacted len=6>") {
		t.Errorf("expected redaction marker; got %q", out)
	}
}

func TestRedactingHandlerPreservesNonMatching(t *testing.T) {
	t.Cleanup(trace.ResetForTest)
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	log.Info("app", "name", "my-app", "region", "us-east-2")
	out := buf.String()
	if !strings.Contains(out, "name=my-app") || !strings.Contains(out, "region=us-east-2") {
		t.Errorf("non-sensitive attrs altered; got %q", out)
	}
}

func TestRedactingHandlerDoesNotRecurseIntoGroup(t *testing.T) {
	t.Cleanup(trace.ResetForTest)
	var buf bytes.Buffer
	log := newTestLogger(&buf)
	// Document the design: values inside slog.Group are NOT inspected.
	// Engineers must keep sensitive values at the top level.
	log.Info("bad", slog.Group("inner", slog.String("password", "topkek")))
	out := buf.String()
	// Intentional: the handler does not recurse, so the group's value
	// escapes the redactor. This test locks in that contract.
	if !strings.Contains(out, "topkek") {
		t.Errorf("expected group contents to pass through (by design); got %q", out)
	}
}

func TestLazyFileOnlyOpensOnWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lazy.log")
	lw := newLazyFile(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("lazy file should not exist before Write; stat err = %v", err)
	}
	if _, err := lw.Write([]byte("hello\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("lazy file missing after Write: %v", err)
	}
	if err := lw.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestInitFileDestWritesRecord(t *testing.T) {
	t.Cleanup(trace.ResetForTest)
	dir := t.TempDir()
	path := filepath.Join(dir, "out.log")
	logger, shutdown, err := Init(Options{
		Dest: DestFile,
		Path: path,
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = shutdown() })
	logger.Info("deploy started", slog.String("app", "my-app"))
	_ = shutdown() // flush before reading
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(b), "deploy started") {
		t.Errorf("expected log line in file; got %q", b)
	}
}

func TestInitStderrDest(t *testing.T) {
	t.Cleanup(trace.ResetForTest)
	_, shutdown, err := Init(Options{Dest: DestStderr})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = shutdown() })
	if p := CurrentPath(); p != "" {
		t.Errorf("CurrentPath should be empty for stderr; got %q", p)
	}
}

func TestInitNoneDest(t *testing.T) {
	t.Cleanup(trace.ResetForTest)
	logger, shutdown, err := Init(Options{Dest: DestNone})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = shutdown() })
	// Must not panic and must not write anywhere observable.
	logger.Info("silenced")
}

func TestInitAutoResolvesUnderHome(t *testing.T) {
	t.Cleanup(trace.ResetForTest)
	home := t.TempDir()
	t.Setenv("HOME", home)
	logger, shutdown, err := Init(Options{Dest: DestFile})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = shutdown() })
	logger.Info("hello")
	_ = shutdown() // flush
	path := CurrentPath()
	if !strings.HasPrefix(path, filepath.Join(home, ".lightsailctl", "logs")) {
		t.Errorf("CurrentPath = %q; want prefix under %s/.lightsailctl/logs", path, home)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("auto-resolved log missing: %v", err)
	}
}

func TestInitUnknownDest(t *testing.T) {
	_, _, err := Init(Options{Dest: Dest("bogus")})
	if err == nil {
		t.Errorf("Init accepted bogus dest")
	}
}

func TestPruneLogsByAge(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := filepath.Join(dir, "old.log")
	if err := os.WriteFile(old, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Backdate old file well past the cutoff.
	past := now.Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(dir, "fresh.log")
	if err := os.WriteFile(fresh, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pruneLogs(dir, func() time.Time { return now }, 14*24*time.Hour, 100); err != nil {
		t.Fatalf("pruneLogs: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("old log should be deleted; stat err=%v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh log should survive: %v", err)
	}
}

func TestPruneLogsByCount(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	// Create 5 files with staggered mod times.
	for i := 0; i < 5; i++ {
		p := filepath.Join(dir, "f"+string(rune('a'+i))+".log")
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		ts := now.Add(time.Duration(i) * time.Hour)
		_ = os.Chtimes(p, ts, ts)
	}
	if err := pruneLogs(dir, func() time.Time { return now }, 365*24*time.Hour, 3); err != nil {
		t.Fatalf("pruneLogs: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Errorf("expected 3 logs after prune; got %d", len(entries))
	}
}

func TestPruneLogsIgnoresNonLogFiles(t *testing.T) {
	dir := t.TempDir()
	other := filepath.Join(dir, "keepme.txt")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-30 * 24 * time.Hour)
	_ = os.Chtimes(other, past, past)
	if err := pruneLogs(dir, time.Now, 14*24*time.Hour, 100); err != nil {
		t.Fatalf("pruneLogs: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("non-.log file was deleted: %v", err)
	}
}

func TestAWSLoggerRoutesToSlog(t *testing.T) {
	t.Cleanup(trace.ResetForTest)
	var buf bytes.Buffer
	ctx := trace.IntoContext(context.Background(), newTestLogger(&buf))
	_ = ctx
	trace.SetLevel(trace.LevelTrace)
	adapter := AWSLogger(newTestLogger(&buf))
	adapter.Logf("WARN", "retry %d", 3)
	out := buf.String()
	if !strings.Contains(out, "aws sdk") || !strings.Contains(out, "retry 3") {
		t.Errorf("expected SDK adapter output; got %q", out)
	}
	if !strings.Contains(out, "source=aws-sdk") {
		t.Errorf("expected source=aws-sdk; got %q", out)
	}
}

func TestInitLazyOpenWhenNothingLogged(t *testing.T) {
	t.Cleanup(trace.ResetForTest)
	dir := t.TempDir()
	path := filepath.Join(dir, "lazy.log")
	_, shutdown, err := Init(Options{Dest: DestFile, Path: path})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	// Don't log anything; then shut down.
	_ = shutdown()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("lazy file should not exist when nothing logged; stat err=%v", err)
	}
}
