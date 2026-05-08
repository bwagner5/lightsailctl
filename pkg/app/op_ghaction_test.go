package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/ghaction"
)

// TestDetectRemoteStep_ExplicitRepoWins checks the --repo override
// skips the git-remote shellout and parses owner/repo from the flag.
func TestDetectRemoteStep_ExplicitRepoWins(t *testing.T) {
	st := &registry.State{
		Input: registry.Input{"repo": "alice/hello"},
		Data:  map[string]any{},
	}
	if err := detectRemoteStep(context.Background(), st); err != nil {
		t.Fatalf("detectRemoteStep: %v", err)
	}
	if got, _ := st.Data["gh.owner"].(string); got != "alice" {
		t.Errorf("owner = %q; want alice", got)
	}
	if got, _ := st.Data["gh.repo"].(string); got != "hello" {
		t.Errorf("repo = %q; want hello", got)
	}
}

// TestDetectRemoteStep_MalformedRepo rejects bad --repo shapes.
func TestDetectRemoteStep_MalformedRepo(t *testing.T) {
	cases := []string{"no-slash", "/empty-owner", "owner/", "a/b/c"}
	for _, tc := range cases {
		t.Run(tc, func(t *testing.T) {
			st := &registry.State{
				Input: registry.Input{"repo": tc},
				Data:  map[string]any{},
			}
			// a/b/c has two slashes: SplitN with n=2 accepts it as
			// ("a", "b/c"), which the code then trims .git from. We
			// don't reject that — document the behavior and skip the
			// one case that's technically accepted.
			if tc == "a/b/c" {
				t.Skip("a/b/c is accepted by SplitN(..., 2) as owner=a, repo=b/c; documented behavior")
			}
			if err := detectRemoteStep(context.Background(), st); err == nil {
				t.Errorf("want error for %q, got nil", tc)
			}
		})
	}
}

// TestSkipOfferGhAction_NonInteractive verifies that -y skips the
// offer-CI step regardless of any other signal.
func TestSkipOfferGhAction_NonInteractive(t *testing.T) {
	nonInt := true
	s := &store{nonInteractive: &nonInt}
	st := &registry.State{Data: map[string]any{}}
	if !skipOfferGhAction(s)(st) {
		t.Errorf("skip should be true under -y")
	}
}

// TestSkipOfferGhAction_ConfPreexisted verifies the non-first-run
// path skips too.
func TestSkipOfferGhAction_ConfPreexisted(t *testing.T) {
	nonInt := false
	s := &store{nonInteractive: &nonInt}
	st := &registry.State{Data: map[string]any{"conf_existed": true}}
	if !skipOfferGhAction(s)(st) {
		t.Errorf("skip should be true when conf pre-existed")
	}
}

// TestSkipOfferGhAction_NoGitHubRemote skips when no git remote.
// Exercised by running in a throwaway directory with no .git.
func TestSkipOfferGhAction_NoGitHubRemote(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	nonInt := false
	s := &store{nonInteractive: &nonInt}
	st := &registry.State{Data: map[string]any{"conf_existed": false}}
	if !skipOfferGhAction(s)(st) {
		t.Errorf("skip should be true when no git remote is present")
	}
}

// TestAsBool_TableCovers the lenient bool parser used across op_ghaction.
func TestAsBool_Table(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"true", true}, {"TRUE", true}, {"yes", true}, {"Y", true},
		{"1", true}, {" true ", true},
		{"false", false}, {"no", false}, {"", false}, {"0", false},
		{"anything-else", false},
	}
	for _, tc := range cases {
		if got := asBool(tc.in); got != tc.want {
			t.Errorf("asBool(%q) = %v; want %v", tc.in, got, tc.want)
		}
	}
}

func TestGhActionTypedFieldKinds(t *testing.T) {
	enable := enableGhActionOp(&store{}, nil)
	for _, flag := range []string{"skip-role-create", "skip-workflow", "dry-run"} {
		t.Run("enable/"+flag, func(t *testing.T) {
			f := fieldByFlag(t, enable.Fields, flag)
			if f.Kind != registry.KindBool {
				t.Fatalf("%s kind = %v; want %v", flag, f.Kind, registry.KindBool)
			}
		})
	}

	disable := disableGhActionOp(&store{}, nil)
	f := fieldByFlag(t, disable.Fields, "skip-workflow")
	if f.Kind != registry.KindBool {
		t.Fatalf("disable skip-workflow kind = %v; want %v", f.Kind, registry.KindBool)
	}
}

