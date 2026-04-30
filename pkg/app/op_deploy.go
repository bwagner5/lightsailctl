package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
// deployOp implements `lightsailctl app deploy` / `lightsailctl deploy`.
//
// All interactive inputs — app / env / instance strategy / new-instance
// details / review-confirm — are collected up front by the CLI's
// in-built wizard as a single continuous flow. The saga steps below
// assume Input is fully resolved and do no mid-flight NeedInput round
// trips, so the user sees one coherent progress view rather than a
// flashing mix of wizard + progress-bar frames.
//
// Non-interactive callers (-y) populate the fields via flags; defaults
// handle the rest.
func deployOp(s *store) registry.Operation {
	// Shared state used by new-instance field closures. These pointers
	// capture the user's selections so blueprint/bundle suggestions can
	// filter appropriately. Reallocated per call to deployOp so the
	// closure state isn't shared across invocations.
	var (
		niStore    = instance.NewStore(s.region, s.regionHints)
		niBpType   = "os"
		niPlatform = "LINUX_UNIX"
	)
	niBpSuggest, niBpValidate := niBlueprintSuggestAndValidate(niStore, &niBpType, &niPlatform)

	// Predicates used as Field.When values. Keep them as named vars so
	// the field list below stays readable.
	wantsNew := func(in registry.Input) bool {
		v, _ := in.Bool("create-new-instance")
		return v
	}
	wantsExisting := func(in registry.Input) bool {
		v, _ := in.Bool("create-new-instance")
		return !v && in.Get("instance") == ""
	}
	askStrategy := func(in registry.Input) bool {
		// Only prompt the yes/no when conf/flag didn't already name
		// an instance. If --instance is set we go straight to the
		// existing-instance flow.
		return in.Get("instance") == ""
	}

	return registry.Operation{
		Name: "deploy", Key: "d", Short: "deploy current dir to an app/env",
		Fields: []registry.Field{
			// ── Lightsail Application ────────────────────────────
			{Flag: "name", Short: "n", Label: "App name", Help: "app name",
				Section:  "Lightsail Application",
				Required: true, Prefill: names.DefaultAppName, Validate: names.ValidateLabel},
			{Flag: "env", Short: "e", Label: "Environment", Help: "environment",
				Section: "Lightsail Application",
				Default: "dev", Required: true, Validate: names.ValidateLabel},

			// ── Deployment Target ────────────────────────────────
			// Yes/no gate. Hidden when --instance is already set.
			{Flag: "create-new-instance", Label: "Target", Help: "use an existing instance or create a new one",
				Section:  "Deployment Target",
				Kind:     registry.KindBool,
				Required: true,
				When:     askStrategy,
				Suggest: yesNoSuggest(
					"pick an existing instance",
					"create a new instance"),
			},
			// Existing-instance picker: shown only when the user
			// chose "pick existing" and didn't already name one.
			{Flag: "instance", Label: "Lightsail instance",
				Help:     "target Lightsail instance",
				Section:  "Deployment Target",
				Required: true,
				When:     wantsExisting,
				Suggest:  instanceSuggest(s)},

			// ── New Lightsail Instance ───────────────────────────
			// All fields conditional on strategy=create-new. They
			// mirror instance.CreateFields shape but live here so
			// the wizard sees them up front.
			{Flag: "__ni/name", Label: "Instance name", Help: "instance name",
				Section: "New Lightsail Instance", Required: true, When: wantsNew,
				Prefill: names.Random, Validate: names.ValidateLabel},
			{Flag: "__ni/region", Label: "Region", Help: "AWS region",
				Section: "New Lightsail Instance", Required: true, When: wantsNew,
				Default: "us-east-1", Suggest: niRegionSuggest(niStore)},
			{Flag: "__ni/blueprint-type", Label: "Blueprint category",
				Help: "blueprint category", Default: "os",
				Section: "New Lightsail Instance", When: wantsNew,
				Suggest:  niBlueprintTypeSuggest(),
				Validate: func(v string) error { niBpType = v; return nil }},
			{Flag: "__ni/blueprint", Label: "Blueprint", Help: "OS / image",
				Section: "New Lightsail Instance", Required: true, When: wantsNew,
				Default: "amazon_linux_2023", Suggest: niBpSuggest, Validate: niBpValidate},
			{Flag: "__ni/bundle", Label: "Instance size", Help: "instance size",
				Section: "New Lightsail Instance", Required: true, When: wantsNew,
				Default: "micro_x_x", Suggest: niBundleSuggest(niStore, &niPlatform)},
			{Flag: "__ni/ip-address-type", Label: "Networking",
				Help: "networking stack", Default: "dualstack",
				Section: "New Lightsail Instance", When: wantsNew,
				Suggest: niIPTypeSuggest()},
			{Flag: "__ni/user-data", Label: "Launch script", Help: "launch script",
				Section: "New Lightsail Instance", When: wantsNew, File: true},
			{Flag: "__ni/monitoring", Label: "Detailed monitoring",
				Help: "detailed monitoring", Default: "false",
				Section: "New Lightsail Instance", Kind: registry.KindBool, When: wantsNew,
				Suggest: niMonitoringSuggest()},

			// ── Agent binary ─────────────────────────────────────
			// Shown only when Pre couldn't auto-resolve (via local
			// build output or per-version cache). Conf-stored values
			// and explicit --agent-path both bypass the prompt via
			// CompleteInput's "already set" fast path.
			{Flag: "agent-path",
				Label:   "Agent binary",
				Help:    "linux/amd64 lightsailctl binary to scp to the instance",
				Section: "Agent Binary",
				File:    true,
				When:    needsAgentBinaryPrompt},

			// ── Review & Confirm ─────────────────────────────────
			{Flag: "deploy-confirm", Label: "Proceed",
				Help:         "confirm before any changes land",
				Section:      "Review & Confirm",
				Kind:         registry.KindBool,
				Required:     true,
				PreambleFunc: deploySummaryPreamble,
				Suggest: yesNoSuggest(
					"abort",
					"deploy now"),
			},

			// ── Hidden fields (flags only; not prompted) ─────────
			{Flag: "region", Help: "AWS region (auto-filled from --instance)",
				Wizard: registry.BoolPtr(false)},
			{Flag: "wait-timeout", Help: "how long to wait for healthy", Default: "3m",
				Kind: registry.KindDuration, Wizard: registry.BoolPtr(false)},
			{Flag: "no-wait", Help: "upload and exit without waiting for health", Default: "false",
				Kind: registry.KindBool, Wizard: registry.BoolPtr(false)},
		},
		Pre: deployPre,
		Steps: []registry.Step{
			{Label: "Inspect lightsail.conf", Do: func(ctx context.Context, st *registry.State) error {
				// Announce app to table (housekeeping).
				_ = announceEarlyStep(s)(ctx, st)
				// Detect conf.
				return detectConfStep(ctx, st)
			}},
			// Configuration phase: translate up-front wizard answers
			// into saga state. Pure: sets st.Data["strategy"], no
			// AWS calls, no mutations.
			{Label: "Resolve deployment target", Do: applyStrategyStep(s)},
			// Check the review-confirm answer FIRST, before any
			// mutating work (instance create, bucket create, ...).
			// Decline short-circuits via st.Data["aborted"]; all
			// downstream steps see skipIfAborted.
			{Label: "Confirm deploy plan", Do: abortIfDeclinedStep},
			{Label: "Create new Lightsail instance",
				Do:   createNewInstanceInlineStep(s),
				Skip: skipUnlessCreatingNewInstanceOrAborted},
			{Label: "Resolve region from instance", Do: resolveRegionFromInstanceStep(s),
				Skip: skipIfRegionSetOrAborted, Undo: unpinStoreStep(s)},
			{Label: "Save lightsail.conf", Do: saveConfigStep, Skip: skipIfConfDetected},
			{Label: "Check app exists (create if missing)", Do: ensureAppStep(s),
				Skip: skipIfAborted, Undo: unpinStoreStep(s)},
			{Label: "Resolve agent binary", Do: resolveAgentStep, Skip: skipIfAbortedOrAppExists},
			{Label: "Create app-config bucket", Do: createAppBucketStep(s), Skip: skipIfAbortedOrAppExists},
			{Label: "Create env bucket", Do: createEnvBucketStep(s), Skip: skipIfAbortedOrAppExists},
			{Label: "Tag target instance", Do: tagInstanceStep(s), Skip: skipIfAbortedOrAppExists},
			{Label: "Grant instance bucket access", Do: grantAccessStep(s), Skip: skipIfAbortedOrAppExists},
			{Label: "Copy agent binary to instance", Do: scpAgentStep(s), Skip: skipIfAbortedOrAppExists},
			{Label: "Install agent on instance", Do: remoteInstallStep(s), Skip: skipIfAbortedOrAppExists},
			{Label: "Start agent on instance", Do: remoteUpStep(s), Skip: skipIfAbortedOrAppExists},
			{Label: "Package source", Do: packageStep, Skip: skipIfAborted, Undo: packageUndo},
			{Label: "Acquire bucket key", Do: acquireKeyStep(s), Skip: skipIfAborted, Undo: acquireKeyUndo},
			{Label: "Upload deploy asset", Do: uploadStep, Skip: skipIfAborted},
			{Label: "Open firewall ports", Do: firewallStep(s), Skip: skipIfAborted},
			{Label: "Release bucket key", Do: releaseKeyStep, Skip: skipIfAborted},
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
			}, Skip: skipIfAbortedOrNoWait},
			// If no-wait, still restore global view.
			{Label: "Finalize", Do: unpinStoreStep(s), Skip: func(st *registry.State) bool {
				// Run on the normal no-wait path; also run on abort
				// so the store unpins before the process exits.
				if skipIfAborted(st) {
					return false
				}
				return !skipIfNoWait(st)
			}},
			// ── GitHub Actions CI setup (first-run only) ─────────
			// Gate: a single yes/no step that runs on a first-run
			// deploy (conf didn't pre-exist) of a GitHub-hosted repo,
			// and only in interactive mode. All subsequent CI steps
			// Skip unless the user opted in here, so the progress
			// view shows the individual IAM / workflow work in the
			// saga step list rather than hiding it inside one opaque
			// "Offer GitHub Actions" row.
			{Label: "Offer GitHub Actions deploy workflow",
				Do:   offerGhActionChoiceStep,
				Skip: skipOfferGhActionWithAbort(s)},
			{Label: "Detect GitHub remote", Do: detectRemoteStep,
				Skip: skipCIOrAborted(skipUnlessOptedIntoCI)},
			{Label: "Resolve GitHub token", Do: resolveGhTokenStep,
				Skip: skipCIOrAborted(skipUnlessOptedIntoCI)},
			{Label: "Fetch repo metadata", Do: fetchRepoStep,
				Skip: skipCIOrAborted(skipCIFetchRepo)},
			{Label: "Build IAM policies", Do: buildPoliciesStep(s),
				Skip: skipCIOrAborted(skipUnlessOptedIntoCI)},
			{Label: "Confirm IAM role creation", Do: confirmIAMCreateStep(s),
				Skip: skipCIOrAborted(skipCIConfirmIAM(s))},
			{Label: "Ensure OIDC provider", Do: ensureProviderStep(s),
				Skip: skipCIOrAborted(skipCIProvisionIAM)},
			{Label: "Create IAM role", Do: ensureRoleStep(s),
				Skip: skipCIOrAborted(skipCIProvisionIAM)},
			{Label: "Write workflow file", Do: writeWorkflowStep,
				Skip: skipCIOrAborted(skipCIWriteWorkflow)},
			{Label: "GitHub Actions setup complete", Do: enableSummaryStep,
				Skip: skipCIOrAborted(skipUnlessOptedIntoCI)},
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

// pickInstanceStrategyStep / createNewInstanceStep / pickExistingInstanceStep
// have been replaced by the up-front wizard + applyStrategyStep +
// createNewInstanceInlineStep. See op_deploy_wizard.go.

// skipUnlessCreatingNewInstance skips the create-new-instance step
// unless the strategy step decided "create-new".
func skipUnlessCreatingNewInstance(st *registry.State) bool {
	strategy, _ := st.Data["strategy"].(string)
	return strategy != "create-new"
}

// yesNoSuggest returns a Suggest that offers exactly two choices.
// The returned values are "false" (no) and "true" (yes). The
// descriptions should NOT repeat "Yes"/"No" — the helper adds a
// styled prefix itself. Matches the convention used by the
// monitoring / ip-address-type pickers.
func yesNoSuggest(noDesc, yesDesc string) func(context.Context) ([]registry.Choice, error) {
	return func(_ context.Context) ([]registry.Choice, error) {
		return []registry.Choice{
			{Value: "false", Display: "No   " + noDesc},
			{Value: "true", Display: "Yes  " + yesDesc},
		}, nil
	}
}

// deployPre is the op Pre hook. Runs:
//
//  1. preloadFromConf — hydrates Input from lightsail.conf.
//  2. preresolveAgentBinary — tries the local-cache / local-build
//     fallbacks (no network) to pre-populate Input["agent-path"].
//     If resolution succeeds, the wizard skips the agent-path field
//     via its already-set fast path. If nothing is found, Input is
//     left empty so the wizard's File picker runs.
//
// Under -y, both steps run; missing agent-path will still produce a
// clear saga-time error via resolveAgentStep, which is where we also
// attempt the network download.
func deployPre(ctx context.Context, in registry.Input) error {
	if err := preloadFromConf(ctx, in); err != nil {
		return err
	}
	preresolveAgentBinary(ctx, in)
	return nil
}

// preresolveAgentBinary best-effort fills Input["agent-path"] from
// non-network sources only: the per-version cache from a prior
// successful deploy, or well-known local build output paths. Keeps
// the wizard fast (no 5-minute HTTP timeout) and avoids asking the
// user for a binary when one is already sitting in their repo.
//
// If nothing matches, Input is left untouched and the wizard's
// agent-path file picker prompts. The saga's resolveAgentStep
// performs the network download as a final fallback.
func preresolveAgentBinary(ctx context.Context, in registry.Input) {
	if in.Get("agent-path") != "" {
		return // explicit flag or conf value wins
	}
	// Check the per-version cache first. If a prior deploy from the
	// same lightsailctl build already downloaded the agent, it's
	// here and we can skip the prompt.
	cacheTarget := agentfetch.CachePath(agentfetch.UserCacheDir(), internal.Version().String())
	if fi, err := os.Stat(cacheTarget); err == nil && !fi.IsDir() && fi.Size() > 0 {
		if abs, aerr := filepath.Abs(cacheTarget); aerr == nil {
			in["agent-path"] = abs
			return
		}
	}
	// Then try well-known local build output paths (goreleaser's
	// dist/ layout, cross-compile convention). Mirrors the fallback
	// list inside agentfetch.Resolve so both paths agree on what
	// counts as a valid local binary.
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	for _, rel := range []string{
		"dist/lightsailctl_linux_amd64_v1/lightsailctl",
		"lightsailctl_linux_amd64",
		"lightsailctl-linux-amd64",
	} {
		p := filepath.Join(cwd, rel)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Size() > 0 {
			if abs, aerr := filepath.Abs(p); aerr == nil {
				in["agent-path"] = abs
				return
			}
		}
	}
}

// needsAgentBinaryPrompt returns true when the agent-path wizard
// field should be shown. Only true when Pre couldn't auto-resolve,
// which is the "no local build, no cached binary, no explicit flag"
// case — the exact case where the saga would otherwise fail with
// the "could not obtain a linux/amd64 lightsailctl binary" error.
func needsAgentBinaryPrompt(in registry.Input) bool {
	return in.Get("agent-path") == ""
}

// preloadFromConf hydrates Input from lightsail.conf so an existing
// conf skips the wizard entirely. If the conf is complete the wizard
// shows nothing; if partial, the wizard opens with known fields
// prefilled.
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
	// Resume an in-progress create-new-instance draft if one was
	// saved on a prior aborted deploy. Pre-populate both the yes/no
	// strategy gate AND the namespaced __ni/* fields so the wizard
	// skips those prompts (via their When predicates reading
	// Input) and the Review preamble shows the saved draft. The
	// user still confirms at the review step.
	if p := cfg.PendingInstance; p != nil && cfg.Instance == "" {
		if in.Get("create-new-instance") == "" {
			in["create-new-instance"] = "true"
		}
		setIfEmpty(in, "__ni/name", p.Name)
		setIfEmpty(in, "__ni/region", p.Region)
		setIfEmpty(in, "__ni/blueprint-type", p.BlueprintType)
		setIfEmpty(in, "__ni/blueprint", p.Blueprint)
		setIfEmpty(in, "__ni/bundle", p.Bundle)
		setIfEmpty(in, "__ni/ip-address-type", p.IPAddressType)
		setIfEmpty(in, "__ni/user-data", p.UserData)
		setIfEmpty(in, "__ni/monitoring", p.Monitoring)
	}
	return nil
}

