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
	flags := map[string]bool{}
	for _, f := range op.Fields {
		flags[f.Flag] = true
	}
	for _, want := range []string{"name", "env", "instance", "force"} {
		if !flags[want] {
			t.Errorf("missing field %q", want)
		}
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