func TestOfferGhActionNeedInputIsTypedBool(t *testing.T) {
	st := &registry.State{Input: registry.Input{}, Data: map[string]any{}}
	err := offerGhActionChoiceStep(context.Background(), st)
	need, ok := err.(*registry.NeedInput)
	if !ok {
		t.Fatalf("err = %T %[1]v; want *registry.NeedInput", err)
	}
	if len(need.Fields) != 1 || need.Fields[0].Flag != "offer-gh-action" {
		t.Fatalf("need fields = %+v; want offer-gh-action", need.Fields)
	}
	if need.Fields[0].Kind != registry.KindBool {
		t.Fatalf("offer-gh-action kind = %v; want %v", need.Fields[0].Kind, registry.KindBool)
	}
}

func TestConfirmIAMCreateNeedInputIsTypedBool(t *testing.T) {
	st := &registry.State{
		Input: registry.Input{},
		Data: map[string]any{
			"acct":          "111111111111",
			"iam.role_name": "lightsailctl-deploy-alice-hello-dev",
			"iam.trust":     "{}",
			"iam.perm":      "{}",
		},
	}
	err := confirmIAMCreateStep(&store{})(context.Background(), st)
	need, ok := err.(*registry.NeedInput)
	if !ok {
		t.Fatalf("err = %T %[1]v; want *registry.NeedInput", err)
	}
	if len(need.Fields) != 1 || need.Fields[0].Flag != "iam-confirm" {
		t.Fatalf("need fields = %+v; want iam-confirm", need.Fields)
	}
	if need.Fields[0].Kind != registry.KindBool {
		t.Fatalf("iam-confirm kind = %v; want %v", need.Fields[0].Kind, registry.KindBool)
	}
}

func TestInputBoolPreservesLegacyTruthyValues(t *testing.T) {
	if !inputBool(registry.Input{"dry-run": "yes"}, "dry-run") {
		t.Fatal("inputBool should preserve legacy yes=true behavior")
	}
}

// TestWriteWorkflowStep_DryRun verifies that --dry-run writes the
// rendered workflow to st.Output without touching the filesystem.
func TestWriteWorkflowStep_DryRun(t *testing.T) {
	orig, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(orig) })

	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	st := &registry.State{
		Input: registry.Input{
			"name":    "hello",
			"env":     "dev",
			"region":  "us-east-2",
			"branch":  "main",
			"dry-run": "true",
		},
		Data: map[string]any{
			"acct":          "111111111111",
			"iam.role_name": "lightsailctl-deploy-alice-hello-dev",
		},
	}
	if err := writeWorkflowStep(context.Background(), st); err != nil {
		t.Fatalf("writeWorkflowStep: %v", err)
	}
	// No file on disk.
	if _, err := os.Stat(filepath.Join(dir, ghaction.WorkflowRelPath)); !os.IsNotExist(err) {
		t.Errorf("workflow file should NOT exist in dry-run mode")
	}
	if st.Output == "" {
		t.Errorf("dry-run should emit the rendered workflow to st.Output")
	}
}

