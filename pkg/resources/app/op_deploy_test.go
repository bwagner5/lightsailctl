// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/build"
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

// TestAbortIfDeclinedStep_Variants verifies the new graceful-abort
// contract: the step NEVER returns an error. Instead it flags
// st.Data["aborted"]=true when the user declined, so downstream
// steps can Skip without the runtime painting a failure.
func TestAbortIfDeclinedStep_Variants(t *testing.T) {
	cases := []struct {
		v           string
		wantAborted bool
	}{
		{"", false},
		{"true", false},
		{"yes", false},
		{"false", true},
		{"no", true},
	}
	for _, tc := range cases {
		st := &registry.State{
			Input: registry.Input{"deploy-confirm": tc.v},
			Data:  map[string]any{},
		}
		err := abortIfDeclinedStep(context.Background(), st)
		if err != nil {
			t.Errorf("deploy-confirm=%q: unexpected error: %v", tc.v, err)
		}
		got, _ := st.Data["aborted"].(bool)
		if got != tc.wantAborted {
			t.Errorf("deploy-confirm=%q: aborted=%v; want %v", tc.v, got, tc.wantAborted)
		}
	}
}

// TestSkipIfAborted_Variants sanity-checks the shared Skip helper.
func TestSkipIfAborted_Variants(t *testing.T) {
	cases := []struct {
		name string
		data map[string]any
		want bool
	}{
		{"nil data", nil, false},
		{"no flag", map[string]any{}, false},
		{"false flag", map[string]any{"aborted": false}, false},
		{"true flag", map[string]any{"aborted": true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &registry.State{Data: tc.data}
			if st.Data == nil {
				st.Data = map[string]any{}
			}
			if got := skipIfAborted(st); got != tc.want {
				t.Errorf("skipIfAborted = %v; want %v", got, tc.want)
			}
		})
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

func TestDeployTypedFieldKinds(t *testing.T) {
	op := deployOp(&store{})
	cases := map[string]registry.FieldKind{
		"create-new-instance": registry.KindBool,
		"__ni/monitoring":     registry.KindBool,
		"deploy-confirm":      registry.KindBool,
		"wait-timeout":        registry.KindDuration,
		"no-wait":             registry.KindBool,
	}
	for flag, want := range cases {
		t.Run(flag, func(t *testing.T) {
			f := fieldByFlag(t, op.Fields, flag)
			if f.Kind != want {
				t.Fatalf("%s kind = %v; want %v", flag, f.Kind, want)
			}
		})
	}
}

// TestDeploySummaryPreamble_CreateNew confirms the summary picks up
// __ni/* fields when strategy is create-new.
func TestDeploySummaryPreamble_CreateNew(t *testing.T) {
	in := registry.Input{
		"name":                "hello",
		"env":                 "dev",
		"create-new-instance": "true",
		"__ni/name":           "ancient-orbit",
		"__ni/region":         "us-east-1",
		"__ni/blueprint":      "amazon_linux_2023",
		"__ni/bundle":         "large_3_0",
	}
	got := deploySummaryPreamble(in)
	for _, want := range []string{"hello", "dev", "ancient-orbit", "us-east-1", "amazon_linux_2023", "large_3_0", "Lightsail Instance (new)"} {
		if !strings.Contains(got, want) {
			t.Errorf("preamble missing %q:\n%s", want, got)
		}
	}
}

func fieldByFlag(t *testing.T, fields []registry.Field, flag string) registry.Field {
	t.Helper()
	for _, f := range fields {
		if f.Flag == flag {
			return f
		}
	}
	t.Fatalf("field %q not found", flag)
	return registry.Field{}
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

// TestDeployOp_HasIamConfirmFastPath pins the fix for the mid-saga
// pause: the deploy op declares a hidden `iam-confirm` field with
// Default="true". That makes confirmIAMCreateStep's early-return
// fast-path fire (it checks for a non-empty Input value), so the saga
// never raises a NeedInput for IAM review during a deploy. Standalone
// `app enable-gh-action` doesn't set this field, so its preview prompt
// still runs.
func TestDeployOp_HasIamConfirmFastPath(t *testing.T) {
	op := deployOp(&store{})
	f := fieldByFlag(t, op.Fields, "iam-confirm")
	if f.Kind != registry.KindBool {
		t.Errorf("iam-confirm kind = %v; want %v", f.Kind, registry.KindBool)
	}
	if f.Default != "true" {
		t.Errorf("iam-confirm default = %v; want \"true\"", f.Default)
	}
	// Must not be prompted — the deploy-confirm preamble is the
	// user-facing review for the IAM setup.
	if f.Wizard == nil || *f.Wizard {
		t.Errorf("iam-confirm must set Wizard=false; got %v", f.Wizard)
	}
}

// TestDeploySummaryPreamble_ShowsGhActionsSection verifies the
// preamble now describes the IAM setup upfront so the user approves
// everything at the deploy-confirm gate.
func TestDeploySummaryPreamble_ShowsGhActionsSection(t *testing.T) {
	in := registry.Input{
		"name":            "hello",
		"env":             "dev",
		"instance":        "box-1",
		"offer-gh-action": "true",
		"__gh-owner":      "alice",
		"__gh-repo":       "hello",
		"repo":            "alice/hello",
	}
	got := deploySummaryPreamble(in)
	for _, want := range []string{
		"GitHub Actions setup",
		"alice/hello",
		"lightsailctl-deploy-alice-hello-dev", // default role name
		"deploy to hello/dev only",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("preamble missing %q:\n%s", want, got)
		}
	}
}

// TestDeploySummaryPreamble_OmitsGhActionsWhenOptedOut confirms we
// don't advertise a setup the user declined.
func TestDeploySummaryPreamble_OmitsGhActionsWhenOptedOut(t *testing.T) {
	in := registry.Input{
		"name":            "hello",
		"env":             "dev",
		"instance":        "box-1",
		"offer-gh-action": "false",
		"__gh-owner":      "alice",
		"__gh-repo":       "hello",
	}
	got := deploySummaryPreamble(in)
	if strings.Contains(got, "GitHub Actions setup") {
		t.Errorf("preamble leaks CI section when offer-gh-action=false:\n%s", got)
	}
}

// TestPackageStep_DetectsStrategies verifies that packageStep stashes
// the right strategy enum + reason for each kind of source tree, and
// fails fast on unknown trees BEFORE running the tarball.
func TestPackageStep_DetectsStrategies(t *testing.T) {
	cases := []struct {
		name    string
		files   []string
		want    build.Strategy
		wantStr string
		wantErr bool
		errSnip string
	}{
		{"compose", []string{"docker-compose.yml"}, build.StrategyCompose, "compose", false, ""},
		{"dockerfile", []string{"Dockerfile"}, build.StrategyDockerfile, "dockerfile", false, ""},
		{"go", []string{"go.mod"}, build.StrategyBuildpack, "buildpack", false, ""},
		{"node", []string{"package.json"}, build.StrategyBuildpack, "buildpack", false, ""},
		{"unknown", []string{"README.md"}, build.StrategyUnknown, "", true, "can't deploy this directory"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			t.Chdir(dir)
			st := &registry.State{Input: registry.Input{}, Data: map[string]any{}}
			err := packageStep(context.Background(), st)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil; data=%v", st.Data)
				}
				if !strings.Contains(err.Error(), tc.errSnip) {
					t.Errorf("err = %q; want it to contain %q", err.Error(), tc.errSnip)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got := st.Data["strategy_enum"]; got != tc.want {
				t.Errorf("strategy_enum = %v; want %v", got, tc.want)
			}
			if got := st.Data["strategy"]; got != tc.wantStr {
				t.Errorf("strategy = %v; want %q", got, tc.wantStr)
			}
			// Cleanup tarball that the success path created.
			if path, ok := st.Data["tarball"].(string); ok {
				_ = os.Remove(path)
			}
		})
	}
}

// TestPortsForStrategy_Buildpack confirms the firewall step opens 8080
// for non-compose strategies even with no compose file present.
func TestPortsForStrategy_Buildpack(t *testing.T) {
	st := &registry.State{Data: map[string]any{"strategy_enum": build.StrategyBuildpack}}
	if got := portsForStrategy(st); len(got) != 1 || got[0] != 8080 {
		t.Errorf("buildpack ports = %v; want [8080]", got)
	}
	st2 := &registry.State{Data: map[string]any{"strategy_enum": build.StrategyDockerfile}}
	if got := portsForStrategy(st2); len(got) != 1 || got[0] != 8080 {
		t.Errorf("dockerfile ports = %v; want [8080]", got)
	}
}

// TestBuildStrategyRows_Variants confirms the deploy preamble's
// "Build strategy" section names what we're going to do.
func TestBuildStrategyRows_Variants(t *testing.T) {
	cases := []struct {
		name      string
		files     []string
		wantSnips []string
	}{
		{"compose", []string{"docker-compose.yml"}, []string{"compose"}},
		{"dockerfile", []string{"Dockerfile"}, []string{"Dockerfile", "docker build"}},
		{"buildpack-go", []string{"go.mod"}, []string{"Cloud Native Buildpacks", "Go", "paketo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			t.Chdir(dir)
			rows := buildStrategyRows()
			if len(rows) == 0 {
				t.Fatalf("got no rows")
			}
			joined := ""
			for _, r := range rows {
				joined += r[0] + " " + r[1] + "\n"
			}
			for _, want := range tc.wantSnips {
				if !strings.Contains(joined, want) {
					t.Errorf("rows missing %q:\n%s", want, joined)
				}
			}
		})
	}
}

// TestBuildStrategyRows_UnknownIsSilent: an unrecognized tree shows no
// "Build strategy" rows in the preamble (the saga step will fail with
// a clear error; the preamble shouldn't lie about a missing strategy).
func TestBuildStrategyRows_UnknownIsSilent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if rows := buildStrategyRows(); len(rows) != 0 {
		t.Errorf("got %d rows; want 0 for unknown tree: %v", len(rows), rows)
	}
}

