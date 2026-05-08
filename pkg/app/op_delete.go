package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/config"
	"github.com/aws/lightsailctl/pkg/ghaction"
	"github.com/aws/lightsailctl/pkg/iamoidc"
	"github.com/aws/lightsailctl/pkg/lightsail"
)

// deleteOp: tear down an app. Phases:
//  1. Run remote `app local down` on each target instance/env
//  2. Untag all instances targeting the app
//  3. Reset firewall on any newly-unused instance
//  4. Delete all env buckets + the app-config bucket
//  5. Delete any CI IAM role(s) `enable-gh-action` provisioned for this app
//  6. Remove the local GitHub Actions workflow file
//  7. Remove the local lightsail.conf
func deleteOp(s *store, suggest func(context.Context) ([]registry.Choice, error)) registry.Operation {
	return registry.Operation{
		Name: "delete", Aliases: []string{"rm"}, Key: "ctrl+d",
		Short:   "delete an app and all its buckets",
		Confirm: "Delete this app? All environments, buckets, CI roles, and status files will be removed.",
		Fields: []registry.Field{
			{Flag: "name", Short: "n", Help: "app name", Required: true, Suggest: suggest},
		},
		Steps: []registry.Step{
			// ── Stopping app ─────────────────────────────────────
			{Category: "Stopping app", Label: "Discover target instances", Do: discoverTargetsStep(s)},
			{Category: "Stopping app", Label: "Stop remote app services", Do: remoteLocalDownStep(s), Skip: skipIfNoTargets},
			{Category: "Stopping app", Label: "Untag target instances", Do: untagTargetsStep(s), Skip: skipIfNoTargets},
			{Category: "Stopping app", Label: "Reset firewalls on unused instances", Do: resetFirewallsStep(s), Skip: skipIfNoTargets},

			// ── Deleting infrastructure ──────────────────────────
			{Category: "Deleting infrastructure", Label: "List app buckets", Do: listBucketsForDeleteStep(s)},
			{Category: "Deleting infrastructure", Label: "Delete env buckets", Do: deleteEnvBucketsStep(s), Skip: skipIfNoEnvBuckets},
			{Category: "Deleting infrastructure", Label: "Discover CI IAM roles", Do: discoverCIRolesStep(s)},
			{Category: "Deleting infrastructure", Label: "Delete CI IAM roles", Do: deleteCIRolesStep(s), Skip: skipIfNoCIRoles},

			// ── Cleaning up locally ──────────────────────────────
			{Category: "Cleaning up locally", Label: "Remove local GitHub Actions workflow", Do: removeLocalWorkflowStep, Skip: skipIfNoLocalWorkflow},
			{Category: "Cleaning up locally", Label: "Remove local lightsail.conf", Do: removeLocalConfStep, Skip: skipIfNoMatchingLocalConf},
		},
	}
}

// discoverTargetsStep lists instances tagged for this app so later steps
// can reference them (and show accurate counts in their progress output
// via the runtime's step-level output buffer).
func discoverTargetsStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		// Best-effort: a total failure here (e.g. one region errors)
		// shouldn't block delete. Missing-instance / not-found cases
		// are already absorbed by FindTargetsForApp returning an empty
		// list from that region.
		refs, err := c.FindTargetsForApp(ctx, st.Input.Get("name"))
		if err != nil && !lightsail.IsNotFound(err) {
			return err
		}
		st.Data["targets"] = refs
		return nil
	}
}

func skipIfNoTargets(st *registry.State) bool {
	refs, _ := st.Data["targets"].([]lightsail.TargetRef)
	return len(refs) == 0
}

func remoteLocalDownStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		refs, _ := st.Data["targets"].([]lightsail.TargetRef)
		if len(refs) == 0 {
			return nil
		}

		var stopped []string
		var skipped []string
		for _, r := range refs {
			rc := c
			if r.Region != "" {
				rc = c.WithRegion(r.Region)
			}
			label := r.Instance + "/" + r.Env
			if err := runRemoteLocalDown(ctx, rc, st.Input.Get("name"), r); err != nil {
				skipped = append(skipped, label+": "+err.Error())
				continue
			}
			stopped = append(stopped, label)
		}
		st.Output = remoteLocalDownSummary(stopped, skipped)
		return nil
	}
}

