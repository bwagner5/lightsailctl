package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/compose"
	"github.com/aws/lightsailctl/pkg/config"
	"github.com/aws/lightsailctl/pkg/lightsail"
	"github.com/aws/lightsailctl/pkg/names"
)

// addTargetOp implements `lightsailctl app add-target`. It bootstraps an
// existing Lightsail instance as an additional deployment target for an
// app/env pair that was already created via `app create` or `deploy`.
func addTargetOp(s *store) registry.Operation {
	return registry.Operation{
		Name:  "add-target",
		Key:   "t",
		Short: "add an instance as a deployment target",
		// add-target validates the app exists (env bucket present)
		// and refuses to run otherwise, so the "t" hint is only
		// meaningful when there's at least one app to attach to.
		// The flag also drives TUI wizard pre-fill from the
		// currently-selected app row.
		NeedsExistingRow: true,
		Fields: []registry.Field{
			{Flag: "name", Short: "n", Label: "App name", Help: "app name",
				Required: true, Prefill: names.DefaultAppName, Validate: names.ValidateLabel},
			{Flag: "env", Short: "e", Label: "Environment", Help: "environment",
				Required: true, Default: "dev", Validate: names.ValidateLabel},
			{Flag: "instance", Short: "i", Label: "Lightsail instances",
				Help:     "target Lightsail instance(s) to add (comma-separated accepts multiple)",
				Required: true, Multi: true,
				Suggest: instanceSuggest(s)},
			{Flag: "agent-path", Label: "Agent binary",
				Help: "linux/amd64 lightsailctl binary to scp to the instance",
				File: true, When: needsAgentBinaryPrompt},
			{Flag: "region", Help: "AWS region (auto-filled from --instance)",
				Wizard: registry.BoolPtr(false)},
		},
		Pre: addTargetPre,
		Steps: []registry.Step{
			// ── Validating ───────────────────────────────────────
			{Category: "Validating", Label: "Validate app exists", Do: addTargetValidateStep(s)},
			{Category: "Validating", Label: "Check instance not already tagged", Do: addTargetCheckDuplicateStep(s)},

			// ── Preparing instance ───────────────────────────────
			// Region lookup + pin are plumbing that belongs with the
			// provisioning work they enable.
			{Category: "Preparing instance", Label: "Resolve region from instance", Do: resolveRegionStep(s)},
			{Category: "Preparing instance", Label: "Pin region", Do: pinRegionStep(s), Undo: unpinStoreStep(s)},
			{Category: "Preparing instance", Label: "Resolve agent binary", Do: resolveAgentStep},
			{Category: "Preparing instance", Label: "Tag target instance", Do: tagInstanceStep(s), Undo: untagInstanceUndo(s)},
			{Category: "Preparing instance", Label: "Grant instance bucket access", Do: grantAccessStep(s), Undo: revokeAccessUndo(s)},
			{Category: "Preparing instance", Label: "SCP agent binary to instance", Do: scpAgentStep(s)},
			{Category: "Preparing instance", Label: "Wait for cloud-init to finish", Do: waitCloudInitStep(s)},
			{Category: "Preparing instance", Label: "Install watcher on instance", Do: remoteInstallStep(s)},
			{Category: "Preparing instance", Label: "Start watcher", Do: remoteUpStep(s)},
			{Category: "Preparing instance", Label: "Open firewall ports", Do: addTargetFirewallStep(s)},

			// ── Finalizing ───────────────────────────────────────
			{Category: "Finalizing", Label: "Update lightsail.conf", Do: addTargetSaveConfStep},
			{Category: "Finalizing", Label: "Restore global view", Do: unpinStoreStep(s)},
		},
	}
}

func addTargetPre(ctx context.Context, in registry.Input) error {
	// Hydrate app / env / region / agent-path from lightsail.conf, but
	// DO NOT touch the instance field. add-target is specifically for
	// bringing a *new* instance into the rotation — pre-populating it
	// with the conf's existing single-instance value (the one the app
	// was bootstrapped on) would make the wizard's "already set" fast
	// path skip the multi-select picker entirely, and the saga would
	// march straight into the duplicate-check step with the instance
	// that's already tagged. Users would see the duplicate error with
	// no picker ever appearing, exactly the symptom reported in
	// https://github.com/aws/lightsailctl (TUI add-target: "Where is
	// the overlay with multi-select for the instances?").
	if cfg, _ := config.LoadFromCwd(); cfg != nil {
		if in.Get("name") == "" && cfg.App != "" {
			in["name"] = cfg.App
		}
		if in.Get("env") == "" && cfg.Env != "" {
			in["env"] = cfg.Env
		}
		if in.Get("region") == "" && cfg.Region != "" {
			in["region"] = cfg.Region
		}
		if in.Get("agent-path") == "" && cfg.AgentPath != "" {
			in["agent-path"] = cfg.AgentPath
		}
	}
	preresolveAgentBinary(ctx, in)
	return nil
}

