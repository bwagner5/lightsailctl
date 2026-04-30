package app

import (
	"context"
	"strings"
	"testing"

	"github.com/bwagner5/triad/pkg/registry"
)

// TestApplyStrategyStep_CreateNew records strategy="create-new" when
// --create-new-instance=true and doesn't need an AWS client.
func TestApplyStrategyStep_CreateNew(t *testing.T) {
	s := &store{}
	st := &registry.State{
		Input: registry.Input{"create-new-instance": "true"},
		Data:  map[string]any{},
	}
	if err := applyStrategyStep(s)(context.Background(), st); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got, want := st.Data["strategy"], "create-new"; got != want {
		t.Errorf("strategy = %v; want %v", got, want)
	}
}

// TestApplyStrategyStep_UseExistingWithInstance records strategy=
// "use-existing" when --instance is already set.
func TestApplyStrategyStep_UseExistingWithInstance(t *testing.T) {
	s := &store{}
	st := &registry.State{
		Input: registry.Input{"instance": "my-box"},
		Data:  map[string]any{},
	}
	if err := applyStrategyStep(s)(context.Background(), st); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got, want := st.Data["strategy"], "use-existing"; got != want {
		t.Errorf("strategy = %v; want %v", got, want)
	}
}

// TestApplyStrategyStep_NonInteractiveFailsCleanly: -y without any
// instance hint produces a helpful error rather than a late-saga
// failure.
func TestApplyStrategyStep_NonInteractiveFailsCleanly(t *testing.T) {
	ni := true
	s := &store{nonInteractive: &ni}
	st := &registry.State{Input: registry.Input{}, Data: map[string]any{}}
	err := applyStrategyStep(s)(context.Background(), st)
	if err == nil {
		t.Fatalf("want error, got nil")
	}
	if !strings.Contains(err.Error(), "no deployment target") {
		t.Errorf("error %q should mention 'no deployment target'", err.Error())
	}
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

// TestAbortIfDeclinedStep_Variants checks that a missing answer or
// "true" proceeds, while "false" aborts with a clear error.
func TestAbortIfDeclinedStep_Variants(t *testing.T) {
	cases := []struct {
		v       string
		wantErr bool
	}{
		{"", false},
		{"true", false},
		{"yes", false},
		{"false", true},
		{"no", true},
	}
	for _, tc := range cases {
		st := &registry.State{Input: registry.Input{"deploy-confirm": tc.v}}
		err := abortIfDeclinedStep(context.Background(), st)
		if (err != nil) != tc.wantErr {
			t.Errorf("deploy-confirm=%q: gotErr=%v wantErr=%v (err=%v)",
				tc.v, err != nil, tc.wantErr, err)
		}
	}
}

// TestYesNoSuggest_EmitsBoolValues ensures the shared helper still
// produces the expected (false,true) value pair.
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
	if !strings.Contains(choices[0].Display, "No") || !strings.Contains(choices[0].Display, "skip it") {
		t.Errorf("no-display wrong: %q", choices[0].Display)
	}
	if !strings.Contains(choices[1].Display, "Yes") || !strings.Contains(choices[1].Display, "do it") {
		t.Errorf("yes-display wrong: %q", choices[1].Display)
	}
}

// TestDeploySummaryPreamble_CreateNew confirms the summary picks up
// __ni/* fields when strategy is create-new.
func TestDeploySummaryPreamble_CreateNew(t *testing.T) {
	in := registry.Input{
		"name":                  "hello",
		"env":                   "dev",
		"create-new-instance":   "true",
		"__ni/name":             "ancient-orbit",
		"__ni/region":           "us-east-1",
		"__ni/blueprint":        "amazon_linux_2023",
		"__ni/bundle":           "large_3_0",
	}
	got := deploySummaryPreamble(in)
	for _, want := range []string{"hello", "dev", "ancient-orbit", "us-east-1", "amazon_linux_2023", "large_3_0", "Lightsail Instance (new)"} {
		if !strings.Contains(got, want) {
			t.Errorf("preamble missing %q:\n%s", want, got)
		}
	}
}

// TestDeploySummaryPreamble_UseExisting confirms the summary shows the
// existing instance name when strategy is use-existing.
func TestDeploySummaryPreamble_UseExisting(t *testing.T) {
	in := registry.Input{
		"name":     "hello",
		"env":      "dev",
		"instance": "my-box",
	}
	got := deploySummaryPreamble(in)
	if !strings.Contains(got, "Lightsail Instance (existing)") {
		t.Errorf("preamble missing existing header:\n%s", got)
	}
	if !strings.Contains(got, "my-box") {
		t.Errorf("preamble missing instance name:\n%s", got)
	}
	// And must not show __ni/ blueprint stuff.
	if strings.Contains(got, "Blueprint") {
		t.Errorf("preamble leaks new-instance fields for use-existing:\n%s", got)
	}
}