// setIfEmpty writes v into in[k] only when the existing value is empty.
// Preserves explicit-flag > env-var > conf precedence.
func setIfEmpty(in registry.Input, k, v string) {
	if v == "" {
		return
	}
	if in.Get(k) == "" {
		in[k] = v
	}
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
	v, _ := st.Input.Bool("no-wait")
	return v
}

// ── compound Skip helpers used by the abort-graceful path ────────────
//
// When the user declines the up-front review, abortIfDeclinedStep
// sets st.Data["aborted"]=true. Every subsequent saga step uses one
// of these compound Skip helpers to short-circuit cleanly without the
// runtime painting a red ✗.

// skipUnlessCreatingNewInstanceOrAborted combines the normal
// "only run when strategy=create-new" gate with the abort short-circuit.
func skipUnlessCreatingNewInstanceOrAborted(st *registry.State) bool {
	return skipIfAborted(st) || skipUnlessCreatingNewInstance(st)
}

// skipIfRegionSetOrAborted extends skipIfRegionSet with abort short-circuit.
func skipIfRegionSetOrAborted(st *registry.State) bool {
	return skipIfAborted(st) || skipIfRegionSet(st)
}

// skipIfAbortedOrAppExists replaces the bare skipIfAppExists on the
// bootstrap sub-steps so an abort suppresses them even when the app
// still needs creating.
func skipIfAbortedOrAppExists(st *registry.State) bool {
	return skipIfAborted(st) || skipIfAppExists(st)
}

// skipIfAbortedOrNoWait replaces the bare skipIfNoWait on the Wait-
// for-healthy step.
func skipIfAbortedOrNoWait(st *registry.State) bool {
	return skipIfAborted(st) || skipIfNoWait(st)
}

// skipOfferGhActionWithAbort wraps skipOfferGhAction so the abort
// path also suppresses the CI offer prompt.
func skipOfferGhActionWithAbort(s *store) func(st *registry.State) bool {
	inner := skipOfferGhAction(s)
	return func(st *registry.State) bool {
		return skipIfAborted(st) || inner(st)
	}
}

// skipCIOrAborted combines any per-CI-step Skip with abort.
func skipCIOrAborted(inner func(*registry.State) bool) func(*registry.State) bool {
	return func(st *registry.State) bool {
		return skipIfAborted(st) || inner(st)
	}
}

func waitStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		timeout, err := st.Input.Duration("wait-timeout")
		if err != nil {
			return fmt.Errorf("invalid --wait-timeout: %w", err)
		}
		if timeout == 0 {
			timeout = 3 * time.Minute
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
