package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/aws/lightsailctl/pkg/compose"
	"github.com/aws/lightsailctl/pkg/config"
	"github.com/aws/lightsailctl/pkg/deploy"
	"github.com/aws/lightsailctl/pkg/lightsail"
	"github.com/aws/lightsailctl/pkg/names"
)

// deployOp implements `lightsailctl app deploy` / `lightsailctl deploy`.
//
// Required: app, env, region (all default from lightsail.conf if present).
// The target Application MUST already exist (buckets + tagged instance +
// installed watcher). Phase 4 adds auto-create; Phase 3 emits a clear error
// telling the user to run `lightsailctl app create` first.
func deployOp(s *store) registry.Operation {
	return registry.Operation{
		Name: "deploy", Key: "d", Short: "deploy current dir to an app/env",
		Fields: []registry.Field{
			// Prefill (not Default) so the wizard opens with the git
			// repo name / space-themed random pre-filled — user hits
			// Enter to accept, or types to override.
			{Flag: "name", Short: "n", Help: "app name", Required: true,
				Prefill: names.DefaultAppName, Validate: names.ValidateLabel},
			{Flag: "env", Short: "e", Help: "environment", Default: "dev", Validate: names.ValidateLabel},
			{Flag: "region", Help: "AWS region"},
			{Flag: "wait-timeout", Help: "how long to wait for healthy (0 with --no-wait)", Default: "3m"},
			{Flag: "no-wait", Help: "upload and exit without waiting for health", Default: "false"},
		},
		Steps: []registry.Step{
			{Label: "Resolve app/env/region from flags + lightsail.conf", Do: resolveStep(s)},
			{Label: "Ensure app exists (auto-create if missing)", Do: ensureAppStep(s)},
			{Label: "Package source", Do: packageStep, Undo: packageUndo},
			{Label: "Acquire bucket key + S3 client", Do: acquireKeyStep(s), Undo: acquireKeyUndo},
			{Label: "Upload deploy asset", Do: uploadStep},
			{Label: "Open firewall ports from compose", Do: firewallStep(s)},
			{Label: "Release bucket key", Do: releaseKeyStep},
			{Label: "Wait for healthy", Do: waitStep(s), Skip: skipIfNoWait},
		},
	}
}

// ── state keys stashed in registry.State.Data ────────────────────────────
// "acct"      string            — AWS account ID
// "bucket"    string            — env bucket name
// "asset"     string            — deploy/<unix>-<sha>.tar.gz (S3 key)
// "tarball"   string            — local tmp file path
// "s3cli"     *s3.Client        — scoped to the env bucket
// "keyClean"  func()            — deletes marker + access key
// "started"   time.Time         — when upload began (for since-filter)
// ─────────────────────────────────────────────────────────────────────────

// resolveStep fills in app/env/region from flags, falling back to lightsail.conf.
func resolveStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		// Auto-load lightsail.conf as default source.
		cfg, _ := config.LoadFromCwd()
		if cfg != nil {
			if st.Input.Get("name") == "" {
				st.Input["name"] = cfg.App
			}
			if st.Input.Get("env") == "" {
				st.Input["env"] = cfg.Env
			}
			if st.Input.Get("region") == "" {
				st.Input["region"] = cfg.Region
			}
			st.Data["ignore"] = cfg.Ignore
		}
		if st.Input.Get("env") == "" {
			st.Input["env"] = "dev"
		}
		if st.Input.Get("name") == "" {
			return errors.New("no app configured (set --name or create ./lightsail.conf)")
		}
		if st.Input.Get("region") != "" && s.region != nil {
			// Pin the store to the flag-supplied region so subsequent steps
			// hit the right Lightsail endpoint.
			*s.region = st.Input.Get("region")
			s.client = nil
		}
		return nil
	}
}

// ensureAppStep: the env bucket must exist or we can't deploy. When it's
// missing, ask the user (via registry.NeedInput) for just the inputs the
// create flow needs — instance + agent-path. The region is INFERRED from
// the picked instance (the user never picks a region separately).
func ensureAppStep(s *store) func(context.Context, *registry.State) error {
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
		envBucket := lightsail.EnvBucketName(acct, st.Input.Get("name"), st.Input.Get("env"))
		if b := findBucket(ctx, c, envBucket); b != nil {
			st.Data["bucket"] = envBucket
			pinRegion(s, b.Region)
			return nil
		}

		// Bucket missing. Do we have the inputs to create it?
		needInstance := st.Input.Get("instance") == ""
		needAgent := st.Input.Get("agent-path") == ""
		if needInstance || needAgent {
			return needInputForCreate(needInstance, needAgent, s, st.Input.Get("name"), st.Input.Get("env"))
		}
		// Inputs are filled. Derive the region from the picked instance.
		if st.Input.Get("region") == "" {
			region, rerr := regionOfInstance(ctx, c, st.Input.Get("instance"))
			if rerr != nil {
				return rerr
			}
			st.Input["region"] = region
		}
		// Run create inline.
		if err := runCreateInline(ctx, s, st); err != nil {
			return err
		}
		st.Data["bucket"] = lightsail.EnvBucketName(acct, st.Input.Get("name"), st.Input.Get("env"))
		return nil
	}
}

