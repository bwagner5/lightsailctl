package app

import (
	"strings"
	"testing"

	"github.com/bwagner5/triad/pkg/registry"
)

func TestRenderDeploySummary_CreateNew(t *testing.T) {
	st := &registry.State{
		Input: registry.Input{
			"name":          "hello-world",
			"env":           "dev",
			"region":        "us-east-1",
			"__ni/name":     "ancient-orbit",
			"__ni/region":   "us-east-1",
			"__ni/blueprint": "amazon_linux_2023",
			"__ni/bundle":   "large_3_0",
			"agent-path":    "/tmp/lightsailctl",
		},
		Data: map[string]any{"strategy": "create-new"},
	}
	got := renderDeploySummary(st)
	for _, want := range []string{
		"Application",
		"hello-world",
		"Environment",
		"dev",
		"Region",
		"us-east-1",
		"Lightsail Instance",
		"create new",
		"ancient-orbit",
		"amazon_linux_2023",
		"large_3_0",
		"Agent binary",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q\n---\n%s", want, got)
		}
	}
	// Sanity: no raw internal flag names should leak.
	if strings.Contains(got, "__ni/") {
		t.Errorf("summary exposes internal namespace:\n%s", got)
	}
}

func TestRenderDeploySummary_UseExisting(t *testing.T) {
	st := &registry.State{
		Input: registry.Input{
			"name":     "hello-world",
			"env":      "dev",
			"region":   "us-east-1",
			"instance": "my-box",
		},
		Data: map[string]any{"strategy": "use-existing"},
	}
	got := renderDeploySummary(st)
	if !strings.Contains(got, "use existing") {
		t.Errorf("summary missing 'use existing':\n%s", got)
	}
	if !strings.Contains(got, "my-box") {
		t.Errorf("summary missing instance name:\n%s", got)
	}
	// Must NOT include New Lightsail Instance fields.
	if strings.Contains(got, "Blueprint") || strings.Contains(got, "Bundle") {
		t.Errorf("summary exposes new-instance fields in use-existing mode:\n%s", got)
	}
}

func TestSkipConfirm_NonInteractive(t *testing.T) {
	nonInt := true
	s := &store{nonInteractive: &nonInt}
	if !skipConfirm(s)(&registry.State{Input: registry.Input{}}) {
		t.Errorf("skip should be true under -y")
	}
}

func TestSkipConfirm_AlreadyAnswered(t *testing.T) {
	nonInt := false
	s := &store{nonInteractive: &nonInt}
	st := &registry.State{Input: registry.Input{"deploy-confirm": "true"}}
	if !skipConfirm(s)(st) {
		t.Errorf("skip should be true when deploy-confirm is already set")
	}
}
