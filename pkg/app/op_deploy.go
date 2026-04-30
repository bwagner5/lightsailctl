package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/aws/lightsailctl/internal"
	"github.com/aws/lightsailctl/pkg/agentfetch"
	"github.com/aws/lightsailctl/pkg/compose"
	"github.com/aws/lightsailctl/pkg/config"
	"github.com/aws/lightsailctl/pkg/deploy"
	"github.com/aws/lightsailctl/pkg/instance"
	"github.com/aws/lightsailctl/pkg/lightsail"
	"github.com/aws/lightsailctl/pkg/names"
)

// deployOp implements `lightsailctl app deploy` / `lightsailctl deploy`.
//
// Required: app, env, region (all default from lightsail.conf if present).
// The target Application MUST already exist (buckets + tagged instance +
// installed watcher). When the env bucket is missing, the saga bootstraps
// the app in-place. When no target instance is configured (and none was
// given on the CLI), the saga offers to create a new Lightsail instance
// inline — see pickInstanceStrategyStep / createNewInstanceStep.
func deployOp(s *store) registry.Operation {
	return registry.Operation{
		Name: "deploy", Key: "d", Short: "deploy current dir to an app/env",
		Fields: []registry.Field{
			// Fields collected up front on a single wizard screen.
			// Pre (below) hydrates from lightsail.conf so an existing
			// conf skips the wizard entirely. Fields still empty after
			// Pre are prompted as usual.
			{Flag: "name", Short: "n", Help: "app name", Required: true,
				Prefill: names.DefaultAppName, Validate: names.ValidateLabel},
			{Flag: "env", Short: "e", Help: "environment", Default: "dev", Required: true, Validate: names.ValidateLabel},
			// "instance" stays on the op so the CLI flag works for
			// non-interactive callers, but Wizard:false hides it from
			// the up-front wizard. pickInstanceStrategyStep +
			// pickExistingInstanceStep prompt for it lazily via
			// NeedInput, which lets us skip the picker entirely when
			// the user opts to create a new instance instead.
			{Flag: "instance", Help: "target Lightsail instance",
				Suggest: instanceSuggest(s), Wizard: registry.BoolPtr(false)},
			// Yes/no gate for the create-new-instance branch. Not shown
			// in the up-front wizard — pickInstanceStrategyStep prompts
			// for it lazily after conf detection so we can suppress the
			// question when conf already supplies an instance or when
			// no instances exist globally.
			{Flag: "create-new-instance", Help: "create a new Lightsail instance as part of deploy",
				Default: "false", Wizard: registry.BoolPtr(false)},
			{Flag: "agent-path", Help: "lightsailctl binary to scp to the instance (linux/amd64); auto-fetched from the matching release when unset",
				File: true},
			{Flag: "region", Help: "AWS region (auto-filled from --instance)",
				Wizard: registry.BoolPtr(false)},
			{Flag: "wait-timeout", Help: "how long to wait for healthy", Default: "3m",
				Wizard: registry.BoolPtr(false)},
			{Flag: "no-wait", Help: "upload and exit without waiting for health", Default: "false",
				Wizard: registry.BoolPtr(false)},
		},
		Pre: preloadFromConf,
		Steps: []registry.Step{
			{Label: "Inspect lightsail.conf", Do: func(ctx context.Context, st *registry.State) error {
				// Announce app to table (housekeeping).
				_ = announceEarlyStep(s)(ctx, st)
				// Detect conf.
				return detectConfStep(ctx, st)
			}},
			// Decide branch: use-existing (conf or picker) vs create-new.
			// May prompt yes/no via NeedInput when instances exist and
			// conf didn't name one.
			{Label: "Pick instance strategy", Do: pickInstanceStrategyStep(s)},
			// Create-new branch. Prompts for instance.CreateFields on
			// first invocation; then runs the instance create step.
			// Skipped entirely when strategy == use-existing.
			{Label: "Create new Lightsail instance", Do: createNewInstanceStep(s), Skip: skipUnlessCreatingNewInstance},
			// Use-existing branch. Prompts for instance via NeedInput
			// (the picker was disabled up front). Skipped when we just
			// created one.
			{Label: "Pick target instance", Do: pickExistingInstanceStep(s), Skip: skipIfCreatingNewInstance},
			{Label: "Resolve region from instance", Do: resolveRegionFromInstanceStep(s), Skip: skipIfRegionSet, Undo: unpinStoreStep(s)},
			{Label: "Create lightsail.conf", Do: saveConfigStep, Skip: skipIfConfDetected},
			{Label: "Check app exists (create if missing)", Do: ensureAppStep(s), Undo: unpinStoreStep(s)},
			{Label: "Resolve agent binary", Do: resolveAgentStep, Skip: skipIfAppExists},
			{Label: "Create app-config bucket", Do: createAppBucketStep(s), Skip: skipIfAppExists},
			{Label: "Create env bucket", Do: createEnvBucketStep(s), Skip: skipIfAppExists},
			{Label: "Tag target instance", Do: tagInstanceStep(s), Skip: skipIfAppExists},
			{Label: "Grant instance bucket access", Do: grantAccessStep(s), Skip: skipIfAppExists},
			{Label: "Copy agent binary to instance", Do: scpAgentStep(s), Skip: skipIfAppExists},
			{Label: "Install agent on instance", Do: remoteInstallStep(s), Skip: skipIfAppExists},
			{Label: "Start agent on instance", Do: remoteUpStep(s), Skip: skipIfAppExists},
			{Label: "Package source", Do: packageStep, Undo: packageUndo},
			{Label: "Acquire bucket key", Do: acquireKeyStep(s), Undo: acquireKeyUndo},
			{Label: "Upload deploy asset", Do: uploadStep},
			{Label: "Open firewall ports", Do: firewallStep(s)},
			{Label: "Release bucket key", Do: releaseKeyStep},
			{Label: "Wait for healthy", Do: func(ctx context.Context, st *registry.State) error {
				err := waitStep(s)(ctx, st)
				// Collect endpoints for the completion summary.
				if c, cerr := s.ensure(ctx); cerr == nil {
					if bucket, ok := st.Data["bucket"].(string); ok {
						if statuses, serr := c.ReadBucketStatuses(ctx, bucket); serr == nil {
							var eps []string
							for _, status := range statuses {
								eps = append(eps, status.Endpoints...)
							}
							if len(eps) > 0 {
								st.Output = "Endpoints:\n"
								for _, ep := range eps {
									st.Output += "  " + ep + "\n"
								}
							}
						}
					}
				}
				// Restore global view (housekeeping).
				_ = unpinStoreStep(s)(ctx, st)
				return err
			}, Skip: skipIfNoWait},
			// If no-wait, still restore global view.
			{Label: "Finalize", Do: unpinStoreStep(s), Skip: func(st *registry.State) bool {
				return st.Input.Get("no-wait") != "true"
			}},
			// Offer-CI tail step: only runs on a first-run deploy
			// (conf didn't pre-exist) of a GitHub-hosted repo, and
			// only in interactive mode. See offerGhActionStep doc.
			{Label: "Offer GitHub Actions deploy workflow",
				Do:   offerGhActionStep(s),
				Skip: skipOfferGhAction(s)},
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

// ─────────────────────────────────────────────────────────────────────────
// State keys consumed by the create-new-instance branch:
// "strategy"  string   — "use-existing" | "create-new"
// ─────────────────────────────────────────────────────────────────────────

// pickInstanceStrategyStep decides whether deploy targets an existing
// instance or creates one inline. Three outcomes:
//
//  1. Conf or --instance already named an instance → strategy
//     "use-existing", no prompt.
//  2. No instances exist in the account globally → strategy "create-new",
//     no prompt (the picker would have nothing to show).
//  3. Instances exist and the user hasn't chosen yet → prompt yes/no via
//     NeedInput. The question is always asked in this branch, even when
//     instances exist, so the user can opt in to a fresh one without
//     leaving the wizard.
//
// The runtime retries the step after NeedInput is satisfied, at which
// point create-new-instance is set and the branch decision can be made
// without re-prompting.
func pickInstanceStrategyStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		// Short-circuit: conf or --instance already picked.
		if st.Input.Get("instance") != "" {
			st.Data["strategy"] = "use-existing"
			return nil
		}
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		insts, err := c.ListInstances(ctx)
		if err != nil {
			return err
		}
		// No instances globally → force create. The picker would be empty.
		if len(insts) == 0 {
			st.Data["strategy"] = "create-new"
			st.Input["create-new-instance"] = "true"
			return nil
		}
		// Ask yes/no once.
		if st.Input.Get("create-new-instance") == "" {
			return &registry.NeedInput{
				Reason: "Create a new Lightsail instance to deploy to?",
				Fields: []registry.Field{
					{Flag: "create-new-instance", Required: true,
						Help: "create a new Lightsail instance",
						Suggest: yesNoSuggest(
							"No, pick an existing one",
							"Yes, create a new instance"),
					},
				},
			}
		}
		if st.Input.Get("create-new-instance") == "true" {
			st.Data["strategy"] = "create-new"
		} else {
			st.Data["strategy"] = "use-existing"
		}
		return nil
	}
}

// createNewInstanceStep runs the full instance.CreateFields wizard
// in-place. Two-phase:
//
//  1. First invocation: if no namespaced new-instance answers exist yet,
//     return NeedInput listing the namespaced fields. The runtime pauses,
//     collects, and retries.
//  2. Second invocation: pull the namespaced answers into a sub-State,
//     run instance.CreateStep, then hand the instance name back to the
//     deploy flow via Input["instance"].
//
// The instance.CreateFields closures share mutable blueprint/platform
// pointers that must be allocated exactly once per saga (see
// instance.CreateFields doc). We stash the slice in st.Data on first
// call so the NeedInput and the subsequent create see the same captured
// state.
func createNewInstanceStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		const nsPrefix = "__ni/"

		// Cache CreateFields so closure-captured bpType/platform
		// pointers survive across the NeedInput retry.
		fields, ok := st.Data["__ni_fields"].([]registry.Field)
		if !ok {
			istore := instance.NewStore(s.region, s.regionHints)
			st.Data["__ni_store"] = istore
			fields = instance.CreateFields(istore)
			st.Data["__ni_fields"] = fields
		}

		// First invocation: the required fields haven't been collected.
		// Detect by checking whether any required field is missing in
		// the namespaced input.
		if needsNewInstanceInput(fields, st.Input, nsPrefix) {
			return &registry.NeedInput{
				Reason: "New Lightsail instance",
				Fields: namespaceFields(nsPrefix, fields),
			}
		}

		// Second invocation: run the instance-create step against a
		// private sub-state whose Input holds the un-namespaced values.
		istore := st.Data["__ni_store"].(*instance.Store)
		sub := &registry.State{
			Input: registry.Input{},
			Data:  map[string]any{},
		}
		for _, f := range fields {
			if v, ok := st.Input[nsPrefix+f.Flag]; ok {
				sub.Input[f.Flag] = v
			}
		}
		if err := instance.CreateStep(istore).Do(ctx, sub); err != nil {
			return err
		}
		// Hand the new instance back to deploy. The "instance" field
		// in instance.CreateFields carries the just-created name.
		newName := sub.Input.Get("name")
		if newName == "" {
			return fmt.Errorf("instance created but name not captured")
		}
		st.Input["instance"] = newName
		// The new instance's region is already known — skip the
		// region-resolve step's API call by seeding Input["region"].
		if r := sub.Input.Get("region"); r != "" {
			st.Input["region"] = r
			pinRegion(s, r)
		}
		return nil
	}
}

