package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/build"
	"github.com/aws/lightsailctl/pkg/iamoidc"
	"github.com/aws/lightsailctl/pkg/instance"
)

// This file holds the up-front-wizard glue for deployOp: the "__ni/*"
// Suggest closures that mirror instance.CreateFields, plus the saga
// steps that translate wizard answers into Input / State for the
// existing deploy step bodies.
//
// Keeping them in a separate file keeps op_deploy.go focused on the
// saga structure.

// niRegionSuggest proxies instance.RegionSuggest so the namespaced
// field shares the same picker behavior (live region list via the
// Lightsail client, same column alignment).
func niRegionSuggest(s *instance.Store) func(context.Context) ([]registry.Choice, error) {
	return instance.RegionSuggest(s)
}

// niBlueprintSuggestAndValidate likewise proxies the instance package's
// blueprint picker + validator, which share mutable filter state
// (bpType / platform pointers) with the surrounding wizard.
func niBlueprintSuggestAndValidate(
	s *instance.Store, bpType, platform *string,
) (func(context.Context) ([]registry.Choice, error), func(string) error) {
	return instance.BlueprintSuggestAndValidate(s, bpType, platform)
}

// niBundleSuggest proxies the bundle picker, filtered by the blueprint's
// platform captured from the blueprint-validate closure above.
func niBundleSuggest(s *instance.Store, platform *string) func(context.Context) ([]registry.Choice, error) {
	return instance.BundleSuggest(s, platform)
}

// niBlueprintTypeSuggest returns the two-option picker for the
// blueprint-type field (os vs app). Static, so it's expressed inline
// here rather than reaching into pkg/instance.
func niBlueprintTypeSuggest() func(context.Context) ([]registry.Choice, error) {
	return func(_ context.Context) ([]registry.Choice, error) {
		return []registry.Choice{
			{Value: "os", Display: "os    Base operating systems (Amazon Linux, Ubuntu, Debian, …)"},
			{Value: "app", Display: "app   Pre-configured applications (WordPress, LAMP, Node.js, …)"},
		}, nil
	}
}

// niIPTypeSuggest returns the networking-stack picker.
func niIPTypeSuggest() func(context.Context) ([]registry.Choice, error) {
	return func(_ context.Context) ([]registry.Choice, error) {
		return []registry.Choice{
			{Value: "dualstack", Display: "dualstack  IPv4 + IPv6"},
			{Value: "ipv6", Display: "ipv6       IPv6 only"},
		}, nil
	}
}

// niMonitoringSuggest returns the monitoring yes/no picker.
func niMonitoringSuggest() func(context.Context) ([]registry.Choice, error) {
	return func(_ context.Context) ([]registry.Choice, error) {
		return []registry.Choice{
			{Value: "false", Display: "No   Basic monitoring (free)"},
			{Value: "true", Display: "Yes  Detailed monitoring (additional cost)"},
		}, nil
	}
}

// deploySummaryPreamble is the PreambleFunc for the deploy-confirm
// field: renders the same grouped summary that confirmDeployStep used
// to emit via NeedInput.Reason. Called by the wizard with the input
// collected so far, so branch-specific sections only appear when
// relevant.
func deploySummaryPreamble(in registry.Input) string {
	var b strings.Builder
	b.WriteString("Review the deploy plan and confirm below.\n")

	writePreambleSection(&b, "Application", [][2]string{
		{"Name", in.Get("name")},
		{"Environment", in.Get("env")},
	})

	if asBool(in.Get("create-new-instance")) {
		writePreambleSection(&b, "Lightsail Instance (new)", [][2]string{
			{"Name", in.Get("__ni/name")},
			{"Region", in.Get("__ni/region")},
			{"Blueprint", in.Get("__ni/blueprint")},
			{"Bundle", in.Get("__ni/bundle")},
		})
	} else if v := in.Get("instance"); v != "" {
		writePreambleSection(&b, "Lightsail Instance (existing)", [][2]string{
			{"Name", v},
		})
	}

	// Build strategy: detect now from the working directory so the
	// user sees what we'll do BEFORE confirming. Same Detect() the
	// saga's packageStep will call, so they can't disagree.
	if rows := buildStrategyRows(); len(rows) > 0 {
		writePreambleSection(&b, "Build strategy", rows)
	}

	// GitHub Actions setup — only when this is a first-time deploy
	// of a GitHub-hosted repo AND the user opted in at the offer
	// prompt. Surfaces what IAM role will be created and what it
	// can do, in plain English, so the user approves it here
	// instead of being interrupted mid-saga by a second confirm.
	// The full trust + permissions JSON is emitted by the saga's
	// "Build IAM policies" step (st.Output), so it's still visible
	// post-approval.
	if asBool(in.Get("offer-gh-action")) {
		owner := in.Get("__gh-owner")
		repo := in.Get("__gh-repo")
		repoSlug := strings.TrimSpace(owner + "/" + repo)
		if repoSlug == "/" {
			repoSlug = in.Get("repo")
		}
		app := in.Get("name")
		env := in.Get("env")
		roleName := ""
		if owner != "" && repo != "" && env != "" {
			roleName = iamoidc.DefaultRoleName(owner, repo, env)
		}
		rows := [][2]string{
			{"Repository", repoSlug},
			{"IAM role", roleName},
		}
		if app != "" && env != "" {
			rows = append(rows, [2]string{
				"Role grants",
				fmt.Sprintf("deploy to %s/%s only (upload assets, tag instances, manage firewall)", app, env),
			})
		}
		rows = append(rows, [2]string{
			"Trust",
			fmt.Sprintf("limited to GitHub Actions runs from %s", repoSlug),
		})
		writePreambleSection(&b, "GitHub Actions setup", rows)
	}
	return b.String()
}