func runRemoteLocalDown(ctx context.Context, c *lightsail.Client, appName string, ref lightsail.TargetRef) error {
	creds, err := c.GetInstanceSSH(ctx, ref.Instance)
	if err != nil {
		return err
	}
	defer creds.Remove()
	cmd := remoteLocalDownCommand(appName, ref.Env)
	if out, err := creds.SSHRun(ctx, cmd); err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%s: %w", msg, err)
		}
		return err
	}
	return nil
}

func remoteLocalDownCommand(appName, env string) string {
	return fmt.Sprintf("sudo /usr/local/bin/lightsailctl app local down --app %s --env %s",
		shellQuote(appName), shellQuote(env))
}

func remoteLocalDownSummary(stopped, skipped []string) string {
	var b strings.Builder
	if len(stopped) > 0 {
		fmt.Fprintf(&b, "Stopped remote services on %d target(s):", len(stopped))
		for _, s := range stopped {
			b.WriteString("\n  " + s)
		}
	}
	if len(skipped) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "Skipped remote service cleanup on %d target(s):", len(skipped))
		for _, s := range skipped {
			b.WriteString("\n  " + s)
		}
	}
	return b.String()
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func untagTargetsStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		// Tolerate already-gone: if an instance vanished between
		// Discover and Untag, or never existed, that's success for
		// delete purposes.
		if _, err := c.UntagInstancesForApp(ctx, st.Input.Get("name")); err != nil && !lightsail.IsNotFound(err) {
			return err
		}
		return nil
	}
}

func resetFirewallsStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		refs, _ := st.Data["targets"].([]lightsail.TargetRef)
		seen := map[string]struct{}{}
		for _, r := range refs {
			if _, ok := seen[r.Instance]; ok {
				continue
			}
			seen[r.Instance] = struct{}{}
			_ = c.ResetFirewallIfUnused(ctx, r.Instance, r.Region)
		}
		return nil
	}
}

// listBucketsForDeleteStep finds the app's env buckets for deletion.
func listBucketsForDeleteStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		all, err := c.ListAppBuckets(ctx)
		if err != nil && !lightsail.IsNotFound(err) {
			return err
		}
		name := st.Input.Get("name")
		var envBuckets []lightsail.Bucket
		for i := range all {
			b := all[i]
			if a, _ := lightsail.ParseAppEnv(b.Name); a == name {
				envBuckets = append(envBuckets, b)
			}
		}
		st.Data["env_buckets"] = envBuckets
		return nil
	}
}

func skipIfNoEnvBuckets(st *registry.State) bool {
	bs, _ := st.Data["env_buckets"].([]lightsail.Bucket)
	return len(bs) == 0
}

func deleteEnvBucketsStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		bs, _ := st.Data["env_buckets"].([]lightsail.Bucket)
		var firstErr error
		for _, b := range bs {
			// Not-found = someone (maybe a previous partial delete)
			// already cleaned this up. That's fine.
			if err := c.DeleteBucket(ctx, b.Name, b.Region); err != nil && !lightsail.IsNotFound(err) && firstErr == nil {
				firstErr = fmt.Errorf("delete %s: %w", b.Name, err)
			}
		}
		return firstErr
	}
}

// removeLocalConfStep deletes ./lightsail.conf (or the nearest parent) when
// its App field matches the app being deleted. No-op when the conf isn't
// present, doesn't match, or can't be read.
func removeLocalConfStep(_ context.Context, st *registry.State) error {
	cwd, err := os.Getwd()
	if err != nil {
		return nil //nolint:nilerr // local-conf cleanup is best-effort
	}
	p := config.Find(cwd)
	if p == "" {
		return nil
	}
	cfg, err := config.Load(p)
	if err != nil || cfg.App != st.Input.Get("name") {
		return nil //nolint:nilerr
	}
	return os.Remove(p)
}

