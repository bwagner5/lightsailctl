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
		Short: "add an instance as a deployment target",
		Fields: []registry.Field{
			{Flag: "name", Short: "n", Label: "App name", Help: "app name",
				Required: true, Prefill: names.DefaultAppName, Validate: names.ValidateLabel},
			{Flag: "env", Short: "e", Label: "Environment", Help: "environment",
				Required: true, Default: "dev", Validate: names.ValidateLabel},
			{Flag: "instance", Short: "i", Label: "Lightsail instance",
				Help: "target Lightsail instance to add", Required: true,
				Suggest: instanceSuggest(s)},
			{Flag: "agent-path", Label: "Agent binary",
				Help: "linux/amd64 lightsailctl binary to scp to the instance",
				File: true, When: needsAgentBinaryPrompt},
			{Flag: "region", Help: "AWS region (auto-filled from --instance)",
				Wizard: registry.BoolPtr(false)},
		},
		Pre: addTargetPre,
		Steps: []registry.Step{
			{Label: "Validate app exists", Do: addTargetValidateStep(s)},
			{Label: "Check instance not already tagged", Do: addTargetCheckDuplicateStep(s)},
			{Label: "Resolve region from instance", Do: resolveRegionStep(s)},
			{Label: "Pin region", Do: pinRegionStep(s), Undo: unpinStoreStep(s)},
			{Label: "Resolve agent binary", Do: resolveAgentStep},
			{Label: "Tag target instance", Do: tagInstanceStep(s), Undo: untagInstanceUndo(s)},
			{Label: "Grant instance bucket access", Do: grantAccessStep(s), Undo: revokeAccessUndo(s)},
			{Label: "SCP agent binary to instance", Do: scpAgentStep(s)},
			{Label: "Install watcher on instance", Do: remoteInstallStep(s)},
			{Label: "Start watcher", Do: remoteUpStep(s)},
			{Label: "Open firewall ports", Do: addTargetFirewallStep(s)},
			{Label: "Update lightsail.conf", Do: addTargetSaveConfStep},
			{Label: "Restore global view", Do: unpinStoreStep(s)},
		},
	}
}

func addTargetPre(ctx context.Context, in registry.Input) error {
	if err := preloadFromConf(ctx, in); err != nil {
		return err
	}
	preresolveAgentBinary(ctx, in)
	return nil
}

// addTargetValidateStep confirms the env bucket exists (app must be created first).
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
		// We need to know the region to check the bucket. Resolve it
		// from the instance first.
		inst, err := c.GetInstance(ctx, st.Input.Get("instance"))
		if err != nil {
			return fmt.Errorf("instance %q not found: %w", st.Input.Get("instance"), err)
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

// addTargetCheckDuplicateStep fails if the instance is already tagged for this app/env.
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
		instance := st.Input.Get("instance")
		for _, t := range targets {
			if t.Name == instance {
				return fmt.Errorf("instance %q is already a target for %s/%s",
					instance, st.Input.Get("name"), st.Input.Get("env"))
			}
		}
		return nil
	}
}

// addTargetFirewallStep opens compose ports on the new target instance.
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
		_, _ = c.OpenFirewallPorts(ctx, st.Input.Get("instance"), ports)
		return nil
	}
}

// addTargetSaveConfStep appends the new instance to lightsail.conf's Instances list.
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
	// Append the new instance to the list, deduplicating.
	instance := st.Input.Get("instance")
	found := false
	for _, inst := range cfg.Instances {
		if inst == instance {
			found = true
			break
		}
	}
	if !found {
		cfg.Instances = append(cfg.Instances, instance)
	}
	// Ensure legacy Instance field is also populated.
	if cfg.Instance == "" {
		cfg.Instance = cfg.Instances[0]
	}
	return cfg.Save(p)
}
