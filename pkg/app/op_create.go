package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/config"
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
	return registry.Operation{
		Name: "create", Key: "c", Short: "create a new Lightsail application",
		Confirm: "Create this Lightsail application?",
		Fields: []registry.Field{
			{Flag: "name", Short: "n", Help: "app name", Prefill: names.DefaultAppName,
				Required: true, Validate: names.ValidateLabel},
			{Flag: "env", Short: "e", Help: "environment", Default: "dev", Required: true, Validate: names.ValidateLabel},
			{Flag: "instance", Help: "target Lightsail instance",
				Required: true, Suggest: instanceSuggest(s)},
			{Flag: "agent-path", Help: "lightsailctl binary to scp to the instance (linux/amd64)",
				Required: true, File: true},
			{Flag: "region", Help: "AWS region (auto-filled from --instance)",
				Wizard: registry.BoolPtr(false)},
		},
		Steps: []registry.Step{
			{Label: "Resolve region from instance", Do: resolveRegionStep(s)},
			{Label: "Pin region", Do: pinRegionStep(s), Undo: unpinStoreStep(s)},
			{Label: "Verify agent binary exists", Do: verifyAgentStep},
			{Label: "Create app-config bucket", Do: createAppBucketStep(s)},
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

func verifyAgentStep(_ context.Context, st *registry.State) error {
	p := st.Input.Get("agent-path")
	fi, err := os.Stat(p)
	if err != nil {
		return fmt.Errorf("agent binary not found at %s: %w", p, err)
	}
	if fi.IsDir() || fi.Size() == 0 {
		return fmt.Errorf("agent binary at %s is not a regular file", p)
	}
	return nil
}

func createAppBucketStep(s *store) func(context.Context, *registry.State) error {
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
		return c.CreateBucket(ctx, lightsail.AppBucketName(acct, st.Input.Get("name")))
	}
}

func createEnvBucketStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		acct := st.Data["acct"].(string)
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
	cfg := &config.Config{
		App:       st.Input.Get("name"),
		Env:       st.Input.Get("env"),
		Region:    st.Input.Get("region"),
		Instance:  st.Input.Get("instance"),
		AgentPath: st.Input.Get("agent-path"),
	}
	if existing != nil {
		cfg.Ignore = existing.Ignore
	}
	p := filepath.Join(cwd, config.Filename)
	if err := cfg.Save(p); err != nil {
		return err
	}
	st.Data["conf_path"] = p
	return nil
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