// TestPredetectGhOffer_StashesRepoDetails guards the Pre-phase
// contract: when a first-time deploy detects a GitHub remote,
// predetectGhOffer populates the fields the deploy-confirm preamble
// reads AND the fields detectRemoteStep uses to fast-path, so the
// saga never re-parses the remote and the preamble can render
// without any network calls.
func TestPredetectGhOffer_StashesRepoDetails(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; skipping")
	}
	dir := t.TempDir()
	t.Chdir(dir)
	// Set up a real git repo with a github.com remote so
	// ghaction.DetectRemoteURL (which shells out to `git`) finds it.
	for _, args := range [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", "https://github.com/alice/hello.git"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	in := registry.Input{}
	predetectGhOffer(in)

	if in.Get("__first-deploy-with-gh") != "true" {
		t.Errorf("__first-deploy-with-gh = %q; want true", in.Get("__first-deploy-with-gh"))
	}
	if in.Get("__gh-owner") != "alice" {
		t.Errorf("__gh-owner = %q; want alice", in.Get("__gh-owner"))
	}
	if in.Get("__gh-repo") != "hello" {
		t.Errorf("__gh-repo = %q; want hello", in.Get("__gh-repo"))
	}
	if in.Get("repo") != "alice/hello" {
		t.Errorf("repo = %q; want alice/hello", in.Get("repo"))
	}
}
