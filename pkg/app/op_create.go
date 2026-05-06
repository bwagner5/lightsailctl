package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/config"
	"github.com/aws/lightsailctl/pkg/instance"
	"github.com/aws/lightsailctl/pkg/lightsail"
	"github.com/aws/lightsailctl/pkg/names"
)

// createOp implements `lightsailctl app create`. It is also invoked by the
// deploy saga when no env bucket exists for the configured app.
//
// Flow:
//  1. Create app-config bucket (ls--<acct>--<app>)  [idempotent]
//  2. Create env bucket        (ls--<acct>--<app>--<env>)
//  3. Tag target instance      ls:app:<app>:<env> = "true"
//  4. Grant instance bucket access (IMDS-based, no static keys)
//  5. Wait for propagation (30s; SetResourceAccessForBucket is eventually
//     consistent per hack/test-bucket-instance-access.sh)
//  6. SCP lightsailctl binary to the instance at /usr/local/bin
//  7. SSH `lightsailctl app local install ...`
//  8. SSH `lightsailctl app local up ...`
//  9. Save lightsail.conf in cwd and print the non-interactive command.
func createOp(s *store) registry.Operation {
	// Shared state for new-instance field closures (same pattern as deployOp).
	var (
		niStore    = instance.NewStore(s.region, s.regionHints)
		niBpType   = "os"
		niPlatform = "LINUX_UNIX"
	)
	niBpSuggest, niBpValidate := niBlueprintSuggestAndValidate(niStore, &niBpType, &niPlatform)

	wantsNew := func(in registry.Input) bool {
		v, _ := in.Bool("create-new-instance")
		return v
	}
	wantsExisting := func(in registry.Input) bool {
		v, _ := in.Bool("create-new-instance")
		return !v && in.Get("instance") == ""
	}
	askStrategy := func(in registry.Input) bool {
		return in.Get("instance") == ""
	}

	return registry.Operation{
		Name: "create", Key: "c", Short: "create a new Lightsail application",
		Confirm: "Create this Lightsail application?",
		Fields: []registry.Field{
			{Flag: "name", Short: "n", Label: "App name", Help: "app name", Prefill: names.DefaultAppName,
				Required: true, Validate: names.ValidateLabel},
			{Flag: "env", Short: "e", Label: "Environment", Help: "environment", Default: "dev",
				Required: true, Validate: names.ValidateLabel},

			// ── Instance target ──────────────────────────────────
			{Flag: "create-new-instance", Label: "Target",
				Help: "use an existing instance or create a new one",
				Kind: registry.KindBool, Required: true, When: askStrategy,
				Suggest: yesNoSuggest(
					"pick an existing instance",
					"create a new instance")},
			{Flag: "instance", Label: "Lightsail instance",
				Help:     "target Lightsail instance",
				Required: true, When: wantsExisting,
				Suggest: instanceSuggest(s)},

			// ── New instance fields ──────────────────────────────
			{Flag: "__ni/name", Label: "Instance name", Help: "instance name",
				Required: true, When: wantsNew,
				Prefill: names.Random, Validate: names.ValidateLabel},
			{Flag: "__ni/region", Label: "Region", Help: "AWS region",
				Required: true, When: wantsNew,
				Default: "us-east-1", Suggest: niRegionSuggest(niStore)},
			{Flag: "__ni/blueprint-type", Label: "Blueprint category",
				Help: "blueprint category", Default: "os", When: wantsNew,
				Suggest:  niBlueprintTypeSuggest(),
				Validate: func(v string) error { niBpType = v; return nil }},
			{Flag: "__ni/blueprint", Label: "Blueprint", Help: "OS / image",
				Required: true, When: wantsNew,
				Default: "amazon_linux_2023", Suggest: niBpSuggest, Validate: niBpValidate},
			{Flag: "__ni/bundle", Label: "Instance size", Help: "instance size",
				Required: true, When: wantsNew,
				Default: "micro_x_x", Suggest: niBundleSuggest(niStore, &niPlatform)},
			{Flag: "__ni/ip-address-type", Label: "Networking",
				Help: "networking stack", Default: "dualstack", When: wantsNew,
				Suggest: niIPTypeSuggest()},
			{Flag: "__ni/user-data", Label: "Launch script",
				Help: "launch script", When: wantsNew, File: true},
			{Flag: "__ni/monitoring", Label: "Detailed monitoring",
				Help: "detailed monitoring", Default: "false",
				Kind: registry.KindBool, When: wantsNew,
				Suggest: niMonitoringSuggest()},

			{Flag: "agent-path", Label: "Agent binary",
				Help: "lightsailctl binary to scp to the instance (linux/amd64)",
				File: true, When: needsAgentBinaryPrompt},
			{Flag: "region", Help: "AWS region (auto-filled from --instance)",
				Wizard: registry.BoolPtr(false)},
		},
		Pre: createPreResolveAgent,
		Steps: []registry.Step{
			{Label: "Resolve deployment target", Do: applyStrategyStep(s)},
			{Label: "Create new Lightsail instance",
				Do:   createNewInstanceInlineStep(s),
				Skip: skipUnlessCreatingNewInstance},
			{Label: "Resolve region from instance", Do: resolveRegionStep(s)},
			{Label: "Pin region", Do: pinRegionStep(s), Undo: unpinStoreStep(s)},
			{Label: "Resolve agent binary", Do: resolveAgentStep},
			{Label: "Create env bucket", Do: createEnvBucketStep(s)},
			{Label: "Tag target instance", Do: tagInstanceStep(s), Undo: untagInstanceUndo(s)},
			{Label: "Grant instance bucket access", Do: grantAccessStep(s), Undo: revokeAccessUndo(s)},
			{Label: "SCP agent binary to instance", Do: scpAgentStep(s)},
			{Label: "Install watcher on instance", Do: remoteInstallStep(s)},
			{Label: "Start watcher", Do: remoteUpStep(s)},
			{Label: "Save lightsail.conf", Do: saveConfigStep},
			{Label: "Restore global view", Do: unpinStoreStep(s)},
		},
	}
}

