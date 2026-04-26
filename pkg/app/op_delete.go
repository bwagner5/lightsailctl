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
			{Label: "Untag target instances", Do: func(ctx context.Context, st *registry.State) error {
				c, err := s.ensure(ctx)
				if err != nil {
					return err
				}
				refs, err := c.UntagInstancesForApp(ctx, st.Input.Get("name"))
				st.Data["targets"] = refs
				return err
			}},
			{Label: "Reset firewalls on unused instances", Do: func(ctx context.Context, st *registry.State) error {
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
					// best-effort; a still-tagged instance is skipped inside ResetFirewallIfUnused
					_ = c.ResetFirewallIfUnused(ctx, r.Instance, r.Region)
				}
				return nil
			}},
			{Label: "Delete buckets", Do: func(ctx context.Context, st *registry.State) error {
				c, err := s.ensure(ctx)
				if err != nil {
					return err
				}
				return deleteAppBuckets(ctx, c, st.Input.Get("name"))
			}},
			{Label: "Remove local lightsail.conf", Do: removeLocalConfStep, Skip: skipIfNoMatchingLocalConf},
		},
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

// deleteAppBuckets removes every bucket belonging to appName (env buckets +
// app-config bucket). Returns the first failure; successful deletions are
// kept (best-effort — partial cleanup is still progress).
func deleteAppBuckets(ctx context.Context, c *lightsail.Client, appName string) error {
	buckets, err := c.ListAppBuckets(ctx)
	if err != nil {
		return err
	}
	var firstErr error
	for _, b := range buckets {
		if a, _ := lightsail.ParseAppEnv(b.Name); a == appName {
			if err := c.DeleteBucket(ctx, b.Name, b.Region); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("delete %s: %w", b.Name, err)
			}
			continue
		}
		if lightsail.ParseAppFromAppBucket(b.Name) == appName {
			if err := c.DeleteBucket(ctx, b.Name, b.Region); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("delete %s: %w", b.Name, err)
			}
		}
	}
	return firstErr
}
