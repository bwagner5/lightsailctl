package watch

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
	strategy, err := buildAndStage(context.Background(), log, staging, "deploy/123-abc.tar.gz", noPush)
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
	_, err := buildAndStage(context.Background(), log, staging, "deploy/123-abc.tar.gz", noPush)
	if err == nil {
		t.Fatalf("want error for unimplemented strategy, got nil")
	}
}

// TestBuildAndStage_Unknown: unknown source tree errors out.
func TestBuildAndStage_Unknown(t *testing.T) {
	staging := t.TempDir()
	log := slog.New(slog.DiscardHandler)
	_, err := buildAndStage(context.Background(), log, staging, "deploy/123-abc.tar.gz", func(string) {})
	if err == nil {
		t.Fatalf("want error for unknown tree, got nil")
	}
}

// TestImageTagFor: deploy keys produce stable tags ending in the
// SHA so images can be deterministically pruned by their dangling
// status without colliding with unrelated user images.
func TestImageTagFor(t *testing.T) {
	cases := map[string]string{
		"deploy/1714000000-abc1234.tar.gz": "lightsail-app:abc1234",
		"deploy/abc1234.tar.gz":            "lightsail-app:abc1234",
		"":                                 "lightsail-app:latest",
	}
	for in, want := range cases {
		if got := imageTagFor(in); got != want {
			t.Errorf("imageTagFor(%q) = %q; want %q", in, got, want)
		}
	}
}

// TestWriteSyntheticCompose: the file lands at the expected path
// and findCompose picks it up as a fallback.
func TestWriteSyntheticCompose(t *testing.T) {
	staging := t.TempDir()
	if err := writeSyntheticCompose(staging, "lightsail-app:abc", 3000); err != nil {
		t.Fatalf("writeSyntheticCompose: %v", err)
	}
	cf := findCompose(staging)
	if cf == "" {
		t.Fatalf("findCompose did not pick up the synthetic file")
	}
	body, err := os.ReadFile(cf)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"lightsail-app:abc"`, `"3000:3000"`, `PORT: "3000"`} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("missing %q in generated compose:\n%s", want, body)
		}
	}
}

// TestFindCompose_UserAuthoredWinsOverSynthetic: a user-authored
// docker-compose.yml takes precedence over the synthetic file the
// agent writes. This means a user can drop a compose file next to
// their Dockerfile to override the auto-generated one.
func TestFindCompose_UserAuthoredWinsOverSynthetic(t *testing.T) {
	staging := t.TempDir()
	if err := writeSyntheticCompose(staging, "lightsail-app:abc", 3000); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cf := findCompose(staging)
	if !strings.HasSuffix(cf, "docker-compose.yml") {
		t.Errorf("findCompose = %q; want user-authored docker-compose.yml to win", cf)
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