// createPreResolveAgent pre-resolves the agent binary so the wizard
// can skip the agent-path prompt when a cached or local build exists.
// Unlike deploy, create does NOT preload from lightsail.conf — it's
// creating a fresh app and should prompt for everything.
func createPreResolveAgent(_ context.Context, in registry.Input) error {
	preresolveAgentBinary(context.Background(), in)
	return nil
}

// ── Suggests ──────────────────────────────────────────────────────────

func instanceSuggest(s *store) func(context.Context) ([]registry.Choice, error) {
	return func(ctx context.Context) ([]registry.Choice, error) {
		c, err := s.ensure(ctx)
		if err != nil {
			return nil, err
		}
		insts, err := c.ListInstances(ctx)
		if err != nil {
			return nil, err
		}
		// Sort by region then name so instances cluster by region in the
		// picker (visually reinforces that picking an instance picks a region).
		sort.Slice(insts, func(i, j int) bool {
			if insts[i].Region != insts[j].Region {
				return insts[i].Region < insts[j].Region
			}
			return insts[i].Name < insts[j].Name
		})
		// Compute column widths for alignment.
		nameW, regionW, stateW := 0, 0, 0
		for _, i := range insts {
			if len(i.Name) > nameW {
				nameW = len(i.Name)
			}
			if len(i.Region) > regionW {
				regionW = len(i.Region)
			}
			if len(i.State) > stateW {
				stateW = len(i.State)
			}
		}
		out := make([]registry.Choice, 0, len(insts))
		for _, i := range insts {
			out = append(out, registry.Choice{
				Value:   i.Name,
				Display: fmt.Sprintf("%-*s  %-*s  %-*s  %s", regionW, i.Region, nameW, i.Name, stateW, i.State, i.IP),
			})
		}
		return out, nil
	}
}

// ── Steps ─────────────────────────────────────────────────────────────

// resolveRegionStep fills --region from the picked instance's actual
// region so the user never has to pick region separately.
func resolveRegionStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		if st.Input.Get("region") != "" {
			return nil
		}
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		region, err := regionOfInstance(ctx, c, st.Input.Get("instance"))
		if err != nil {
			return err
		}
		st.Input["region"] = region
		return nil
	}
}

// pinRegionStep locks the store to --region before any regional op runs.
func pinRegionStep(s *store) func(context.Context, *registry.State) error {
	return func(_ context.Context, st *registry.State) error {
		r := st.Input.Get("region")
		if r == "" {
			return fmt.Errorf("region is required")
		}
		if s.region != nil && *s.region != r {
			*s.region = r
			s.client = nil
		}
		return nil
	}
}

func createEnvBucketStep(s *store) func(context.Context, *registry.State) error {
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
		if err := c.CreateBucket(ctx, bucket); err != nil {
			return err
		}
		st.Data["bucket"] = bucket
		return nil
	}
}

func tagInstanceStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		key := lightsail.TagPrefix + st.Input.Get("name") + ":" + st.Input.Get("env")
		return c.TagInstance(ctx, st.Input.Get("instance"), key, "true")
	}
}

func untagInstanceUndo(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c := mustClient(ctx, s)
		if c == nil {
			return nil
		}
		_, _ = c.UntagInstancesForApp(ctx, st.Input.Get("name"))
		return nil
	}
}

func grantAccessStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		return c.SetBucketAccessForInstance(ctx,
			st.Data["bucket"].(string), st.Input.Get("instance"), true)
	}
}

func revokeAccessUndo(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		bucket, ok := st.Data["bucket"].(string)
		if !ok {
			return nil
		}
		c := mustClient(ctx, s)
		if c == nil {
			return nil
		}
		_ = c.SetBucketAccessForInstance(ctx, bucket, st.Input.Get("instance"), false)
		return nil
	}
}

func scpAgentStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		creds, err := c.GetInstanceSSH(ctx, st.Input.Get("instance"))
		if err != nil {
			return err
		}
		defer creds.Remove()

		// 1) scp to /tmp, 2) sudo mv into /usr/local/bin, 3) smoke-test --version.
		if err := creds.SCPTo(ctx, st.Input.Get("agent-path"), "/tmp/lightsailctl", false); err != nil {
			return err
		}
		install := "sudo mv /tmp/lightsailctl /usr/local/bin/lightsailctl && sudo chmod +x /usr/local/bin/lightsailctl && /usr/local/bin/lightsailctl --version"
		if out, err := creds.SSHRun(ctx, install); err != nil {
			return fmt.Errorf("install/verify: %s: %w", out, err)
		}
		return nil
	}
}

func remoteInstallStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		creds, err := c.GetInstanceSSH(ctx, st.Input.Get("instance"))
		if err != nil {
			return err
		}
		defer creds.Remove()
		cmd := fmt.Sprintf(
			"sudo /usr/local/bin/lightsailctl app local install --app %s --env %s --bucket %s --region %s --instance %s",
			st.Input.Get("name"), st.Input.Get("env"),
			st.Data["bucket"].(string), st.Input.Get("region"),
			st.Input.Get("instance"))
		if out, err := creds.SSHRun(ctx, cmd); err != nil {
			return fmt.Errorf("remote install: %s: %w", out, err)
		}
		return nil
	}
}

func remoteUpStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		creds, err := c.GetInstanceSSH(ctx, st.Input.Get("instance"))
		if err != nil {
			return err
		}
		defer creds.Remove()
		cmd := fmt.Sprintf("sudo /usr/local/bin/lightsailctl app local up --app %s --env %s",
			st.Input.Get("name"), st.Input.Get("env"))
		if out, err := creds.SSHRun(ctx, cmd); err != nil {
			return fmt.Errorf("remote up: %s: %w", out, err)
		}
		return nil
	}
}

func saveConfigStep(ctx context.Context, st *registry.State) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	// Merge with any existing conf so we don't drop fields we don't know
	// about here (e.g. user-added Ignore entries).
	existing, _ := config.Load(filepath.Join(cwd, config.Filename))
	// On the create-new-instance abort path we get here before
	// createNewInstanceInlineStep ran, so Input["region"] / ["instance"]
	// aren't populated yet. Fall back to the namespaced new-instance
	// answers so the saved conf reflects the user's choices.
	region := st.Input.Get("region")
	if region == "" {
		region = st.Input.Get("__ni/region")
	}
	cfg := &config.Config{
		App:       st.Input.Get("name"),
		Env:       st.Input.Get("env"),
		Region:    region,
		Instance:  st.Input.Get("instance"),
		AgentPath: st.Input.Get("agent-path"),
	}
	if existing != nil {
		cfg.Ignore = existing.Ignore
		cfg.Instances = existing.Instances
	}
	// Ensure the current instance is in the Instances list.
	if inst := cfg.Instance; inst != "" {
		found := false
		for _, i := range cfg.Instances {
			if i == inst {
				found = true
				break
			}
		}
		if !found {
			cfg.Instances = append(cfg.Instances, inst)
		}
	}
	// Persist the create-new-instance draft when the deploy is
	// aborted before the instance actually gets created. Next run
	// re-reads these into Input so the wizard doesn't re-ask. The
	// draft is cleared once the instance is real (see
	// clearPendingInstanceStep).
	aborted, _ := st.Data["aborted"].(bool)
	strategy, _ := st.Data["strategy"].(string)
	if aborted && strategy == "create-new" && cfg.Instance == "" {
		cfg.PendingInstance = pendingInstanceFromInput(st.Input)
	}
	p := filepath.Join(cwd, config.Filename)
	if err := cfg.Save(p); err != nil {
		return err
	}
	st.Data["conf_path"] = p
	return nil
}

// pendingInstanceFromInput extracts the namespaced __ni/* answers
// into a config.PendingInstance draft. Returns nil when no draft
// fields are set.
func pendingInstanceFromInput(in registry.Input) *config.PendingInstance {
	p := &config.PendingInstance{
		Name:          in.Get("__ni/name"),
		Region:        in.Get("__ni/region"),
		BlueprintType: in.Get("__ni/blueprint-type"),
		Blueprint:     in.Get("__ni/blueprint"),
		Bundle:        in.Get("__ni/bundle"),
		IPAddressType: in.Get("__ni/ip-address-type"),
		UserData:      in.Get("__ni/user-data"),
		Monitoring:    in.Get("__ni/monitoring"),
	}
	if p.Name == "" && p.Region == "" && p.Blueprint == "" && p.Bundle == "" {
		return nil
	}
	return p
}

// mustClient panics if the client can't be built; only for Undo paths where
// we're already in a failure state and can't do much else. Returns nil only
// when there's nothing we could have done anyway.
func mustClient(ctx context.Context, s *store) *lightsail.Client {
	c, err := s.ensure(ctx)
	if err != nil {
		return nil
	}
	return c
}