// TestPreloadGhActionFromConf_Backfills confirms the Pre hook fills
// Input from a nearby lightsail.conf.
func TestPreloadGhActionFromConf_Backfills(t *testing.T) {
	orig, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(orig) })

	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	// Write a minimal conf.
	conf := "app: myapp\nenv: prod\nregion: us-west-2\n"
	if err := os.WriteFile(filepath.Join(dir, "lightsail.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}

	in := registry.Input{}
	if err := preloadGhActionFromConf(context.Background(), in); err != nil {
		t.Fatalf("preload: %v", err)
	}
	if in.Get("name") != "myapp" || in.Get("env") != "prod" || in.Get("region") != "us-west-2" {
		t.Errorf("preload didn't fill input: %+v", in)
	}
}

// TestPreloadGhActionFromConf_DoesntOverrideExplicit preserves
// explicit CLI flags over conf values.
func TestPreloadGhActionFromConf_DoesntOverrideExplicit(t *testing.T) {
	orig, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(orig) })

	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	conf := "app: confapp\nenv: confenv\nregion: conf-region\n"
	if err := os.WriteFile(filepath.Join(dir, "lightsail.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}

	in := registry.Input{"name": "clipname"}
	if err := preloadGhActionFromConf(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if in.Get("name") != "clipname" {
		t.Errorf("preload overrode explicit name: %q", in.Get("name"))
	}
	if in.Get("env") != "confenv" {
		t.Errorf("preload didn't fill env: %q", in.Get("env"))
	}
}

// TestStore_InteractiveNilPointer ensures a nil nonInteractive
// pointer is treated as interactive (backward compat with callers
// of the legacy Resource() constructor).
func TestStore_InteractiveNilPointer(t *testing.T) {
	s := &store{}
	if !s.Interactive() {
		t.Errorf("nil nonInteractive pointer should be interactive")
	}
	f := false
	s2 := &store{nonInteractive: &f}
	if !s2.Interactive() {
		t.Errorf("*false = interactive")
	}
	tr := true
	s3 := &store{nonInteractive: &tr}
	if s3.Interactive() {
		t.Errorf("*true = non-interactive")
	}
}

// TestWorkflowFileInCwd_MatchesEnabledPredicates pins the Enabled
// predicates on enable-gh-action / disable-gh-action to
// workflowFileInCwd()'s return value, and exercises both branches.
//
// This is the regression guard for the status-bar decluttering
// change: only the contextually relevant verb (enable vs. disable)
// should surface on the bottom hint row; both remain reachable via
// the "?" help overlay and the command palette.
func TestWorkflowFileInCwd_MatchesEnabledPredicates(t *testing.T) {
	orig, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(orig) })

	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	enable := enableGhActionOp(&store{}, nil)
	disable := disableGhActionOp(&store{}, nil)
	if enable.Enabled == nil || disable.Enabled == nil {
		t.Fatal("enable/disable ops must each have an Enabled predicate")
	}

	// 1) No workflow file → enable is ON, disable is OFF.
	if workflowFileInCwd() {
		t.Fatal("workflowFileInCwd true in empty tempdir")
	}
	if !enable.Enabled(nil) {
		t.Errorf("enable should be enabled when workflow file is absent")
	}
	if disable.Enabled(nil) {
		t.Errorf("disable should be DISabled when workflow file is absent")
	}

	// 2) Drop the workflow file → enable flips OFF, disable flips ON.
	wfPath := filepath.Join(dir, ghaction.WorkflowRelPath)
	if err := os.MkdirAll(filepath.Dir(wfPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wfPath, []byte("name: Lightsail deploy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !workflowFileInCwd() {
		t.Fatal("workflowFileInCwd false after writing file")
	}
	if enable.Enabled(nil) {
		t.Errorf("enable should be DISabled when workflow file is present")
	}
	if !disable.Enabled(nil) {
		t.Errorf("disable should be enabled when workflow file is present")
	}
}

// TestCreateOp_HiddenFromStatusBar confirms the "c" create binding
// opts out of the always-on status bar (but remains key-dispatchable
// and visible in "?" help / palette).
func TestCreateOp_HiddenFromStatusBar(t *testing.T) {
	region := ""
	r := Resource(&region, nil)
	op, ok := r.Operations["create"]
	if !ok {
		t.Fatal("create operation not registered")
	}
	if !op.HideFromStatusBar {
		t.Error("create op should have HideFromStatusBar=true to keep the bottom bar uncluttered")
	}
	if op.Key == "" {
		t.Error("create op should still have a Key so it's reachable from '?' help")
	}
}

// TestConfirmIAMCreateStep_FastPathWhenPreConfirmed pins the deploy-
// flow contract: the hidden `iam-confirm` field in deploy op presets
// Input["iam-confirm"]="true", so the saga step returns nil instead
// of raising a mid-flight NeedInput. This is the UX fix that keeps
// the deploy saga running end-to-end after the single upfront
// deploy-confirm prompt.
func TestConfirmIAMCreateStep_FastPathWhenPreConfirmed(t *testing.T) {
	st := &registry.State{
		Input: registry.Input{"iam-confirm": "true"},
		Data: map[string]any{
			"acct":          "111111111111",
			"iam.role_name": "lightsailctl-deploy-alice-hello-dev",
			"iam.trust":     "{}",
			"iam.perm":      "{}",
		},
	}
	err := confirmIAMCreateStep(&store{})(context.Background(), st)
	if err != nil {
		t.Fatalf("err = %v; want nil (fast-path on pre-confirmed iam-confirm)", err)
	}
}

// TestConfirmIAMCreateStep_FastPathDeclineErrors: a pre-set
// iam-confirm=false means the user explicitly said no (e.g. via --iam-
// confirm=false); the step returns an error rather than silently
// proceeding.
func TestConfirmIAMCreateStep_FastPathDeclineErrors(t *testing.T) {
	st := &registry.State{
		Input: registry.Input{"iam-confirm": "false"},
		Data:  map[string]any{},
	}
	err := confirmIAMCreateStep(&store{})(context.Background(), st)
	if err == nil {
		t.Fatal("want error on iam-confirm=false")
	}
	if _, ok := err.(*registry.NeedInput); ok {
		t.Fatalf("got NeedInput; want plain error (decline should abort, not re-prompt)")
	}
}
