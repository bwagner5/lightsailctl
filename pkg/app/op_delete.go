package app

import (
	"context"
	"fmt"
	"os"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/config"
	"github.com/aws/lightsailctl/pkg/lightsail"
)

// deleteOp: tear down an app. In Phase 1 this is:
//  1. Untag all instances targeting the app
//  2. Reset firewall on any newly-unused instance
//  3. Delete all env buckets + the app-config bucket
//
// Phase 5 will add a step-0 that runs `lightsailctl app local down` over SSH
// on each target to cleanly tear down containers.
func deleteOp(s *store, suggest func(context.Context) ([]registry.Choice, error)) registry.Operation {
	return registry.Operation{
		Name: "delete", Aliases: []string{"rm"}, Key: "ctrl+d",
		Short:   "delete an app and all its buckets",
		Confirm: "Delete this app? All environments, buckets, and status files will be removed.",
		Fields: []registry.Field{
			{Flag: "name", Short: "n", Help: "app name", Required: true, Suggest: suggest},
		},
		Steps: []registry.Step{
			{Label: "Discover target instances", Do: discoverTargetsStep(s)},
			{Label: "Untag target instances", Do: untagTargetsStep(s), Skip: skipIfNoTargets},
			{Label: "Reset firewalls on unused instances", Do: resetFirewallsStep(s), Skip: skipIfNoTargets},
			{Label: "List app buckets", Do: listBucketsForDeleteStep(s)},
			{Label: "Delete env buckets", Do: deleteEnvBucketsStep(s), Skip: skipIfNoEnvBuckets},
			{Label: "Delete app-config bucket", Do: deleteAppConfigBucketStep(s), Skip: skipIfNoAppConfigBucket},
			{Label: "Remove local lightsail.conf", Do: removeLocalConfStep, Skip: skipIfNoMatchingLocalConf},
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
		refs, err := c.FindTargetsForApp(ctx, st.Input.Get("name"))
		if err != nil {
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

func untagTargetsStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		_, err = c.UntagInstancesForApp(ctx, st.Input.Get("name"))
		return err
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

// listBucketsForDeleteStep splits the app's buckets into env vs app-config
// so the two categories can show as separate steps. Env buckets (plural)
// are grouped; the app-config bucket is singular.
func listBucketsForDeleteStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		all, err := c.ListAppBuckets(ctx)
		if err != nil {
			return err
		}
		name := st.Input.Get("name")
		var envBuckets []lightsail.Bucket
		var appConfig *lightsail.Bucket
		for i := range all {
			b := all[i]
			if a, _ := lightsail.ParseAppEnv(b.Name); a == name {
				envBuckets = append(envBuckets, b)
				continue
			}
			if lightsail.ParseAppFromAppBucket(b.Name) == name {
				appConfig = &b
			}
		}
		st.Data["env_buckets"] = envBuckets
		st.Data["app_config_bucket"] = appConfig
		return nil
	}
}

func skipIfNoEnvBuckets(st *registry.State) bool {
	bs, _ := st.Data["env_buckets"].([]lightsail.Bucket)
	return len(bs) == 0
}

func skipIfNoAppConfigBucket(st *registry.State) bool {
	b, _ := st.Data["app_config_bucket"].(*lightsail.Bucket)
	return b == nil
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
			if err := c.DeleteBucket(ctx, b.Name, b.Region); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("delete %s: %w", b.Name, err)
			}
		}
		return firstErr
	}
}

func deleteAppConfigBucketStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		b, _ := st.Data["app_config_bucket"].(*lightsail.Bucket)
		if b == nil {
			return nil
		}
		return c.DeleteBucket(ctx, b.Name, b.Region)
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
