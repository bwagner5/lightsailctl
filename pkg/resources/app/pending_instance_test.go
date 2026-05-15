// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/config"
)

// TestPreloadFromConf_ResumesPendingInstanceDraft verifies that a
// conf carrying a pending-instance draft re-populates both the yes/no
// strategy gate and every __ni/* field so the wizard skips those
// prompts on the re-run.
func TestPreloadFromConf_ResumesPendingInstanceDraft(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	cfg := &config.Config{
		App: "hello", Env: "dev",
		PendingInstance: &config.PendingInstance{
			Name:          "silent-cosmos",
			Region:        "us-east-2",
			BlueprintType: "os",
			Blueprint:     "amazon_linux_2023",
			Bundle:        "large_3_0",
			IPAddressType: "dualstack",
			Monitoring:    "false",
		},
	}
	p := filepath.Join(dir, config.Filename)
	if err := cfg.Save(p); err != nil {
		t.Fatal(err)
	}

	in := registry.Input{}
	if err := preloadFromConf(context.Background(), in); err != nil {
		t.Fatalf("preload: %v", err)
	}
	if in.Get("create-new-instance") != "true" {
		t.Errorf("create-new-instance = %q; want 'true'", in.Get("create-new-instance"))
	}
	cases := []struct{ k, want string }{
		{"__ni/name", "silent-cosmos"},
		{"__ni/region", "us-east-2"},
		{"__ni/blueprint-type", "os"},
		{"__ni/blueprint", "amazon_linux_2023"},
		{"__ni/bundle", "large_3_0"},
		{"__ni/ip-address-type", "dualstack"},
		{"__ni/monitoring", "false"},
	}
	for _, tc := range cases {
		if got := in.Get(tc.k); got != tc.want {
			t.Errorf("%s = %q; want %q", tc.k, got, tc.want)
		}
	}
}

