package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/config"
)

// TestRemoveLocalConfStep_Matches asserts the conf is deleted when its
// App matches the deleted-app name.
func TestRemoveLocalConfStep_Matches(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	p := filepath.Join(dir, config.Filename)
	if err := (&config.Config{App: "my-app", Env: "dev"}).Save(p); err != nil {
		t.Fatal(err)
	}

	st := &registry.State{Input: registry.Input{"name": "my-app"}, Data: map[string]any{}}
	if err := removeLocalConfStep(context.Background(), st); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("expected conf removed, stat err=%v", err)
	}
}

// TestRemoveLocalConfStep_WrongApp asserts a conf for a DIFFERENT app
// is left alone when deleting an unrelated app.
func TestRemoveLocalConfStep_WrongApp(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	p := filepath.Join(dir, config.Filename)
	if err := (&config.Config{App: "other-app", Env: "dev"}).Save(p); err != nil {
		t.Fatal(err)
	}

	st := &registry.State{Input: registry.Input{"name": "my-app"}, Data: map[string]any{}}
	if err := removeLocalConfStep(context.Background(), st); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("conf for other-app should be untouched: %v", err)
	}
}

// TestSkipIfNoMatchingLocalConf_NoConf: outside a project dir the step
// must skip itself so no empty row shows in the TUI.
func TestSkipIfNoMatchingLocalConf_NoConf(t *testing.T) {
	t.Chdir(t.TempDir())
	st := &registry.State{Input: registry.Input{"name": "anything"}}
	if !skipIfNoMatchingLocalConf(st) {
		t.Errorf("skip should return true when no conf is present")
	}
}
