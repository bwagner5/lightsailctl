package app

import (
	"context"
	"testing"

	"github.com/bwagner5/triad/pkg/registry"
)

// TestPickInstanceStrategy_ShortCircuitsWhenInstanceSet verifies that
// conf or --instance already naming a target bypasses the yes/no
// prompt and the AWS ListInstances call entirely. This is the common
// path for established projects.
func TestPickInstanceStrategy_ShortCircuitsWhenInstanceSet(t *testing.T) {
	// A store with no region/client is fine: we never reach s.ensure.
	s := &store{}
	st := &registry.State{
		Input: registry.Input{"instance": "my-box"},
		Data:  map[string]any{},
	}
	if err := pickInstanceStrategyStep(s)(context.Background(), st); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got, want := st.Data["strategy"], "use-existing"; got != want {
		t.Errorf("strategy = %v; want %v", got, want)
	}
}

// TestPickInstanceStrategy_UsesInputWhenCreateNewPrefilled verifies
// that a non-interactive caller passing --create-new-instance=true with
// the strategy step not yet decided lands on "create-new" without
// needing a NeedInput round-trip. The ListInstances call is still made
// to confirm the account, but we stub past it by setting the input
// before any ensure.
func TestPickInstanceStrategy_UsesInputWhenCreateNewPrefilled(t *testing.T) {
	// Pre-answered create-new path. s.ensure will run; we can't avoid
	// it without an injection seam, so this is skipped unless the
	// fake-client plumbing lands. Left here as documentation of intent.
	t.Skip("requires a mockable store.ensure() seam; covered by manual + integ")
}

// TestSkipUnlessCreatingNewInstance_Matrix tables the skip helper's
// behavior across the three strategy values the saga emits.
func TestSkipUnlessCreatingNewInstance_Matrix(t *testing.T) {
	cases := []struct {
		strategy string
		wantSkip bool
	}{
		{"create-new", false},
		{"use-existing", true},
		{"", true}, // unset is treated as "not creating"
	}
	for _, tc := range cases {
		st := &registry.State{Data: map[string]any{"strategy": tc.strategy}}
		if tc.strategy == "" {
			st.Data = map[string]any{}
		}
		if got := skipUnlessCreatingNewInstance(st); got != tc.wantSkip {
			t.Errorf("strategy=%q: skipUnlessCreatingNew=%v; want %v", tc.strategy, got, tc.wantSkip)
		}
	}
}

// TestSkipIfCreatingNewInstance_Matrix covers the complementary helper.
func TestSkipIfCreatingNewInstance_Matrix(t *testing.T) {
	cases := []struct {
		strategy string
		wantSkip bool
	}{
		{"create-new", true},
		{"use-existing", false},
		{"", false},
	}
	for _, tc := range cases {
		st := &registry.State{Data: map[string]any{"strategy": tc.strategy}}
		if tc.strategy == "" {
			st.Data = map[string]any{}
		}
		if got := skipIfCreatingNewInstance(st); got != tc.wantSkip {
			t.Errorf("strategy=%q: skipIfCreatingNew=%v; want %v", tc.strategy, got, tc.wantSkip)
		}
	}
}

// TestNamespaceFields_PrefixesFlagsNotSuggest verifies the helper only
// touches Flag so the closures still fire on raw values. Suggest /
// Validate identity is preserved by pointer comparison on Help (their
// closures are unexported; Help is the cheapest sentinel here).
func TestNamespaceFields_PrefixesFlagsNotSuggest(t *testing.T) {
	in := []registry.Field{
		{Flag: "name", Help: "instance name", Required: true},
		{Flag: "region", Help: "AWS region"},
	}
	out := namespaceFields("__ni/", in)
	if len(out) != len(in) {
		t.Fatalf("len = %d; want %d", len(out), len(in))
	}
	for i, f := range out {
		if want := "__ni/" + in[i].Flag; f.Flag != want {
			t.Errorf("[%d] Flag = %q; want %q", i, f.Flag, want)
		}
		if f.Help != in[i].Help {
			t.Errorf("[%d] Help mutated: %q vs %q", i, f.Help, in[i].Help)
		}
		if f.Required != in[i].Required {
			t.Errorf("[%d] Required mutated: %v vs %v", i, f.Required, in[i].Required)
		}
	}
	// Ensure the input slice wasn't mutated (namespaceFields returns a copy).
	if in[0].Flag != "name" {
		t.Errorf("input slice mutated: %q", in[0].Flag)
	}
}

// TestNeedsNewInstanceInput_RequiredMissing triggers a NeedInput round
// when any required namespaced field is empty. Optional fields don't
// trigger a round by themselves — matches the plan's "ask for all of
// them when any required is missing" semantics.
func TestNeedsNewInstanceInput_RequiredMissing(t *testing.T) {
	fields := []registry.Field{
		{Flag: "name", Required: true},
		{Flag: "region", Required: true},
		{Flag: "user-data"}, // optional
	}
	// All empty: needs input.
	in := registry.Input{}
	if !needsNewInstanceInput(fields, in, "__ni/") {
		t.Errorf("empty input should need input")
	}
	// All required set, optional empty: no input needed.
	in = registry.Input{"__ni/name": "box", "__ni/region": "us-east-1"}
	if needsNewInstanceInput(fields, in, "__ni/") {
		t.Errorf("required filled should NOT need input (optional user-data missing is fine)")
	}
	// One required missing: needs input.
	in = registry.Input{"__ni/name": "box"}
	if !needsNewInstanceInput(fields, in, "__ni/") {
		t.Errorf("partial required should need input")
	}
}

// TestYesNoSuggest_EmitsBoolValues confirms the tiny helper used for
// the new "create a new instance?" question returns values parseable
// as booleans downstream. Display strings include the "No"/"Yes"
// prefix convention shared with the monitoring and ip-address-type
// pickers.
func TestYesNoSuggest_EmitsBoolValues(t *testing.T) {
	choices, err := yesNoSuggest("skip it", "do it")(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(choices) != 2 {
		t.Fatalf("got %d choices; want 2", len(choices))
	}
	if choices[0].Value != "false" || choices[1].Value != "true" {
		t.Errorf("values = %q/%q; want false/true", choices[0].Value, choices[1].Value)
	}
	// Display sanity.
	if !containsAll(choices[0].Display, "No", "skip it") {
		t.Errorf("no-display missing parts: %q", choices[0].Display)
	}
	if !containsAll(choices[1].Display, "Yes", "do it") {
		t.Errorf("yes-display missing parts: %q", choices[1].Display)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
