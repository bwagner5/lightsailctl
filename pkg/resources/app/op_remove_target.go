// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/config"
	"github.com/aws/lightsailctl/pkg/lightsail"
	"github.com/aws/lightsailctl/pkg/names"
)

// removeTargetOp implements `lightsailctl app remove-target`. It detaches
// an instance from an app/env: stops the watcher, untags, revokes bucket
// access, resets firewall if unused, and updates lightsail.conf.
//
// The initial target set up by `app create` / `app deploy` is no different
// from one added later via `app add-target` — any of them can be removed.
// Removing the last target leaves the app's infrastructure (env bucket)
// in place; attach a new target with `app add-target` or tear the app
// down entirely with `app delete`.
//
// Note: we do NOT set an Enabled predicate today. The natural one ("hide
// when the selected app has no targets") would read App.Instances, but
// that column is populated by a Field.Async loader in the TUI list
// view — the async result lands in a side cache, not on the App struct
// passed to Enabled. Wiring Enabled into that cache is a triad-level
// change. For now the hint shows on every selected app and the saga's
// Validate step gives a clear error if the chosen instance isn't a
// target. NeedsExistingRow still hides the hint when the table is empty.
func removeTargetOp(s *store, suggest func(context.Context) ([]registry.Choice, error)) registry.Operation {
	return registry.Operation{
		Name:             "remove-target",
		Key:              "shift+t",
		Short:            "remove an instance from deployment targets",
		Confirm:          "Remove this instance from the deployment targets?",
		NeedsExistingRow: true,
		// Cluster next to add-target in the TUI status bar and help
		// overlay. Without SortKey the ops would sort alphabetically
		// as add-target (a-) and remove-target (r-), splitting them
		// across the hint row. "add-target-remove" sorts immediately
		// after "add-target" regardless of what other ops exist.
		SortKey: "add-target-remove",
		Fields: []registry.Field{
			{Flag: "name", Short: "n", Label: "App name", Help: "app name",
				Required: true, Suggest: suggest, Prefill: names.DefaultAppName},
			{Flag: "env", Short: "e", Label: "Environment", Help: "environment",
				Required: true, Default: "dev"},
			{Flag: "instance", Short: "i", Label: "Instance to remove",
				Help: "target instance to detach", Required: true,
				Suggest: instanceSuggest(s)},
			{Flag: "region", Help: "AWS region (auto-filled)",
				Wizard: registry.BoolPtr(false)},
		},
		Pre: removeTargetPre,
		Steps: []registry.Step{
			// ── Removing target ──────────────────────────────────
			{Category: "Removing target", Label: "Validate target exists", Do: removeTargetValidateStep(s)},
			{Category: "Removing target", Label: "Resolve region", Do: resolveRegionStep(s)},
			{Category: "Removing target", Label: "Pin region", Do: pinRegionStep(s), Undo: unpinStoreStep(s)},
			{Category: "Removing target", Label: "Stop remote services", Do: removeTargetDownStep(s)},
			{Category: "Removing target", Label: "Untag instance", Do: removeTargetUntagStep(s)},
			{Category: "Removing target", Label: "Revoke bucket access", Do: removeTargetRevokeStep(s)},
			{Category: "Removing target", Label: "Reset firewall if unused", Do: removeTargetResetFWStep(s)},

			// ── Finalizing ───────────────────────────────────────
			{Category: "Finalizing", Label: "Update lightsail.conf", Do: removeTargetSaveConfStep},
			{Category: "Finalizing", Label: "Restore global view", Do: unpinStoreStep(s)},
		},
	}
}

func removeTargetPre(_ context.Context, in registry.Input) error {
	return preloadFromConf(context.Background(), in)
}

// removeTargetValidateStep confirms the instance is actually tagged for this
// app/env. Removing the last target is allowed — the operation's Confirm
// prompt is the user's intent check, and the app's infrastructure stays
// in place so a new target can be attached via `app add-target`.
func removeTargetValidateStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		targets, err := c.FindTargetsForAppEnv(ctx, st.Input.Get("name"), st.Input.Get("env"))
		if err != nil {
			return err
		}
		instance := st.Input.Get("instance")
		found := false
		for _, t := range targets {
			if t.Name == instance {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("instance %q is not a target for %s/%s",
				instance, st.Input.Get("name"), st.Input.Get("env"))
		}
		return nil
	}
}

func removeTargetDownStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		ref := lightsail.TargetRef{
			Instance: st.Input.Get("instance"),
			Env:      st.Input.Get("env"),
			Region:   st.Input.Get("region"),
		}
		rc := c
		if ref.Region != "" {
			rc = c.WithRegion(ref.Region)
		}
		// Best-effort: if SSH fails (instance stopped, etc.) continue with untag.
		_ = runRemoteLocalDown(ctx, rc, st.Input.Get("name"), ref)
		return nil
	}
}

func removeTargetUntagStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		key := lightsail.TagPrefix + st.Input.Get("name") + ":" + st.Input.Get("env")
		instance := st.Input.Get("instance")
		return c.UntagInstance(ctx, instance, key)
	}
}

func removeTargetRevokeStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		acct, err := c.AccountID(ctx)
		if err != nil {
			return err
		}
		bucket := lightsail.EnvBucketName(acct, st.Input.Get("name"), st.Input.Get("env"))
		return c.SetBucketAccessForInstance(ctx, bucket, st.Input.Get("instance"), false)
	}
}

func removeTargetResetFWStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		region := st.Input.Get("region")
		_ = c.ResetFirewallIfUnused(ctx, st.Input.Get("instance"), region)
		return nil
	}
}

// removeTargetSaveConfStep removes the instance from lightsail.conf's Instances list.
func removeTargetSaveConfStep(_ context.Context, st *registry.State) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	p := filepath.Join(cwd, config.Filename)
	cfg, _ := config.Load(p)
	if cfg == nil {
		return nil // no conf to update
	}
	instance := st.Input.Get("instance")
	var kept []string
	for _, inst := range cfg.Instances {
		if inst != instance {
			kept = append(kept, inst)
		}
	}
	cfg.Instances = kept
	// If the removed instance was the legacy Instance field, update it.
	if cfg.Instance == instance {
		if len(kept) > 0 {
			cfg.Instance = kept[0]
		} else {
			cfg.Instance = ""
		}
	}
	return cfg.Save(p)
}
