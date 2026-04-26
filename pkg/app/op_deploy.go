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
			// All fields collected up front on a single wizard screen.
			// Pre (below) hydrates from lightsail.conf so an existing
			// conf skips the wizard entirely. Fields still empty after
			// Pre are prompted as usual.
			{Flag: "name", Short: "n", Help: "app name", Required: true,
				Prefill: names.DefaultAppName, Validate: names.ValidateLabel},
			{Flag: "env", Short: "e", Help: "environment", Default: "dev", Required: true, Validate: names.ValidateLabel},
			{Flag: "instance", Help: "target Lightsail instance (region inferred from it)", Required: true, Suggest: instanceSuggest(s)},
			{Flag: "agent-path", Help: "lightsailctl binary to scp to the instance (linux/amd64)",
				Required: true, File: true},
			{Flag: "region", Help: "AWS region (auto-filled from --instance)"},
			{Flag: "wait-timeout", Help: "how long to wait for healthy (0 with --no-wait)", Default: "3m"},
			{Flag: "no-wait", Help: "upload and exit without waiting for health", Default: "false"},
		},
		Pre: preloadFromConf,
		Steps: []registry.Step{
			// First visible step reflects the conf state to the user.
			// detectConfStep is invisible housekeeping that records
			// conf_existed + conf_complete in st.Data; the two
			// user-facing rows below pick which to render.
			{Label: "Inspect lightsail.conf", Do: detectConfStep},
			{Label: "Resolve region from instance", Do: resolveRegionFromInstanceStep(s), Skip: skipIfRegionSet, Undo: unpinStoreStep(s)},
			{Label: "Create lightsail.conf", Do: saveConfigStep, Skip: skipIfConfDetected},
			{Label: "Detected lightsail.conf", Do: noopStep, Skip: skipIfConfNotDetected},
			{Label: "Check app exists (create if missing)", Do: ensureAppStep(s), Undo: unpinStoreStep(s)},
			// Publish an optimistic entry to the shared cache as soon as
			// we know we're creating, BEFORE the slow bucket-create
			// steps run. The TUI's next refresh picks it up and the new
			// app appears in the table within seconds rather than
			// minutes.
			{Label: "Announce new app to the table", Do: announceOptimisticStep(s), Skip: skipIfAppExists},
			// Create sub-steps run only when ensureAppStep decided we
			// need to. Skipped on subsequent deploys to an existing app.
			{Label: "Verify agent binary", Do: verifyAgentStep, Skip: skipIfAppExists},
			{Label: "Create app-config bucket", Do: createAppBucketStep(s), Skip: skipIfAppExists},
			{Label: "Create env bucket", Do: createEnvBucketStep(s), Skip: skipIfAppExists},
			{Label: "Tag target instance", Do: tagInstanceStep(s), Skip: skipIfAppExists},
			{Label: "Grant instance bucket access", Do: grantAccessStep(s), Skip: skipIfAppExists},
			{Label: "Copy agent binary to instance", Do: scpAgentStep(s), Skip: skipIfAppExists},
			{Label: "Install agent on instance", Do: remoteInstallStep(s), Skip: skipIfAppExists},
			{Label: "Start agent on instance", Do: remoteUpStep(s), Skip: skipIfAppExists},
			{Label: "Package source", Do: packageStep, Undo: packageUndo},
			{Label: "Acquire bucket key + S3 client", Do: acquireKeyStep(s), Undo: acquireKeyUndo},
			{Label: "Upload deploy asset", Do: uploadStep},
			{Label: "Open firewall ports from compose", Do: firewallStep(s)},
			{Label: "Release bucket key", Do: releaseKeyStep},
			{Label: "Wait for healthy", Do: waitStep(s), Skip: skipIfNoWait},
			// Unpin the store so the next TUI refresh fans out across
			// ALL regions again rather than staying pinned to whichever
			// region this deploy touched. Without this, the app list
			// post-deploy is limited to the single-region view and
			// other-region apps vanish from the table.
			{Label: "Restore global view", Do: unpinStoreStep(s)},
		},
	}
}

