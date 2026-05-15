// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"testing"

	"github.com/bwagner5/triad/pkg/registry"
)

// TestCreateOpRegistered pins the shape of the create op: it must
// surface in the TUI via "c", expose a setup-target gate, and
// declare every target-related field as conditional on that gate so
// the infra-only flow doesn't prompt for them.
func TestCreateOpRegistered(t *testing.T) {
	region := ""
	r := Resource(&region, nil)
	op, ok := r.Operations["create"]
	if !ok {
		t.Fatal("create operation not registered")
	}
	if op.Key != "c" {
		t.Errorf("Key = %q; want c", op.Key)
	}
	// Collect fields by flag name for look-ups below.
	fields := map[string]registry.Field{}
	for _, f := range op.Fields {
		fields[f.Flag] = f
	}

	// setup-target must exist, default to "true", and use a bool kind
	// so the wizard renders it as a yes/no choice.
	st, ok := fields["setup-target"]
	if !ok {
		t.Fatal("setup-target field missing")
	}
	if st.Kind != registry.KindBool {
		t.Errorf("setup-target Kind = %v; want KindBool", st.Kind)
	}
	if st.Default != "true" {
		t.Errorf("setup-target Default = %v; want \"true\"", st.Default)
	}

	// The target strategy + new-instance fields MUST declare a When
	// predicate so the wizard skips them when setup-target=false.
	gated := []string{
		"create-new-instance",
		"instance",
		"__ni/name",
		"__ni/region",
		"__ni/blueprint",
		"__ni/bundle",
		"agent-path",
	}
	for _, f := range gated {
		fd, ok := fields[f]
		if !ok {
			t.Errorf("field %q missing", f)
			continue
		}
		if fd.When == nil {
			t.Errorf("field %q has no When predicate; will prompt even when setup-target=false", f)
		}
	}

	// The region field must be promptable (no hard Wizard:false) so
	// it surfaces in the infra-only flow, but gated by When so it
	// stays hidden when a target is being set up.
	reg, ok := fields["region"]
	if !ok {
		t.Fatal("region field missing")
	}
	if reg.Wizard != nil && !*reg.Wizard {
		t.Error("region field Wizard=false; should rely on When so infra-only mode can prompt for it")
	}
	if reg.When == nil {
		t.Error("region field needs a When predicate that shows it in infra-only mode")
	}
}

// TestCreateOp_SetupTargetGate verifies the When predicates on every
// target-related field return false when setup-target is off, and true
// when it's on (with no explicit instance yet). This is the wizard's
// contract: answer "no" to setup-target and the whole downstream
// target prompt chain collapses.
func TestCreateOp_SetupTargetGate(t *testing.T) {
	region := ""
	r := Resource(&region, nil)
	op := r.Operations["create"]
	fields := map[string]registry.Field{}
	for _, f := range op.Fields {
		fields[f.Flag] = f
	}

	off := registry.Input{"setup-target": "false"}
	on := registry.Input{"setup-target": "true"}

	gated := []string{"create-new-instance", "instance", "__ni/name", "agent-path"}
	for _, f := range gated {
		when := fields[f].When
		if when == nil {
			t.Fatalf("field %q: When missing", f)
		}
		if when(off) {
			t.Errorf("field %q: When returned true with setup-target=false", f)
		}
	}

	// With setup-target=true and no instance set, the target picker
	// should open (create-new-instance, instance-or-new). This keeps
	// the existing guided flow working for new users.
	if !fields["create-new-instance"].When(on) {
		t.Error("create-new-instance not prompted when setup-target=true and no instance set")
	}

	// Conversely, the region prompt is only for infra-only mode.
	regWhen := fields["region"].When
	if regWhen(on) {
		t.Error("region prompted when setup-target=true; should only prompt in infra-only mode")
	}
	if !regWhen(off) {
		t.Error("region not prompted when setup-target=false; need it to create the bucket")
	}
}

// TestSkipIfInfraOnly ensures the skip predicate that gates every
// target-related step reports the right thing for each state shape.
func TestSkipIfInfraOnly(t *testing.T) {
	on := &registry.State{Input: registry.Input{"setup-target": "true"}}
	off := &registry.State{Input: registry.Input{"setup-target": "false"}}
	absent := &registry.State{Input: registry.Input{}}

	if skipIfInfraOnly(on) {
		t.Error("skipIfInfraOnly(on) = true; should run target steps when setup-target=true")
	}
	if !skipIfInfraOnly(off) {
		t.Error("skipIfInfraOnly(off) = false; should skip target steps when setup-target=false")
	}
	// Absent setup-target (legacy callers, -y without the flag) must
	// default to "skip" so an unset flag doesn't secretly kick off
	// instance provisioning. Default="true" on the field means the
	// wizard/CLI fills it in; saga-time saw raw Input should treat
	// empty as "no target" conservatively.
	if !skipIfInfraOnly(absent) {
		t.Error("skipIfInfraOnly(absent) = false; empty value should be treated as infra-only")
	}
}