// addTargetValidateStep confirms the env bucket exists (app must be created first).
// When multiple instances are requested the first one seeds the region
// lookup; all selected instances must live in the same region (a later
// per-instance step rejects mismatches).
func addTargetValidateStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		acct, err := c.AccountID(ctx)
		if err != nil {
			return err
		}
		st.Data["acct"] = acct
		bucket := lightsail.EnvBucketName(acct, st.Input.Get("name"), st.Input.Get("env"))
		instances := st.Input.Multi("instance")
		if len(instances) == 0 {
			return fmt.Errorf("at least one instance is required")
		}
		// Region is derived from the first instance; downstream
		// per-instance steps will fail if other instances live in a
		// different region, which is the correct outcome since an
		// app/env bucket is regional.
		inst, err := c.GetInstance(ctx, instances[0])
		if err != nil {
			return fmt.Errorf("instance %q not found: %w", instances[0], err)
		}
		if inst.Region != "" {
			st.Input["region"] = inst.Region
		}
		b := findBucket(ctx, c, inst.Region, bucket)
		if b == nil {
			return fmt.Errorf("app %s/%s does not exist (no env bucket %s); run 'lightsailctl app create' or 'deploy' first",
				st.Input.Get("name"), st.Input.Get("env"), bucket)
		}
		st.Data["bucket"] = bucket
		return nil
	}
}

// addTargetCheckDuplicateStep fails if any selected instance is already
// tagged for this app/env. All requested instances are checked so the
// user sees one clear error listing every duplicate in one pass.
func addTargetCheckDuplicateStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		targets, err := c.FindTargetsForAppEnv(ctx, st.Input.Get("name"), st.Input.Get("env"))
		if err != nil {
			return err
		}
		existing := map[string]bool{}
		for _, t := range targets {
			existing[t.Name] = true
		}
		var dup []string
		for _, inst := range st.Input.Multi("instance") {
			if existing[inst] {
				dup = append(dup, inst)
			}
		}
		if len(dup) == 1 {
			return fmt.Errorf("instance %q is already a target for %s/%s",
				dup[0], st.Input.Get("name"), st.Input.Get("env"))
		}
		if len(dup) > 1 {
			return fmt.Errorf("instances %v are already targets for %s/%s",
				dup, st.Input.Get("name"), st.Input.Get("env"))
		}
		return nil
	}
}

// addTargetFirewallStep opens compose ports on every new target instance.
// Compose parsing is done once up front so errors / "no ports" short-circuit
// before opening any SSH sessions.
func addTargetFirewallStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		composePath := compose.Find()
		if composePath == "" {
			return nil
		}
		ports, err := compose.ParsePorts(composePath)
		if err != nil || len(ports) == 0 {
			return nil
		}
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		for _, inst := range st.Input.Multi("instance") {
			_, _ = c.OpenFirewallPorts(ctx, inst, ports)
		}
		return nil
	}
}

// addTargetSaveConfStep appends every added instance to lightsail.conf's
// Instances list (deduplicated) and keeps the legacy Instance scalar
// populated for older tooling that still reads it.
func addTargetSaveConfStep(_ context.Context, st *registry.State) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	p := filepath.Join(cwd, config.Filename)
	cfg, _ := config.Load(p)
	if cfg == nil {
		cfg = &config.Config{}
	}
	cfg.App = st.Input.Get("name")
	cfg.Env = st.Input.Get("env")
	cfg.Region = st.Input.Get("region")
	// Append every new instance, deduplicating against whatever the
	// conf already had. Order preserved so `lightsail.conf` reads the
	// same as the user's selection.
	seen := map[string]bool{}
	for _, inst := range cfg.Instances {
		seen[inst] = true
	}
	for _, inst := range st.Input.Multi("instance") {
		if inst == "" || seen[inst] {
			continue
		}
		cfg.Instances = append(cfg.Instances, inst)
		seen[inst] = true
	}
	// Ensure legacy Instance field is also populated.
	if cfg.Instance == "" && len(cfg.Instances) > 0 {
		cfg.Instance = cfg.Instances[0]
	}
	return cfg.Save(p)
}
