// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveThenLoad(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "lightsail.conf")
	orig := &Config{
		App: "foo", Env: "dev", Region: "us-east-2",
		Instance: "box-1", AgentPath: "/tmp/lightsailctl",
		Ignore: []string{".venv"},
	}
	if err := orig.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.App != "foo" || got.Env != "dev" || got.Region != "us-east-2" ||
		got.Instance != "box-1" || got.AgentPath != "/tmp/lightsailctl" ||
		len(got.Ignore) != 1 {
		t.Errorf("roundtrip lost data: %+v", got)
	}
	if got.Path != p {
		t.Errorf("Path = %q; want %q", got.Path, p)
	}
}

func TestAllInstancesSingleLegacy(t *testing.T) {
	c := &Config{Instance: "box-1"}
	got := c.AllInstances()
	if len(got) != 1 || got[0] != "box-1" {
		t.Errorf("AllInstances() = %v; want [box-1]", got)
	}
}

func TestAllInstancesDeduplicates(t *testing.T) {
	c := &Config{Instance: "box-1", Instances: []string{"box-1", "box-2"}}
	got := c.AllInstances()
	if len(got) != 2 || got[0] != "box-1" || got[1] != "box-2" {
		t.Errorf("AllInstances() = %v; want [box-1 box-2]", got)
	}
}

func TestAllInstancesOnlyList(t *testing.T) {
	c := &Config{Instances: []string{"box-2", "box-3"}}
	got := c.AllInstances()
	if len(got) != 2 || got[0] != "box-2" || got[1] != "box-3" {
		t.Errorf("AllInstances() = %v; want [box-2 box-3]", got)
	}
}

func TestSaveInstancesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "lightsail.conf")
	orig := &Config{
		App: "myapp", Env: "prod",
		Instances: []string{"box-1", "box-2"},
	}
	if err := orig.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	// Instance should be synced to first entry for backward compat.
	if got.Instance != "box-1" {
		t.Errorf("Instance = %q; want box-1", got.Instance)
	}
	if len(got.Instances) != 2 || got.Instances[0] != "box-1" || got.Instances[1] != "box-2" {
		t.Errorf("Instances = %v; want [box-1 box-2]", got.Instances)
	}
}

func TestFindWalksUp(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "deep", "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "lightsail.conf")
	if err := os.WriteFile(root, []byte("app: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Find(sub)
	if got != root {
		t.Errorf("Find(%s) = %q; want %q", sub, got, root)
	}
}

func TestFindAbsent(t *testing.T) {
	if got := Find(t.TempDir()); got != "" {
		t.Errorf("Find on empty tree = %q; want \"\"", got)
	}
}
