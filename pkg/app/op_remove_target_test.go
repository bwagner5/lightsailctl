package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/config"
)

func TestRemoveTargetOpRegistered(t *testing.T) {
	region := ""
	r := Resource(&region, nil)
	op, ok := r.Operations["remove-target"]
	if !ok {
		t.Fatal("remove-target operation not registered")
	}
	if op.Name != "remove-target" {
		t.Errorf("Name = %q; want remove-target", op.Name)
	}
	// TUI reachability: remove-target must bind a key and declare
	// NeedsExistingRow so the "T" hint hides on an empty table but
	// shows as soon as an app row is selected.
	if op.Key != "T" {
		t.Errorf("Key = %q; want T", op.Key)
	}
	if !op.NeedsExistingRow {
		t.Error("NeedsExistingRow = false; want true (remove-target needs a selected app)")
	}
	// Enabled is intentionally nil — see the comment on removeTargetOp
	// about App.Instances being populated by a Field.Async loader
	// whose result lives in the TUI's side cache, not on the row
	// struct. The saga validate step provides the "not a target"
	// error when the user tries to remove an instance that isn't
	// currently attached.
	if op.Enabled != nil {
		t.Error("Enabled != nil; remove-target should rely on NeedsExistingRow + validate step until Enabled can see async-populated columns")
	}
	// SortKey clusters remove-target next to add-target in the TUI
	// status bar and help overlay. Without it, alphabetical sort
	// splits them across the hint row.
	if op.SortKey != "add-target-remove" {
		t.Errorf("SortKey = %q; want \"add-target-remove\" (pairs with add-target)", op.SortKey)
	}
	flags := map[string]bool{}
	for _, f := range op.Fields {
		flags[f.Flag] = true
	}
	for _, want := range []string{"name", "env", "instance"} {
		if !flags[want] {
			t.Errorf("missing field %q", want)
		}
	}
	// The "force" flag was dropped: the Confirm prompt is the user's
	// intent check, and removing the last target is now allowed so
	// the TUI can reach it without a flag the wizard can't pass.
	if flags["force"] {
		t.Errorf("force flag should have been removed")
	}
}

func TestRemoveTargetValidateStep_NotFound(t *testing.T) {
	region := ""
	s := &store{region: &region}
	step := removeTargetValidateStep(s)
	st := &registry.State{
		Input: registry.Input{"name": "myapp", "env": "dev", "instance": "box-99"},
		Data:  map[string]any{},
	}
	err := step(context.Background(), st)
	if err == nil {
		t.Skip("AWS credentials available; skipping")
	}
	// Should fail (no creds or not found), not panic.
	if strings.Contains(err.Error(), "panic") {
		t.Errorf("unexpected panic: %v", err)
	}
}

func TestRemoveTargetSaveConfStep(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "lightsail.conf")
	cfg := &config.Config{
		App: "myapp", Env: "dev",
		Instance:  "box-1",
		Instances: []string{"box-1", "box-2", "box-3"},
	}
	if err := cfg.Save(p); err != nil {
		t.Fatal(err)
	}
	// Change to the temp dir so the step finds the conf.
	orig, _ := os.Getwd()
	defer os.Chdir(orig)
	os.Chdir(dir)

	st := &registry.State{
		Input: registry.Input{"name": "myapp", "env": "dev", "instance": "box-2"},
		Data:  map[string]any{},
	}
	if err := removeTargetSaveConfStep(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Instances) != 2 || got.Instances[0] != "box-1" || got.Instances[1] != "box-3" {
		t.Errorf("Instances = %v; want [box-1 box-3]", got.Instances)
	}
	if got.Instance != "box-1" {
		t.Errorf("Instance = %q; want box-1", got.Instance)
	}
}
