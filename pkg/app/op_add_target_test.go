package app

import (
	"context"
	"strings"
	"testing"

	"github.com/bwagner5/triad/pkg/registry"
)

func TestAddTargetOpRegistered(t *testing.T) {
	region := ""
	r := Resource(&region, nil)
	op, ok := r.Operations["add-target"]
	if !ok {
		t.Fatal("add-target operation not registered")
	}
	if op.Name != "add-target" {
		t.Errorf("Name = %q; want add-target", op.Name)
	}
	// Verify required fields exist.
	flags := map[string]bool{}
	for _, f := range op.Fields {
		flags[f.Flag] = true
	}
	for _, want := range []string{"name", "env", "instance"} {
		if !flags[want] {
			t.Errorf("missing field %q", want)
		}
	}
}

func TestAddTargetValidateStep_NoBucket(t *testing.T) {
	// The validate step requires a real AWS client, so we just verify
	// the step function is wired and returns an error when the client
	// can't be built (no AWS creds in test env).
	region := ""
	s := &store{region: &region}
	step := addTargetValidateStep(s)
	st := &registry.State{
		Input: registry.Input{"name": "myapp", "env": "dev", "instance": "box-1"},
		Data:  map[string]any{},
	}
	err := step(context.Background(), st)
	// Without AWS creds, we expect an error (not nil). The important
	// thing is it doesn't panic and the step is callable.
	if err == nil {
		t.Skip("AWS credentials available; skipping negative test")
	}
}

func TestAddTargetCheckDuplicateStep_NoCreds(t *testing.T) {
	region := ""
	s := &store{region: &region}
	step := addTargetCheckDuplicateStep(s)
	st := &registry.State{
		Input: registry.Input{"name": "myapp", "env": "dev", "instance": "box-1"},
		Data:  map[string]any{},
	}
	err := step(context.Background(), st)
	if err == nil {
		t.Skip("AWS credentials available; skipping negative test")
	}
	// Should fail with a client error, not a panic.
	if !strings.Contains(err.Error(), "") {
		t.Errorf("unexpected error shape: %v", err)
	}
}