// regionOfInstance finds the AWS region of a Lightsail instance by its
// (globally unique) name. Uses the client as-is (already global-aware)
// and returns an error if the instance doesn't exist.
func regionOfInstance(ctx context.Context, c *lightsail.Client, name string) (string, error) {
	instances, err := c.ListInstances(ctx)
	if err != nil {
		return "", err
	}
	for _, i := range instances {
		if i.Name == name {
			if i.Region == "" {
				return "", fmt.Errorf("instance %q has no region on record", name)
			}
			return i.Region, nil
		}
	}
	return "", fmt.Errorf("instance %q not found", name)
}

func findBucket(ctx context.Context, c *lightsail.Client, name string) *lightsail.Bucket {
	buckets, err := c.ListAppBuckets(ctx)
	if err != nil {
		return nil
	}
	for i := range buckets {
		if buckets[i].Name == name {
			return &buckets[i]
		}
	}
	return nil
}

func pinRegion(s *store, region string) {
	if s.region != nil && region != "" && *s.region != region {
		*s.region = region
		s.client = nil
	}
}

// needInputForCreate builds a NeedInput listing only the fields that aren't
// already set, so the user doesn't re-answer things they already provided.
func needInputForCreate(needInstance, needAgent bool, s *store, app, env string) error {
	var fields []registry.Field
	if needInstance {
		fields = append(fields, registry.Field{
			Flag: "instance", Required: true, Help: "target Lightsail instance",
			Suggest: instanceSuggest(s),
		})
	}
	if needAgent {
		fields = append(fields, registry.Field{
			Flag: "agent-path", Required: true,
			Help: "lightsailctl binary to scp to the instance (linux/amd64)",
			File: true, // picker accepts any file when AllowedExts is empty
		})
	}
	return &registry.NeedInput{
		Fields: fields,
		Reason: fmt.Sprintf("app %q / env %q doesn't exist yet — let's create it", app, env),
	}
}

// runCreateInline executes the create saga's side-effect steps directly.
// Requires region to already be set in Input (derived from the picked
// instance in ensureAppStep).
func runCreateInline(ctx context.Context, s *store, st *registry.State) error {
	pinRegion(s, st.Input.Get("region"))

	for _, do := range []func(context.Context, *registry.State) error{
		verifyAgentStep,
		createAppBucketStep(s),
		createEnvBucketStep(s),
		tagInstanceStep(s),
		grantAccessStep(s),
		scpAgentStep(s),
		remoteInstallStep(s),
		remoteUpStep(s),
		saveConfigStep,
	} {
		if err := do(ctx, st); err != nil {
			return err
		}
	}
	return nil
}

func packageStep(ctx context.Context, st *registry.State) error {
	if compose.Find() == "" {
		return errors.New("no docker-compose.yml in current directory")
	}
	ignore, _ := st.Data["ignore"].([]string)
	path, _, err := deploy.Package(".", ignore)
	if err != nil {
		return err
	}
	st.Data["tarball"] = path
	st.Data["asset"] = deploy.AssetName()
	st.Data["started"] = time.Now().UTC()
	return nil
}

func packageUndo(_ context.Context, st *registry.State) error {
	if path, ok := st.Data["tarball"].(string); ok {
		_ = os.Remove(path)
	}
	return nil
}

func acquireKeyStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		bucket := st.Data["bucket"].(string)
		s3cli, cleanup, err := c.S3ClientFor(ctx, bucket)
		if err != nil {
			return err
		}
		st.Data["s3cli"] = s3cli
		st.Data["keyClean"] = cleanup
		return nil
	}
}

func acquireKeyUndo(_ context.Context, st *registry.State) error {
	if cleanup, ok := st.Data["keyClean"].(func()); ok {
		cleanup()
		delete(st.Data, "keyClean")
	}
	return nil
}

func uploadStep(ctx context.Context, st *registry.State) error {
	s3cli := st.Data["s3cli"].(*s3.Client)
	return deploy.Upload(ctx, s3cli,
		st.Data["bucket"].(string),
		st.Data["asset"].(string),
		st.Data["tarball"].(string))
}

func firewallStep(s *store) func(context.Context, *registry.State) error {
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
		targets, err := c.FindTargetsForAppEnv(ctx, st.Input.Get("name"), st.Input.Get("env"))
		if err != nil || len(targets) == 0 {
			return nil // no targets yet is fine during first deploy
		}
		for _, t := range targets {
			_, _ = c.OpenFirewallPorts(ctx, t.Name, ports)
		}
		return nil
	}
}

func releaseKeyStep(ctx context.Context, st *registry.State) error {
	if cleanup, ok := st.Data["keyClean"].(func()); ok {
		cleanup()
		delete(st.Data, "keyClean")
	}
	if path, ok := st.Data["tarball"].(string); ok {
		_ = os.Remove(path)
	}
	return nil
}

func skipIfNoWait(st *registry.State) bool {
	v := st.Input.Get("no-wait")
	return v == "true" || v == "1" || v == "yes"
}

func waitStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		timeout, err := time.ParseDuration(firstNonEmpty(st.Input.Get("wait-timeout"), "3m"))
		if err != nil {
			return fmt.Errorf("invalid --wait-timeout: %w", err)
		}
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		waitCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		started := st.Data["started"].(time.Time)
		bucket := st.Data["bucket"].(string)
		werr := deploy.WaitForHealthy(waitCtx, c, bucket, started, 5*time.Second)
		if errors.Is(werr, context.DeadlineExceeded) {
			// Don't fail the saga: the deploy was uploaded, the watcher may
			// just be slow. User sees this as a warning in the saga log.
			return nil
		}
		return werr
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