// buildStrategyRows returns the rows the deploy preamble shows in
// its "Build strategy" section. Detection runs against the cwd so the
// answer matches what packageStep will compute later. Errors and
// StrategyUnknown both yield no rows (the section is suppressed) —
// the saga step will surface a clear error if the tree is unbuildable;
// here we just don't lie to the user.
func buildStrategyRows() [][2]string {
	strategy, reason, err := build.Detect(".")
	if err != nil || strategy == build.StrategyUnknown {
		return nil
	}
	switch strategy {
	case build.StrategyCompose:
		return [][2]string{{"Strategy", "compose (" + reason + ")"}}
	case build.StrategyDockerfile:
		return [][2]string{
			{"Strategy", "Dockerfile"},
			{"Builder", "docker build"},
		}
	case build.StrategyBuildpack:
		return [][2]string{
			{"Strategy", "Cloud Native Buildpacks (" + reason + ")"},
			{"Builder", "paketobuildpacks/builder-jammy-base"},
			{"Note", "no Dockerfile needed — built on the instance"},
		}
	}
	return nil
}

// writePreambleSection is the summary helper used by
// deploySummaryPreamble. Mirrors the writeSection helper in
// op_deploy_confirm.go but doesn't depend on it so we can delete that
// file when the old confirm step is removed.
func writePreambleSection(b *strings.Builder, title string, rows [][2]string) {
	var kept [][2]string
	maxKey := 0
	for _, r := range rows {
		if strings.TrimSpace(r[1]) == "" {
			continue
		}
		kept = append(kept, r)
		if n := len(r[0]); n > maxKey {
			maxKey = n
		}
	}
	if len(kept) == 0 {
		return
	}
	fmt.Fprintf(b, "\n%s\n", title)
	for _, r := range kept {
		fmt.Fprintf(b, "  %-*s  %s\n", maxKey, r[0], r[1])
	}
}

// ── saga steps ───────────────────────────────────────────────────────

// applyStrategyStep mirrors pickInstanceStrategyStep but reads from
// the already-collected Input instead of prompting via NeedInput. It
// sets st.Data["strategy"] so the downstream saga branches work as
// before.
//
// Three cases, matching the legacy behavior:
//
//  1. --instance is set (conf or flag) → "use-existing". The wizard's
//     When predicates ensured we didn't double-ask.
//  2. --create-new-instance=true → "create-new".
//  3. Otherwise → "use-existing" (wizard forced a choice in interactive
//     mode; -y callers that passed neither flag default to existing,
//     which fails downstream if no instance is resolvable — matching
//     legacy behavior).
func applyStrategyStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		if asBool(st.Input.Get("create-new-instance")) {
			st.Data["strategy"] = "create-new"
			return nil
		}
		st.Data["strategy"] = "use-existing"
		// If the user didn't pick anything and there's no --instance,
		// fail cleanly rather than letting downstream steps produce
		// a confusing error. This is the -y-without-flags path.
		if !s.Interactive() && st.Input.Get("instance") == "" {
			return fmt.Errorf("no deployment target: pass --instance or --create-new-instance=true")
		}
		return nil
	}
}

// createNewInstanceInlineStep runs instance.CreateStep against the
// namespaced "__ni/*" answers from the up-front wizard. Replaces the
// two-phase createNewInstanceStep which used NeedInput mid-saga.
func createNewInstanceInlineStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		const nsPrefix = "__ni/"
		istore := instance.NewStore(s.region, s.regionHints)
		// Build a sub-State that de-namespaces the input so
		// instance.CreateStep sees the fields with their native names.
		sub := &registry.State{
			Input: registry.Input{},
			Data:  map[string]any{},
		}
		for k, v := range st.Input {
			if strings.HasPrefix(k, nsPrefix) {
				sub.Input[strings.TrimPrefix(k, nsPrefix)] = v
			}
		}
		if err := instance.CreateStep(istore).Do(ctx, sub); err != nil {
			return err
		}
		newName := sub.Input.Get("name")
		if newName == "" {
			return fmt.Errorf("instance created but name not captured")
		}
		st.Input["instance"] = newName
		if r := sub.Input.Get("region"); r != "" {
			st.Input["region"] = r
			pinRegion(s, r)
		}
		return nil
	}
}

// abortIfDeclinedStep gracefully short-circuits the remaining saga
// when the user answered "no" to the deploy-confirm up-front-wizard
// prompt. Rather than returning an error (which the runtime paints as
// a red ✗ failure), it sets st.Data["aborted"]=true and surfaces a
// friendly st.Output. Every subsequent saga step's Skip func honors
// the flag — except saveConfigStep, which still runs so the user's
// wizard answers are preserved in lightsail.conf for next time.
//
// Under -y the deploy-confirm field isn't present (wizard is skipped
// entirely) so this step is effectively a no-op for unattended flows.
func abortIfDeclinedStep(_ context.Context, st *registry.State) error {
	v := st.Input.Get("deploy-confirm")
	if v == "" {
		// Unset under -y, or user didn't reach the confirm field
		// (shouldn't happen in interactive mode). Treat as proceed.
		return nil
	}
	if asBool(v) {
		return nil
	}
	// Decline: short-circuit without erroring. Downstream steps
	// check st.Data["aborted"] in their Skip funcs.
	st.Data["aborted"] = true
	st.Output = "Deploy aborted. Saved your choices to lightsail.conf so " +
		"you can re-run `lightsailctl deploy` later without re-answering."
	return nil
}

// skipIfAborted is the shared Skip function for every post-abort
// saga step. Returns true once abortIfDeclinedStep has flagged the
// decline, keeping the rest of the deploy from running without
// marking it failed.
func skipIfAborted(st *registry.State) bool {
	aborted, _ := st.Data["aborted"].(bool)
	return aborted
}