// skipIfNoMatchingLocalConf hides the "Remove local lightsail.conf" row
// when there's nothing local to remove — avoids an empty green row on a
// TUI delete where the user's cwd has nothing to do with the app.
func skipIfNoMatchingLocalConf(st *registry.State) bool {
	cwd, err := os.Getwd()
	if err != nil {
		return true
	}
	p := config.Find(cwd)
	if p == "" {
		return true
	}
	cfg, err := config.Load(p)
	if err != nil {
		return true
	}
	return cfg.App != st.Input.Get("name")
}

// ── CI teardown (IAM role + local workflow file) ─────────────────────

// discoverCIRolesStep lists the IAM role(s) tagged
// lightsailctl:app=<name>. Stashes the names on st.Data for
// deleteCIRolesStep. Best-effort: IAM listing failures are logged via
// st.Output but don't block the rest of the teardown.
func discoverCIRolesStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			// No AWS client yet: skip gracefully.
			st.Data["ci_roles"] = []string(nil)
			return nil
		}
		iamClient := iamoidc.NewIAMClient(c.Config())
		p := iamoidc.Provisioner{IAM: iamClient}
		roles, err := p.FindRolesForApp(ctx, st.Input.Get("name"))
		if err != nil {
			// Non-fatal: a user without iam:ListRoles still gets the
			// rest of delete. Record the hint for the summary.
			st.Data["ci_roles"] = []string(nil)
			st.Output = "Skipped IAM role discovery: " + err.Error()
			return nil
		}
		st.Data["ci_roles"] = roles
		if len(roles) > 0 {
			var b strings.Builder
			fmt.Fprintf(&b, "Found %d CI role(s) tagged for this app:", len(roles))
			for _, r := range roles {
				b.WriteString("\n  " + r)
			}
			st.Output = b.String()
		}
		return nil
	}
}

func skipIfNoCIRoles(st *registry.State) bool {
	roles, _ := st.Data["ci_roles"].([]string)
	return len(roles) == 0
}

// deleteCIRolesStep detaches the inline policy and deletes every IAM
// role discovered by discoverCIRolesStep. Idempotent: missing roles
// are treated as success. The shared OIDC provider is NEVER deleted
// — other apps in the same account may depend on it.
func deleteCIRolesStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		iamClient := iamoidc.NewIAMClient(c.Config())
		p := iamoidc.Provisioner{IAM: iamClient}
		roles, _ := st.Data["ci_roles"].([]string)
		var deleted []string
		var firstErr error
		for _, name := range roles {
			if err := p.DeleteRole(ctx, name); err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("delete %s: %w", name, err)
				}
				continue
			}
			deleted = append(deleted, name)
		}
		if len(deleted) > 0 {
			var b strings.Builder
			fmt.Fprintf(&b, "Deleted %d IAM role(s):", len(deleted))
			for _, r := range deleted {
				b.WriteString("\n  " + r)
			}
			st.Output = b.String()
		}
		return firstErr
	}
}

// skipIfNoLocalWorkflow skips the "Remove local GitHub Actions
// workflow" step when the file doesn't exist under the current
// directory (or any parent). The workflow lives at a well-known
// relative path, .github/workflows/lightsail-deploy.yml.
func skipIfNoLocalWorkflow(st *registry.State) bool {
	path := findLocalWorkflow()
	return path == ""
}

// removeLocalWorkflowStep deletes the workflow file if it exists.
// Best-effort: non-existence isn't an error, and a missing cwd is
// tolerated.
func removeLocalWorkflowStep(_ context.Context, st *registry.State) error {
	path := findLocalWorkflow()
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	st.Output = "Removed " + path
	return nil
}

// findLocalWorkflow walks up from cwd looking for a
// .github/workflows/lightsail-deploy.yml. Returns its absolute path
// or "". We don't validate that the file belongs to the app being
// deleted — running `app delete` already required the user to name
// the app explicitly, and the workflow file path itself is
// app-neutral. A conservative future improvement would parse the
// YAML and match its role-to-assume ARN against the deleted roles.
func findLocalWorkflow() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	dir := cwd
	for {
		candidate := filepath.Join(dir, ghaction.WorkflowRelPath)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