// TestPreloadFromConf_IgnoresDraftWhenInstanceIsSet asserts that a
// conf with BOTH an instance name AND a stale draft prefers the real
// instance (instance != "" wins). Belt-and-suspenders for cases where
// the draft wasn't cleared on a prior successful deploy.
func TestPreloadFromConf_IgnoresDraftWhenInstanceIsSet(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	cfg := &config.Config{
		App: "hello", Env: "dev",
		Region:   "us-east-2",
		Instance: "my-box",
		PendingInstance: &config.PendingInstance{
			Name: "orphan-draft", Region: "us-west-1",
		},
	}
	if err := cfg.Save(filepath.Join(dir, config.Filename)); err != nil {
		t.Fatal(err)
	}

	in := registry.Input{}
	if err := preloadFromConf(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if in.Get("instance") != "my-box" {
		t.Errorf("instance = %q; want 'my-box'", in.Get("instance"))
	}
	if in.Get("__ni/name") != "" {
		t.Errorf("__ni/name should not be set when instance exists: %q", in.Get("__ni/name"))
	}
	if in.Get("create-new-instance") != "" {
		t.Errorf("create-new-instance should not be forced when instance exists: %q",
			in.Get("create-new-instance"))
	}
}

// TestPendingInstanceFromInput_EmptyReturnsNil: no __ni/* answers means
// no pending-instance draft to persist.
func TestPendingInstanceFromInput_EmptyReturnsNil(t *testing.T) {
	if got := pendingInstanceFromInput(registry.Input{}); got != nil {
		t.Errorf("empty input produced a draft: %+v", got)
	}
}

// TestPendingInstanceFromInput_PopulatesDraft: all namespaced fields
// round-trip cleanly.
func TestPendingInstanceFromInput_PopulatesDraft(t *testing.T) {
	in := registry.Input{
		"__ni/name":            "silent-cosmos",
		"__ni/region":          "us-east-2",
		"__ni/blueprint-type":  "os",
		"__ni/blueprint":       "amazon_linux_2023",
		"__ni/bundle":          "large_3_0",
		"__ni/ip-address-type": "dualstack",
		"__ni/user-data":       "/tmp/userdata.sh",
		"__ni/monitoring":      "true",
	}
	got := pendingInstanceFromInput(in)
	if got == nil {
		t.Fatal("nil draft")
	}
	if got.Name != "silent-cosmos" || got.Region != "us-east-2" ||
		got.BlueprintType != "os" || got.Blueprint != "amazon_linux_2023" ||
		got.Bundle != "large_3_0" || got.IPAddressType != "dualstack" ||
		got.UserData != "/tmp/userdata.sh" || got.Monitoring != "true" {
		t.Errorf("draft = %+v", got)
	}
}

// TestSaveConfigStep_WritesPendingInstanceOnAbort covers the abort
// round-trip: aborted + strategy=create-new + Instance unset → conf
// gains a PendingInstance block.
func TestSaveConfigStep_WritesPendingInstanceOnAbort(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	st := &registry.State{
		Input: registry.Input{
			"name":           "hello",
			"env":            "dev",
			"__ni/name":      "silent-cosmos",
			"__ni/region":    "us-east-2",
			"__ni/blueprint": "amazon_linux_2023",
			"__ni/bundle":    "large_3_0",
		},
		Data: map[string]any{
			"strategy": "create-new",
			"aborted":  true,
		},
	}
	if err := saveConfigStep(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(dir, config.Filename))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PendingInstance == nil {
		t.Fatalf("saved conf has no PendingInstance; cfg=%+v", cfg)
	}
	if cfg.PendingInstance.Name != "silent-cosmos" {
		t.Errorf("draft name = %q", cfg.PendingInstance.Name)
	}
}

// TestSaveConfigStep_DropsPendingOnSuccessfulCreate asserts the
// draft is NOT re-persisted after a successful create (saga writes
// Instance into Input; strategy remains create-new but aborted=false).
func TestSaveConfigStep_DropsPendingOnSuccessfulCreate(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	// Seed an existing conf that did carry a draft (simulating the
	// prior aborted run).
	cfg := &config.Config{
		App: "hello", Env: "dev",
		PendingInstance: &config.PendingInstance{Name: "old-draft"},
	}
	if err := cfg.Save(filepath.Join(dir, config.Filename)); err != nil {
		t.Fatal(err)
	}

	st := &registry.State{
		Input: registry.Input{
			"name":     "hello",
			"env":      "dev",
			"region":   "us-east-2",
			"instance": "silent-cosmos",
		},
		Data: map[string]any{"strategy": "create-new", "aborted": false},
	}
	if err := saveConfigStep(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(filepath.Join(dir, config.Filename))
	if err != nil {
		t.Fatal(err)
	}
	if got.PendingInstance != nil {
		t.Errorf("expected PendingInstance cleared after successful create, got %+v", got.PendingInstance)
	}
	if got.Instance != "silent-cosmos" {
		t.Errorf("instance = %q; want silent-cosmos", got.Instance)
	}
	// Sanity: os.Stat the file to make sure we wrote something real.
	if _, serr := os.Stat(filepath.Join(dir, config.Filename)); serr != nil {
		t.Fatal(serr)
	}
}

// TestSaveConfigStep_PreservesInstancesList verifies that saveConfigStep
// preserves the existing Instances list and adds the current instance.
func TestSaveConfigStep_PreservesInstancesList(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	// Seed a conf with an existing instances list.
	cfg := &config.Config{
		App: "hello", Env: "dev", Region: "us-east-1",
		Instance:  "box-1",
		Instances: []string{"box-1", "box-2"},
	}
	if err := cfg.Save(filepath.Join(dir, config.Filename)); err != nil {
		t.Fatal(err)
	}

	st := &registry.State{
		Input: registry.Input{
			"name":     "hello",
			"env":      "dev",
			"region":   "us-east-1",
			"instance": "box-1",
		},
		Data: map[string]any{},
	}
	if err := saveConfigStep(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(filepath.Join(dir, config.Filename))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Instances) != 2 || got.Instances[0] != "box-1" || got.Instances[1] != "box-2" {
		t.Errorf("Instances = %v; want [box-1 box-2]", got.Instances)
	}
}

// TestSaveConfigStep_AddsNewInstance verifies that saveConfigStep adds
// the current instance to the list if not already present.
func TestSaveConfigStep_AddsNewInstance(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	cfg := &config.Config{
		App: "hello", Env: "dev", Region: "us-east-1",
		Instance:  "box-1",
		Instances: []string{"box-1"},
	}
	if err := cfg.Save(filepath.Join(dir, config.Filename)); err != nil {
		t.Fatal(err)
	}

	st := &registry.State{
		Input: registry.Input{
			"name":     "hello",
			"env":      "dev",
			"region":   "us-east-1",
			"instance": "box-3",
		},
		Data: map[string]any{},
	}
	if err := saveConfigStep(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(filepath.Join(dir, config.Filename))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Instances) != 2 || got.Instances[0] != "box-1" || got.Instances[1] != "box-3" {
		t.Errorf("Instances = %v; want [box-1 box-3]", got.Instances)
	}
}