// pickExistingInstanceStep prompts for --instance via NeedInput. This
// step exists because the up-front wizard doesn't ask for "instance"
// anymore (Wizard:false) — the strategy step decides whether we need it
// at all. Skipped when we just created a new instance.
func pickExistingInstanceStep(s *store) func(context.Context, *registry.State) error {
	return func(_ context.Context, st *registry.State) error {
		if st.Input.Get("instance") != "" {
			return nil
		}
		return &registry.NeedInput{
			Reason: "Target Lightsail instance",
			Fields: []registry.Field{
				{Flag: "instance", Help: "target Lightsail instance",
					Required: true, Suggest: instanceSuggest(s)},
			},
		}
	}
}

// skipUnlessCreatingNewInstance skips the create-new-instance step
// unless the strategy step decided "create-new".
func skipUnlessCreatingNewInstance(st *registry.State) bool {
	strategy, _ := st.Data["strategy"].(string)
	return strategy != "create-new"
}

// skipIfCreatingNewInstance skips the existing-instance picker when
// we're going to create a new one (or already did).
func skipIfCreatingNewInstance(st *registry.State) bool {
	strategy, _ := st.Data["strategy"].(string)
	return strategy == "create-new"
}

// yesNoSuggest returns a Suggest that offers exactly two choices,
// matching the convention used by monitoring / ip-address-type etc.
// The returned values are "false" (no) and "true" (yes) so callers can
// check Input.Get(flag) == "true" without extra parsing.
func yesNoSuggest(noDisplay, yesDisplay string) func(context.Context) ([]registry.Choice, error) {
	return func(_ context.Context) ([]registry.Choice, error) {
		return []registry.Choice{
			{Value: "false", Display: "No   " + noDisplay},
			{Value: "true", Display: "Yes  " + yesDisplay},
		}, nil
	}
}

