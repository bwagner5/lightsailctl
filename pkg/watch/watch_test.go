package watch

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/lightsailctl/pkg/build"
)

// TestBuildAndStage_Compose: compose path detects and returns
// without erroring. The actual `compose up --build` happens later
// in swap().
func TestBuildAndStage_Compose(t *testing.T) {
	staging := t.TempDir()
	if err := os.WriteFile(filepath.Join(staging, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	noPush := func(string) {}
	strategy, err := buildAndStage(context.Background(), log, staging, noPush)
	if err != nil {
		t.Fatalf("buildAndStage: %v", err)
	}
	if strategy != build.StrategyCompose {
		t.Errorf("strategy = %v; want compose", strategy)
	}
}

// TestBuildAndStage_BuildpackNotImplemented: a recognized non-compose
// strategy returns a clear error rather than silently failing or
// proceeding into swap. This is what protects the live stack from
// being torn down by an unimplemented build path.
func TestBuildAndStage_BuildpackNotImplemented(t *testing.T) {
	staging := t.TempDir()
	if err := os.WriteFile(filepath.Join(staging, "go.mod"), []byte("module x"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	noPush := func(string) {}
	_, err := buildAndStage(context.Background(), log, staging, noPush)
	if err == nil {
		t.Fatalf("want error for unimplemented strategy, got nil")
	}
}

// TestBuildAndStage_Unknown: unknown source tree errors out.
func TestBuildAndStage_Unknown(t *testing.T) {
	staging := t.TempDir()
	log := slog.New(slog.DiscardHandler)
	_, err := buildAndStage(context.Background(), log, staging, func(string) {})
	if err == nil {
		t.Fatalf("want error for unknown tree, got nil")
	}
}

// TestSetPhase_OnlyChangesOnTransition: re-setting the same phase
// is a no-op so the watcher doesn't churn the status JSON.
func TestSetPhase_OnlyChangesOnTransition(t *testing.T) {
	p := &phaseState{}
	setPhase(p, "downloading")
	first := p.Since
	if p.Phase != "downloading" || first.IsZero() {
		t.Fatalf("phase not set: %+v", p)
	}
	setPhase(p, "downloading")
	if !p.Since.Equal(first) {
		t.Errorf("Since changed on no-op transition: was %v, now %v", first, p.Since)
	}
	setPhase(p, "extracting")
	if p.Phase != "extracting" {
		t.Errorf("Phase = %q; want extracting", p.Phase)
	}
	if p.Since.Equal(first) {
		t.Errorf("Since not updated on real transition")
	}
}

// TestSetPhase_NilSafe: nil phaseState is a no-op (defensive — keeps
// callers tidy without nil-checks).
func TestSetPhase_NilSafe(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("panic on nil phase: %v", r)
		}
	}()
	setPhase(nil, "downloading")
}

// errString returns the message of err or "<nil>".
func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	var pErr *os.PathError
	if errors.As(err, &pErr) {
		return pErr.Error()
	}
	return err.Error()
}
