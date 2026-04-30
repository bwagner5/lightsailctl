package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/config"
)

// TestRemoveLocalConfStep_Matches asserts the conf is deleted when its
// App matches the deleted-app name.
func TestRemoveLocalConfStep_Matches(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	p := filepath.Join(dir, config.Filename)
	if err := (&config.Config{App: "my-app", Env: "dev"}).Save(p); err != nil {
		t.Fatal(err)
	}

	st := &registry.State{Input: registry.Input{"name": "my-app"}, Data: map[string]any{}}
	if err := removeLocalConfStep(context.Background(), st); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("expected conf removed, stat err=%v", err)
	}
}

// TestRemoveLocalConfStep_WrongApp asserts a conf for a DIFFERENT app
// is left alone when deleting an unrelated app.
func TestRemoveLocalConfStep_WrongApp(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	p := filepath.Join(dir, config.Filename)
	if err := (&config.Config{App: "other-app", Env: "dev"}).Save(p); err != nil {
		t.Fatal(err)
	}

	st := &registry.State{Input: registry.Input{"name": "my-app"}, Data: map[string]any{}}
	if err := removeLocalConfStep(context.Background(), st); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("conf for other-app should be untouched: %v", err)
	}
}

// TestSkipIfNoMatchingLocalConf_NoConf: outside a project dir the step
// must skip itself so no empty row shows in the TUI.
func TestSkipIfNoMatchingLocalConf_NoConf(t *testing.T) {
	t.Chdir(t.TempDir())
	st := &registry.State{Input: registry.Input{"name": "anything"}}
	if !skipIfNoMatchingLocalConf(st) {
		t.Errorf("skip should return true when no conf is present")
	}
}

// TestSkipIfNoCIRoles skips the delete step when discovery produced
// no matching roles (the common "CI was never enabled" path).
func TestSkipIfNoCIRoles(t *testing.T) {
	cases := []struct {
		name string
		data map[string]any
		want bool
	}{
		{"nil data", nil, true},
		{"missing key", map[string]any{}, true},
		{"empty slice", map[string]any{"ci_roles": []string{}}, true},
		{"one role", map[string]any{"ci_roles": []string{"lightsailctl-deploy-alice-hello-dev"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &registry.State{Data: tc.data}
			if st.Data == nil {
				st.Data = map[string]any{}
			}
			if got := skipIfNoCIRoles(st); got != tc.want {
				t.Errorf("skipIfNoCIRoles = %v; want %v", got, tc.want)
			}
		})
	}
}

// TestSkipIfNoLocalWorkflow_NoFile returns true in a bare temp dir.
func TestSkipIfNoLocalWorkflow_NoFile(t *testing.T) {
	t.Chdir(t.TempDir())
	st := &registry.State{Input: registry.Input{"name": "x"}}
	if !skipIfNoLocalWorkflow(st) {
		t.Errorf("skip should be true when no workflow file exists")
	}
}

// TestRemoveLocalWorkflowStep_Deletes verifies the step removes the
// workflow file from the cwd's .github/workflows dir.
func TestRemoveLocalWorkflowStep_Deletes(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	wf := filepath.Join(dir, ".github", "workflows", "lightsail-deploy.yml")
	if err := os.MkdirAll(filepath.Dir(wf), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wf, []byte("name: Lightsail deploy\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	st := &registry.State{Input: registry.Input{"name": "x"}, Data: map[string]any{}}
	if err := removeLocalWorkflowStep(context.Background(), st); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if _, err := os.Stat(wf); !os.IsNotExist(err) {
		t.Errorf("workflow file still present: %v", err)
	}
}