// namespaceFields returns a copy of fields with each Flag prefixed.
// Suggest/Validate closures fire on values, not flag names, so copying
// only the Flag is safe. The prefix shields the returned fields from
// colliding with any of the parent saga's own fields (e.g. "name").
func namespaceFields(prefix string, fields []registry.Field) []registry.Field {
	out := make([]registry.Field, len(fields))
	for i, f := range fields {
		f.Flag = prefix + f.Flag
		out[i] = f
	}
	return out
}

// needsNewInstanceInput reports whether any required instance-create
// field is missing from the namespaced input. Optional fields (e.g.
// user-data) don't force a NeedInput round-trip on their own.
func needsNewInstanceInput(fields []registry.Field, in registry.Input, prefix string) bool {
	for _, f := range fields {
		if !f.Required {
			continue
		}
		if in.Get(prefix+f.Flag) == "" {
			return true
		}
	}
	return false
}

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
	// agent-path is no longer required in conf — deploy auto-fetches
	// the matching release binary when unset. See resolveAgentStep.
	complete := existed &&
		cfg.App != "" && cfg.Env != "" &&
		cfg.Region != "" && cfg.Instance != ""
	st.Data["conf_existed"] = existed
	st.Data["conf_complete"] = complete
	return nil
}

// resolveAgentStep resolves the linux/amd64 lightsailctl binary that
// will be scp'd to the instance on a bootstrap deploy. Two paths:
//
//   - --agent-path set (or conf's agent-path set): stat it; use as-is.
//   - unset: download the matching release from GitHub, cache under
//     the user's cache dir keyed by version, and use the cached copy.
//
// On success, the resolved absolute path is written back into
// Input["agent-path"] so the downstream scpAgentStep (which existed
// before this behavior shipped) continues to work unchanged.
func resolveAgentStep(ctx context.Context, st *registry.State) error {
	explicit := st.Input.Get("agent-path")
	version := internal.Version().String()
	resolved, err := agentfetch.Resolve(ctx, explicit, version, agentfetch.UserCacheDir())
	if err != nil {
		return err
	}
	st.Input["agent-path"] = resolved
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
		if b := findBucket(ctx, c, st.Input.Get("region"), envBucket); b != nil {
			st.Data["bucket"] = envBucket
			pinRegion(s, b.Region)
			st.Data["needs-create"] = false
			return nil
		}
		region := st.Input.Get("region")
		pinRegion(s, region)
		st.Data["needs-create"] = true
		st.Data["bucket"] = envBucket
		// Re-announce now that we know the region, so the TUI's
		// regional ListBuckets merge (which strictly matches region)
		// picks up the entry. The earlier announceEarlyStep may have
		// fired with an empty region when the conf was bare.
		appBucket := lightsail.AppBucketName(acct, st.Input.Get("name"))
		c.AnnounceBucket(envBucket, region)
		c.AnnounceBucket(appBucket, region)
		return nil
	}
}

