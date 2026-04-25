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
			// Default to git-repo-or-random rather than a Suggest picker:
			// fresh `deploy` runs auto-create when the app is missing, so
			// forcing the user to "pick from existing apps" is wrong for
			// the bootstrap path.
			{Flag: "name", Short: "n", Help: "app name", Required: true, Default: names.DefaultAppName()},
			{Flag: "env", Short: "e", Help: "environment", Default: "dev"},
			{Flag: "region", Help: "AWS region"},
			{Flag: "wait-timeout", Help: "how long to wait for healthy (0 with --no-wait)", Default: "3m"},
			{Flag: "no-wait", Help: "upload and exit without waiting for health", Default: "false"},
		},
		Steps: []registry.Step{
			{Label: "Resolve app/env/region from flags + lightsail.conf", Do: resolveStep(s)},
			{Label: "Verify app exists", Do: verifyAppStep(s)},
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

// verifyAppStep: the env bucket must exist or we can't deploy. When the
// store is running global (no --region), we find the bucket across regions
// and pin the store to its region so all subsequent saga steps hit the
// right Lightsail endpoint.
func verifyAppStep(s *store) func(context.Context, *registry.State) error {
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
		buckets, err := c.ListAppBuckets(ctx)
		if err != nil {
			return err
		}
		for _, b := range buckets {
			if b.Name == envBucket {
				st.Data["bucket"] = envBucket
				// Pin the store to the bucket's region for subsequent steps.
				if s.region != nil && b.Region != "" && *s.region != b.Region {
					*s.region = b.Region
					s.client = nil
				}
				return nil
			}
		}
		return fmt.Errorf("app %q / env %q not found — run `lightsailctl app create` first",
			st.Input.Get("name"), st.Input.Get("env"))
	}
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