// unpinStoreStep clears the store's region and forces a fresh client on
// next ensure, so subsequent refreshes go back to the global fanout.
func unpinStoreStep(s *store) func(context.Context, *registry.State) error {
	return func(_ context.Context, _ *registry.State) error {
		if s.region != nil {
			*s.region = ""
		}
		s.client = nil
		return nil
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

// preloadFromConf is the op Pre hook: hydrates Input from lightsail.conf
// before the wizard considers what's missing. If the conf is complete
// the wizard shows nothing; if partial, the wizard opens with known
// fields prefilled.
func preloadFromConf(_ context.Context, in registry.Input) error {
	cfg, _ := config.LoadFromCwd()
	if cfg == nil {
		return nil
	}
	if in.Get("name") == "" && cfg.App != "" {
		in["name"] = cfg.App
	}
	if in.Get("env") == "" && cfg.Env != "" {
		in["env"] = cfg.Env
	}
	if in.Get("region") == "" && cfg.Region != "" {
		in["region"] = cfg.Region
	}
	if in.Get("instance") == "" && cfg.Instance != "" {
		in["instance"] = cfg.Instance
	}
	if in.Get("agent-path") == "" && cfg.AgentPath != "" {
		in["agent-path"] = cfg.AgentPath
	}
	return nil
}

// detectConfStep records in st.Data whether the conf pre-existed and
// whether it was complete (had every field the deploy needs). The two
// user-visible "Create lightsail.conf" / "Detected lightsail.conf" rows
// use these flags via their Skip funcs to pick which runs.
func detectConfStep(_ context.Context, st *registry.State) error {
	cfg, _ := config.LoadFromCwd()
	existed := cfg != nil
	complete := existed &&
		cfg.App != "" && cfg.Env != "" &&
		cfg.Region != "" && cfg.Instance != "" && cfg.AgentPath != ""
	st.Data["conf_existed"] = existed
	st.Data["conf_complete"] = complete
	return nil
}

// skipIfConfDetected skips "Create lightsail.conf" when the conf already
// exists AND is complete. If the conf existed but was missing fields the
// wizard just filled, we still want to rewrite.
func skipIfConfDetected(st *registry.State) bool {
	existed, _ := st.Data["conf_existed"].(bool)
	complete, _ := st.Data["conf_complete"].(bool)
	return existed && complete
}

// skipIfConfNotDetected hides the "Detected" row when we WROTE or updated
// the conf (either fresh-create or fill-in-missing case).
func skipIfConfNotDetected(st *registry.State) bool {
	existed, _ := st.Data["conf_existed"].(bool)
	complete, _ := st.Data["conf_complete"].(bool)
	return !existed || !complete
}

// skipIfRegionSet skips the region-from-instance resolver when region
// is already known (from conf or --region flag).
func skipIfRegionSet(st *registry.State) bool {
	return st.Input.Get("region") != ""
}

// noopStep is a zero-work step used purely to show a green "Detected
// lightsail.conf" row in the saga overlay. Also propagates the ignore
// list from the conf into st.Data for the package step.
func noopStep(_ context.Context, st *registry.State) error {
	if cfg, _ := config.LoadFromCwd(); cfg != nil {
		st.Data["ignore"] = cfg.Ignore
	}
	return nil
}

// resolveRegionFromInstanceStep fills Input["region"] by looking up the
// instance's region. Pre already confirmed the user has an instance name.
func resolveRegionFromInstanceStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		region, rerr := regionOfInstance(ctx, c, st.Input.Get("instance"))
		if rerr != nil {
			return rerr
		}
		st.Input["region"] = region
		pinRegion(s, region)
		return nil
	}
}

// ensureAppStep: the env bucket must exist or we can't deploy. If the
// bucket is already there, "needs-create" stays false and the subsequent
// create sub-steps skip. If it's missing, mark needs-create=true so the
// create sub-steps run. (No more NeedInput / mid-saga prompting — Pre +
// the up-front wizard gave us everything.)
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
		// Propagate the conf's ignore list for the package step.
		// (noopStep does the same for the already-detected path.)
		if cfg, _ := config.LoadFromCwd(); cfg != nil {
			st.Data["ignore"] = cfg.Ignore
		}
		envBucket := lightsail.EnvBucketName(acct, st.Input.Get("name"), st.Input.Get("env"))
		if b := findBucket(ctx, c, envBucket); b != nil {
			st.Data["bucket"] = envBucket
			pinRegion(s, b.Region)
			st.Data["needs-create"] = false
			return nil
		}
		pinRegion(s, st.Input.Get("region"))
		st.Data["needs-create"] = true
		st.Data["bucket"] = envBucket
		return nil
	}
}

// skipIfAppExists returns true when ensureAppStep already confirmed the
// app is live. The create sub-steps skip themselves in that case.
func skipIfAppExists(st *registry.State) bool {
	v, _ := st.Data["needs-create"].(bool)
	return !v
}

// announceOptimisticStep seeds the shared optimistic bucket cache with
// the env bucket (and app-config bucket) so the TUI table shows the new
// app immediately on the next refresh, well before the real CreateBucket
// calls finish. Values are upgraded to state=OK once the real buckets
// come up.
func announceOptimisticStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return nil //nolint:nilerr // pre-announce is best-effort
		}
		acct, _ := st.Data["acct"].(string)
		region := st.Input.Get("region")
		envBucket, _ := st.Data["bucket"].(string)
		appBucket := lightsail.AppBucketName(acct, st.Input.Get("name"))
		c.AnnounceBucket(envBucket, region)
		c.AnnounceBucket(appBucket, region)
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

// pinRegion makes the store's client region-specific. Temporary: callers
// running inside a saga MUST pair this with an unpin step (or an Undo)
// so the post-saga TUI list view goes back to multi-region fanout. A
// permanently pinned store hides apps outside that region from the
// refresh.
func pinRegion(s *store, region string) {
	if s.region != nil && region != "" && *s.region != region {
		*s.region = region
		s.client = nil
	}
}

// needInputForCreate builds a NeedInput listing only the fields that aren't
// already set, so the user doesn't re-answer things they already provided.
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