// skipIfAppExists returns true when ensureAppStep already confirmed the
// app is live. The create sub-steps skip themselves in that case.
func skipIfAppExists(st *registry.State) bool {
	v, _ := st.Data["needs-create"].(bool)
	return !v
}

// announceEarlyStep fires IMMEDIATELY — before the AccountID lookup in
// ensureAppStep — and publishes an optimistic bucket entry keyed by
// (name, env). AccountID isn't known yet so we use an AccountID lookup
// here too (it's fast; one STS call). Value is cached in st.Data["acct"]
// so ensureAppStep can reuse it.
func announceEarlyStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return nil //nolint:nilerr // best-effort
		}
		acct, err := c.AccountID(ctx)
		if err != nil {
			return nil //nolint:nilerr
		}
		st.Data["acct"] = acct
		envBucket := lightsail.EnvBucketName(acct, st.Input.Get("name"), st.Input.Get("env"))
		appBucket := lightsail.AppBucketName(acct, st.Input.Get("name"))
		region := st.Input.Get("region")
		c.AnnounceBucket(envBucket, region)
		c.AnnounceBucket(appBucket, region)
		return nil
	}
}

// announceOptimisticStep seeds the shared optimistic bucket cache with
// announceOptimisticStep seeds the shared optimistic bucket cache with
// the env bucket (and app-config bucket) so the TUI table shows the new
// app immediately on the next refresh, well before the real CreateBucket
// calls finish. Values are upgraded to state=OK once the real buckets
// come up.

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

// findBucket authoritatively checks whether the env bucket exists. Uses
// the direct GetBuckets API (via Client.GetBucket) instead of the
// optimistic-cache-augmented list, so a PENDING entry the saga just
// announced doesn't make us skip the create sub-steps for a bucket
// that was never actually provisioned.
//
// region is the caller's best guess for where the bucket would live
// (usually the deploy target's region). GetBucket requires a
// region-pinned client; we build one locally rather than mutating the
// shared store's pin state, which is the responsibility of pinRegion.
func findBucket(ctx context.Context, c *lightsail.Client, region, name string) *lightsail.Bucket {
	if region == "" {
		return nil
	}
	b, err := c.WithRegion(region).GetBucket(ctx, name)
	if err != nil {
		return nil
	}
	return b
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
