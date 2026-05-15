// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package names

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultAppNameFromGitOrigin(t *testing.T) {
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, ".git"))
	mustWrite(t, filepath.Join(dir, ".git", "config"), `[core]
	repositoryformatversion = 0
[remote "origin"]
	url = git@github.com:aws/Lightsail-CTL.git
`)
	chdir(t, dir)
	if got := DefaultAppName(); got != "lightsail-ctl" {
		t.Errorf("DefaultAppName = %q; want lightsail-ctl", got)
	}
}

func TestDefaultAppNameFromDirWhenNoOrigin(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "my-cool-app")
	mustMkdir(t, filepath.Join(repo, ".git"))
	mustWrite(t, filepath.Join(repo, ".git", "config"), "[core]\n")
	chdir(t, repo)
	if got := DefaultAppName(); got != "my-cool-app" {
		t.Errorf("DefaultAppName = %q; want my-cool-app", got)
	}
}

func TestDefaultAppNameFallsBackToRandom(t *testing.T) {
	chdir(t, t.TempDir())
	got := DefaultAppName()
	if got == "" || !containsDash(got) {
		t.Errorf("DefaultAppName without a repo = %q; want random adj-noun", got)
	}
}

func containsDash(s string) bool {
	for _, r := range s {
		if r == '-' {
			return true
		}
	}
	return false
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
}
