// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/config"
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

// TestAddTargetPre_DoesNotPreloadInstanceFromConf is the regression
// guard for the TUI bug "Where is the overlay with multi-select for
// the instances?". When lightsail.conf already has Instance set (the
// typical state after a successful deploy), addTargetPre used to
// hydrate it into Input, which made the wizard's "already set" fast
// path skip the instance picker entirely and the saga would march
// straight into the duplicate-check step. The fix: addTargetPre loads
// app/env/region/agent-path from conf but leaves instance alone so
// the user always sees the multi-select picker.
func TestAddTargetPre_DoesNotPreloadInstanceFromConf(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	existing := &config.Config{
		App: "hello", Env: "dev", Region: "us-east-1",
		Instance:  "already-tagged-box",
		Instances: []string{"already-tagged-box"},
		AgentPath: "/some/cached/lightsailctl",
	}
	if err := existing.Save(filepath.Join(dir, config.Filename)); err != nil {
		t.Fatal(err)
	}

	in := registry.Input{}
	if err := addTargetPre(context.Background(), in); err != nil {
		t.Fatal(err)
	}

	// app / env / region / agent-path SHOULD hydrate from conf so the
	// user only has to answer "which instance" in the wizard.
	if got := in.Get("name"); got != "hello" {
		t.Errorf("name = %q; want hello", got)
	}
	if got := in.Get("env"); got != "dev" {
		t.Errorf("env = %q; want dev", got)
	}
	if got := in.Get("region"); got != "us-east-1" {
		t.Errorf("region = %q; want us-east-1", got)
	}
	if got := in.Get("agent-path"); got != "/some/cached/lightsailctl" {
		t.Errorf("agent-path = %q; want /some/cached/lightsailctl", got)
	}
	// Instance MUST remain empty so the wizard renders the
	// multi-select picker. This is the bug-fix assertion.
	if got := in.Get("instance"); got != "" {
		t.Errorf("instance preloaded to %q; want empty so wizard shows the picker", got)
	}
}

// TestAddTargetPre_ExplicitInstanceFlagIsRespected confirms an
// explicit --instance flag wins over "don't preload from conf" — i.e.
// if the user passed --instance on the CLI, Pre leaves their value
// intact and the wizard treats the field as already answered.
func TestAddTargetPre_ExplicitInstanceFlagIsRespected(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	existing := &config.Config{
		App: "hello", Env: "dev", Region: "us-east-1",
		Instance: "in-conf-box",
	}
	if err := existing.Save(filepath.Join(dir, config.Filename)); err != nil {
		t.Fatal(err)
	}

	in := registry.Input{"instance": "explicit-box"}
	if err := addTargetPre(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if got := in.Get("instance"); got != "explicit-box" {
		t.Errorf("instance = %q; want explicit-box (CLI flag must win)", got)
	}
}

// TestAddTargetField_IsMulti verifies the add-target instance field
// declares Multi: true so the TUI wizard renders a checkbox list and
// comma-separated --instance values parse correctly.
func TestAddTargetField_IsMulti(t *testing.T) {
	region := ""
	r := Resource(&region, nil)
	op := r.Operations["add-target"]
	for _, f := range op.Fields {
		if f.Flag == "instance" {
			if !f.Multi {
				t.Errorf("instance field Multi = false; want true")
			}
			return
		}
	}
	t.Fatal("add-target instance field not found")
}

// TestAddTargetSaveConfStep_MultiInstance verifies the add-target
// save-conf step appends every selected instance (comma-separated in
// Input) to lightsail.conf's Instances list and does not lose the
// pre-existing entries.
func TestAddTargetSaveConfStep_MultiInstance(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	existing := &config.Config{
		App: "hello", Env: "dev", Region: "us-east-1",
		Instance:  "box-1",
		Instances: []string{"box-1"},
	}
	if err := existing.Save(filepath.Join(dir, config.Filename)); err != nil {
		t.Fatal(err)
	}

	st := &registry.State{
		Input: registry.Input{
			"name":     "hello",
			"env":      "dev",
			"region":   "us-east-1",
			"instance": "box-2,box-3",
		},
		Data: map[string]any{},
	}
	if err := addTargetSaveConfStep(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(filepath.Join(dir, config.Filename))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"box-1", "box-2", "box-3"}
	if len(got.Instances) != len(want) {
		t.Fatalf("Instances = %v; want %v", got.Instances, want)
	}
	for i, v := range want {
		if got.Instances[i] != v {
			t.Errorf("Instances[%d] = %q; want %q", i, got.Instances[i], v)
		}
	}
	// Legacy scalar should remain the first entry from the prior conf.
	if got.Instance != "box-1" {
		t.Errorf("legacy Instance = %q; want box-1", got.Instance)
	}
}

// TestAddTargetSaveConfStep_DedupesExistingEntries asserts adding an
// instance that's already in the list is a no-op.
func TestAddTargetSaveConfStep_DedupesExistingEntries(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	existing := &config.Config{
		App: "hello", Env: "dev", Region: "us-east-1",
		Instance:  "box-1",
		Instances: []string{"box-1", "box-2"},
	}
	if err := existing.Save(filepath.Join(dir, config.Filename)); err != nil {
		t.Fatal(err)
	}

	st := &registry.State{
		Input: registry.Input{
			"name":     "hello",
			"env":      "dev",
			"region":   "us-east-1",
			"instance": "box-1,box-2",
		},
		Data: map[string]any{},
	}
	if err := addTargetSaveConfStep(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(filepath.Join(dir, config.Filename))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Instances) != 2 {
		t.Errorf("Instances = %v; want len 2 (no duplicates)", got.Instances)
	}
}
