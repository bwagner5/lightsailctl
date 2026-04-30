package app

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/config"
	"github.com/aws/lightsailctl/pkg/ghaction"
	"github.com/aws/lightsailctl/pkg/iamoidc"
	"github.com/aws/lightsailctl/pkg/lightsail"
	"github.com/aws/lightsailctl/pkg/names"
)

// ── enable-gh-action ──────────────────────────────────────────────────
//
// `lightsailctl app enable-gh-action` (also invoked from the first-run
// path in deploy) is the CI-setup wizard. It:
//
//   1. Detects the git remote / owner / repo.
//   2. Picks a GitHub token source and fetches repo metadata.
//   3. Builds the trust + permissions policy docs and shows them.
//   4. Creates (or reconciles) the OIDC provider and role.
//   5. Renders .github/workflows/lightsail-deploy.yml on disk.
//
// Every mutating step is idempotent and skippable. Teardown lives in
// disableGhActionOp and mirrors this flow.

func enableGhActionOp(s *store, suggest func(context.Context) ([]registry.Choice, error)) registry.Operation {
	return registry.Operation{
		Name: "enable-gh-action", Aliases: []string{"enable-ci"}, Key: "G",
		Short: "set up a GitHub Actions deploy workflow for this app",
		Fields: []registry.Field{
			{Flag: "name", Short: "n", Help: "app name",
				Prefill: names.DefaultAppName, Required: true, Suggest: suggest},
			{Flag: "env", Short: "e", Help: "environment",
				Default: "dev", Required: true},
			{Flag: "region", Help: "AWS region the workflow deploys to",
				Wizard: registry.BoolPtr(false)},
			{Flag: "repo", Help: "<owner>/<repo>, auto-detected from git remote when unset",
				Wizard: registry.BoolPtr(false)},
			{Flag: "branch", Help: "branch that triggers deploys", Default: "main",
				Wizard: registry.BoolPtr(false)},
			{Flag: "role-name", Help: "IAM role name",
				Wizard: registry.BoolPtr(false)},
			{Flag: "github-auth", Help: "how to obtain a GitHub token (auto|gh|token|device|none)",
				Default: "auto", Wizard: registry.BoolPtr(false)},
			{Flag: "github-token", Help: "GitHub PAT (only read when --github-auth=token)",
				Sensitive: true, Wizard: registry.BoolPtr(false)},
			{Flag: "skip-role-create", Help: "use an existing role ARN instead of creating one",
				Default: "false", Kind: registry.KindBool, Wizard: registry.BoolPtr(false)},
			{Flag: "role-arn", Help: "required when --skip-role-create is set",
				Wizard: registry.BoolPtr(false)},
			{Flag: "skip-workflow", Help: "don't write the workflow file (print it instead)",
				Default: "false", Kind: registry.KindBool, Wizard: registry.BoolPtr(false)},
			{Flag: "dry-run", Help: "print every resource, make no changes",
				Default: "false", Kind: registry.KindBool, Wizard: registry.BoolPtr(false)},
		},
		Pre: preloadGhActionFromConf,
		Steps: []registry.Step{
			{Label: "Detect git remote", Do: detectRemoteStep},
			{Label: "Resolve GitHub token", Do: resolveGhTokenStep},
			{Label: "Fetch repo metadata", Do: fetchRepoStep, Skip: skipIfDetachedFromGitHub},
			{Label: "Build IAM policies", Do: buildPoliciesStep(s)},
			{Label: "Ensure OIDC provider", Do: ensureProviderStep(s), Skip: skipRoleProvisioning},
			{Label: "Ensure role + inline policy", Do: ensureRoleStep(s), Skip: skipRoleProvisioning},
			{Label: "Write workflow file", Do: writeWorkflowStep, Skip: skipIfSkipWorkflow},
			{Label: "Summary", Do: enableSummaryStep},
		},
	}
}

// ── disable-gh-action ────────────────────────────────────────────────

func disableGhActionOp(s *store, suggest func(context.Context) ([]registry.Choice, error)) registry.Operation {
	return registry.Operation{
		Name: "disable-gh-action", Aliases: []string{"disable-ci"}, Key: "shift+G",
		Short:   "remove the GitHub Actions deploy workflow and its IAM role",
		Confirm: "Delete the CI IAM role and local workflow file?",
		Fields: []registry.Field{
			{Flag: "name", Short: "n", Help: "app name",
				Prefill: names.DefaultAppName, Required: true, Suggest: suggest},
			{Flag: "env", Short: "e", Help: "environment",
				Default: "dev", Required: true},
			{Flag: "repo", Help: "<owner>/<repo>, auto-detected from git remote when unset",
				Wizard: registry.BoolPtr(false)},
			{Flag: "role-name", Help: "IAM role name (defaults to the enable-gh-action default)",
				Wizard: registry.BoolPtr(false)},
			{Flag: "skip-workflow", Help: "don't remove the local workflow file",
				Default: "false", Kind: registry.KindBool, Wizard: registry.BoolPtr(false)},
		},
		Pre: preloadGhActionFromConf,
		Steps: []registry.Step{
			{Label: "Detect git remote", Do: detectRemoteStep},
			{Label: "Resolve role name", Do: resolveRoleNameForDisableStep},
			{Label: "Delete IAM role", Do: deleteRoleStep(s)},
			{Label: "Remove workflow file", Do: removeWorkflowStep, Skip: skipIfSkipWorkflow},
		},
	}
}

// ── shared state keys stashed in registry.State.Data ─────────────────
//
//   "gh.owner"         string         — repo owner
//   "gh.repo"          string         — repo name
//   "gh.repo_id"       string         — GitHub numeric repo ID
//   "gh.private"       bool           — informational
//   "gh.token_src"     string         — "token"/"gh"/"device"/"none" (for disclosure)
//   "gh.user"          string         — GitHub login, if known
//   "iam.trust"        string         — JSON trust policy
//   "iam.perm"         string         — JSON permissions policy
//   "iam.role_arn"     string         — final role ARN
//   "iam.role_name"    string         — role name (for disable)
//   "iam.provider_arn" string         — OIDC provider ARN
//   "gh.wf_path"       string         — absolute path of written workflow
//
// ─────────────────────────────────────────────────────────────────────

// preloadGhActionFromConf hydrates Input from lightsail.conf so a repo
// with a working deploy doesn't re-prompt for app/env/region when the
// user opts in to CI.
func preloadGhActionFromConf(_ context.Context, in registry.Input) error {
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
	return nil
}

// detectRemoteStep fills in --repo from the local git remote when it
// wasn't explicitly supplied. Non-github.com remotes produce a clear
// error (GHES is out of scope).
func detectRemoteStep(_ context.Context, st *registry.State) error {
	// Explicit --repo wins.
	if v := st.Input.Get("repo"); v != "" {
		parts := strings.SplitN(v, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return fmt.Errorf("--repo must be <owner>/<repo>, got %q", v)
		}
		st.Data["gh.owner"] = parts[0]
		st.Data["gh.repo"] = strings.TrimSuffix(parts[1], ".git")
		return nil
	}
	cwd, _ := os.Getwd()
	raw, _ := ghaction.DetectRemoteURL(cwd)
	if raw == "" {
		return fmt.Errorf("no git remote origin found; pass --repo <owner>/<repo>")
	}
	ref, err := ghaction.ParseRemoteURL(raw)
	if err != nil {
		return err
	}
	st.Data["gh.owner"] = ref.Owner
	st.Data["gh.repo"] = ref.Repo
	st.Input["repo"] = ref.String()
	return nil
}

// resolveGhTokenStep picks the GitHub token source per --github-auth.
// In non-interactive mode the auto path is token/env/gh only; we do
// NOT start a device flow without a terminal.
func resolveGhTokenStep(ctx context.Context, st *registry.State) error {
	mode := ghaction.AuthMode(strings.TrimSpace(st.Input.Get("github-auth")))
	if mode == "" {
		mode = ghaction.AuthAuto
	}
	flagTok := st.Input.Get("github-token")
	envTok := os.Getenv("GITHUB_TOKEN")

	// No PAT-paste or device-flow prompt here — both require a TTY.
	// The wizard offers those flows via --github-auth=device explicitly.
	res, err := ghaction.ResolveToken(ctx, mode, flagTok, envTok, nil, nil)
	if err != nil {
		return err
	}
	st.Data["gh.token_src"] = string(res.Source)
	if res.Token != "" {
		st.Data["gh.token"] = res.Token
	}
	if res.UserHint != "" {
		st.Data["gh.user"] = res.UserHint
	}
	return nil
}

// skipIfDetachedFromGitHub is true when --github-auth=none: the caller
// supplied enough info on the CLI that we don't need to call GitHub.
func skipIfDetachedFromGitHub(st *registry.State) bool {
	return strings.EqualFold(st.Input.Get("github-auth"), "none")
}

// fetchRepoStep hits GET /repos/{owner}/{repo} via go-github.
// Surfaces repo_id + private. The token was stashed by resolveGhTokenStep.
func fetchRepoStep(ctx context.Context, st *registry.State) error {
	owner, _ := st.Data["gh.owner"].(string)
	repo, _ := st.Data["gh.repo"].(string)
	token, _ := st.Data["gh.token"].(string)
	md, err := ghaction.FetchRepoMetadata(ctx, token, owner, repo)
	if err != nil {
		return err
	}
	st.Data["gh.repo_id"] = md.RepositoryID
	st.Data["gh.private"] = md.Private
	return nil
}

// buildPoliciesStep composes the trust + permissions policy documents.
// Emits both as pretty JSON via st.Output so the saga UI shows them
// ahead of the (potentially mutating) provider/role steps.
func buildPoliciesStep(s *store) func(context.Context, *registry.State) error {
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

		region := st.Input.Get("region")
		if region == "" {
			return fmt.Errorf("--region is required (set in lightsail.conf or pass --region)")
		}

		// Resolve deploy targets via the app's tagged instances so the
		// firewall statement is narrowly scoped. Best-effort — no
		// instances (yet) is fine for a pre-deploy enable run; the
		// firewall statement is just omitted.
		var targets []iamoidc.TargetInstance
		insts, _ := c.FindTargetsForAppEnv(ctx, st.Input.Get("name"), st.Input.Get("env"))
		for _, inst := range insts {
			targets = append(targets, iamoidc.TargetInstance{
				Name:   inst.Name,
				Region: inst.Region,
			})
		}

		owner, _ := st.Data["gh.owner"].(string)
		repo, _ := st.Data["gh.repo"].(string)
		repoID, _ := st.Data["gh.repo_id"].(string)

		trust, err := iamoidc.BuildTrustPolicy(iamoidc.TrustPolicyInput{
			AccountID:    acct,
			Owner:        owner,
			Repo:         repo,
			RepositoryID: repoID,
			Branch:       st.Input.Get("branch"),
		})
		if err != nil {
			return err
		}
		perm, err := iamoidc.BuildPermissionsPolicy(iamoidc.PermissionsPolicyInput{
			AccountID: acct,
			App:       st.Input.Get("name"),
			Env:       st.Input.Get("env"),
			Region:    region,
			Targets:   targets,
		})
		if err != nil {
			return err
		}
		st.Data["iam.trust"] = trust
		st.Data["iam.perm"] = perm

		// Resolve role name now so downstream steps and the summary agree.
		roleName := st.Input.Get("role-name")
		if roleName == "" {
			roleName = iamoidc.DefaultRoleName(owner, repo, st.Input.Get("env"))
			st.Input["role-name"] = roleName
		}
		st.Data["iam.role_name"] = roleName

		// Clear output — the policies are shown by the IAM confirm
		// step (confirmIAMCreateStep), which has the additional
		// context of the account ID and role name. Duplicating the
		// JSON blob here would crowd the saga step list.
		st.Output = fmt.Sprintf("Policies built for role %s", roleName)
		return nil
	}
}

// skipRoleProvisioning is true when --skip-role-create=true OR
// --dry-run=true. Also true when the caller supplied an explicit
// --role-arn (they've already provisioned the role out-of-band).
func skipRoleProvisioning(st *registry.State) bool {
	if inputBool(st.Input, "dry-run") {
		return true
	}
	if inputBool(st.Input, "skip-role-create") {
		return true
	}
	return false
}

// ensureProviderStep ensures the GitHub OIDC provider exists. Stashes
// the ARN on state for the summary.
func ensureProviderStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		iamClient := iamoidc.NewIAMClient(c.Config())
		p := iamoidc.Provisioner{IAM: iamClient}
		acct, _ := st.Data["acct"].(string)
		arn, reused, err := p.EnsureOIDCProvider(ctx, acct)
		if err != nil {
			return err
		}
		st.Data["iam.provider_arn"] = arn
		if reused {
			st.Output = "Reused existing GitHub OIDC provider\nARN: " + arn
		} else {
			st.Output = "Created GitHub OIDC provider\nARN: " + arn
		}
		return nil
	}
}

// ensureRoleStep creates or reconciles the role and writes the inline
// permissions policy.
func ensureRoleStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		iamClient := iamoidc.NewIAMClient(c.Config())
		p := iamoidc.Provisioner{IAM: iamClient}

		trust, _ := st.Data["iam.trust"].(string)
		perm, _ := st.Data["iam.perm"].(string)
		roleName, _ := st.Data["iam.role_name"].(string)
		owner, _ := st.Data["gh.owner"].(string)
		repo, _ := st.Data["gh.repo"].(string)

		res, err := p.EnsureRole(ctx, iamoidc.RoleSpec{
			Name:              roleName,
			TrustPolicy:       trust,
			PermissionsPolicy: perm,
			Description:       fmt.Sprintf("CI deploy role for %s/%s (%s/%s)", owner, repo, st.Input.Get("name"), st.Input.Get("env")),
			Tags: map[string]string{
				lightsail.VersionTagKey: lightsail.CLIVersion(),
				"lightsailctl:app":      st.Input.Get("name"),
				"lightsailctl:env":      st.Input.Get("env"),
				"lightsailctl:repo":     owner + "/" + repo,
			},
		})
		if err != nil {
			return err
		}
		st.Data["iam.role_arn"] = res.ARN
		var action string
		if res.Created {
			action = "Created"
		} else {
			action = "Reconciled"
		}
		st.Output = action + " role\n" +
			"ARN: " + res.ARN + "\n" +
			"Inline policy: " + iamoidc.InlinePolicyName
		return nil
	}
}

// writeWorkflowStep renders and writes the .github/workflows/... file.
// In --dry-run mode the content is printed but no file is written.
// If a differing file already exists we surface an ExistsError; in
// non-interactive mode this fails the step (safe default: never
// clobber a user's workflow file without their say-so).
func writeWorkflowStep(ctx context.Context, st *registry.State) error {
	// Resolve the role ARN: either from our own create step or from
	// --role-arn if --skip-role-create was used.
	roleARN, _ := st.Data["iam.role_arn"].(string)
	if roleARN == "" {
		roleARN = st.Input.Get("role-arn")
	}
	if roleARN == "" {
		// --dry-run case: the role wasn't provisioned but we still
		// want to show the workflow. Synthesize the "expected" ARN.
		acct, _ := st.Data["acct"].(string)
		roleName, _ := st.Data["iam.role_name"].(string)
		if acct != "" && roleName != "" {
			roleARN = fmt.Sprintf("arn:aws:iam::%s:role/%s", acct, roleName)
		}
	}

	in := ghaction.WorkflowInput{
		App:     st.Input.Get("name"),
		Env:     st.Input.Get("env"),
		Region:  st.Input.Get("region"),
		RoleARN: roleARN,
		Branch:  firstNonEmpty(st.Input.Get("branch"), "main"),
	}

	if inputBool(st.Input, "dry-run") {
		content, err := ghaction.RenderWorkflow(in)
		if err != nil {
			return err
		}
		st.Output = "Workflow (dry-run, not written):\n" + content
		return nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	path, werr := ghaction.WriteWorkflow(cwd, in, false)
	if werr != nil {
		// If it's the ExistsError and differs from rendered, surface
		// a clear prompt-style error. Re-run with --skip-workflow or
		// delete the file to proceed.
		var exErr *ghaction.ExistsError
		if asExists(werr, &exErr) {
			return fmt.Errorf("%s\nre-run with --skip-workflow=true to keep the existing file, "+
				"or delete it to overwrite", werr.Error())
		}
		return werr
	}
	st.Data["gh.wf_path"] = path
	st.Output = "Wrote " + path
	return nil
}

// asExists is a local generic errors.As so we don't pull in a new import.
func asExists(err error, target **ghaction.ExistsError) bool {
	for err != nil {
		if x, ok := err.(*ghaction.ExistsError); ok {
			*target = x
			return true
		}
		type wrap interface{ Unwrap() error }
		if w, ok := err.(wrap); ok {
			err = w.Unwrap()
			continue
		}
		return false
	}
	return false
}

// skipIfSkipWorkflow returns true when --skip-workflow=true.
func skipIfSkipWorkflow(st *registry.State) bool {
	return inputBool(st.Input, "skip-workflow")
}

// enableSummaryStep emits the final "Done" message with next-step
// instructions. Pure output — no state changes.
func enableSummaryStep(_ context.Context, st *registry.State) error {
	var b strings.Builder
	b.WriteString("GitHub Actions deploy ready.\n\n")
	if path, ok := st.Data["gh.wf_path"].(string); ok {
		fmt.Fprintf(&b, "Commit and push:\n  git add %s\n  git commit -m \"ci: add Lightsail deploy workflow\"\n  git push\n", path)
	} else if inputBool(st.Input, "dry-run") {
		b.WriteString("Dry-run complete — no resources were created.\n")
	}
	st.Output = b.String()
	return nil
}

// ── disable helpers ──────────────────────────────────────────────────

// resolveRoleNameForDisableStep figures out which IAM role to delete.
// Preference order: --role-name flag, DefaultRoleName(owner,repo,env).
func resolveRoleNameForDisableStep(_ context.Context, st *registry.State) error {
	owner, _ := st.Data["gh.owner"].(string)
	repo, _ := st.Data["gh.repo"].(string)
	roleName := st.Input.Get("role-name")
	if roleName == "" {
		if owner == "" || repo == "" {
			return fmt.Errorf("cannot derive role name without --repo or git remote; pass --role-name")
		}
		roleName = iamoidc.DefaultRoleName(owner, repo, st.Input.Get("env"))
	}
	st.Data["iam.role_name"] = roleName
	st.Output = "Will delete IAM role: " + roleName
	return nil
}

// deleteRoleStep detaches the inline policy and deletes the role.
// Does NOT touch the shared OIDC provider.
func deleteRoleStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		iamClient := iamoidc.NewIAMClient(c.Config())
		p := iamoidc.Provisioner{IAM: iamClient}
		roleName, _ := st.Data["iam.role_name"].(string)
		if err := p.DeleteRole(ctx, roleName); err != nil {
			return err
		}
		st.Output = "Deleted IAM role: " + roleName
		return nil
	}
}

// removeWorkflowStep deletes .github/workflows/lightsail-deploy.yml
// if present. Missing = success.
func removeWorkflowStep(_ context.Context, st *registry.State) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	path := cwd + "/" + ghaction.WorkflowRelPath
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	st.Output = "Removed " + path
	return nil
}

// ── offer-CI tail step (invoked from op_deploy) ──────────────────────
//
// The deploy saga appends this as its final step. It runs only on a
// first-run bootstrap deploy (conf didn't pre-exist) of a GitHub-hosted
// repo, and only in interactive mode. The plan forbids silently
// provisioning IAM under -y.

// skipOfferGhAction returns true when the offer-CI tail step should
// not run. The gate is strict: any of the following → skip.
//
//  1. non-interactive (-y): we don't prompt, and silently
//     provisioning IAM against the user's account without explicit
//     opt-in is a non-starter.
//  2. lightsail.conf pre-existed: this isn't the first-run path.
//  3. no GitHub remote: the workflow generator is github.com-only.
func skipOfferGhAction(s *store) func(st *registry.State) bool {
	return func(st *registry.State) bool {
		if !s.Interactive() {
			return true
		}
		existed, _ := st.Data["conf_existed"].(bool)
		if existed {
			return true
		}
		cwd, err := os.Getwd()
		if err != nil {
			return true
		}
		raw, _ := ghaction.DetectRemoteURL(cwd)
		if raw == "" {
			return true
		}
		if _, perr := ghaction.ParseRemoteURL(raw); perr != nil {
			return true
		}
		return false
	}
}

// offerGhActionChoiceStep is the single yes/no gate: asks the user
// whether to set up a GitHub Actions workflow. Records the answer in
// Input["offer-gh-action"]. Subsequent CI steps read that answer via
// skipUnlessOptedIntoCI so the deploy saga shows their individual
// progress rows in the live view.
func offerGhActionChoiceStep(_ context.Context, st *registry.State) error {
	if st.Input.Get("offer-gh-action") != "" {
		// Already answered (e.g. by flag or by an earlier round).
		if !inputBool(st.Input, "offer-gh-action") {
			st.Output = "Skipped GitHub Actions setup. Run " +
				"`lightsailctl app enable-gh-action` later to set it up."
		}
		return nil
	}
	return &registry.NeedInput{
		Reason: "Set up a GitHub Actions workflow so pushes auto-deploy?",
		Fields: []registry.Field{
			{Flag: "offer-gh-action", Required: true,
				Help: "opt in to CI setup", Kind: registry.KindBool,
				Suggest: yesNoSuggest(
					"I'll do this later",
					"walk me through it"),
			},
		},
	}
}

// skipUnlessOptedIntoCI returns true when the user declined CI setup
// or never reached the offer step. All GitHub-Actions work steps use
// this as their base skip condition.
func skipUnlessOptedIntoCI(st *registry.State) bool {
	return !inputBool(st.Input, "offer-gh-action")
}

// skipCIFetchRepo skips the repo-metadata fetch when the user declined
// CI, or when --github-auth=none means they've supplied repo info
// themselves and we should make no GitHub API calls.
func skipCIFetchRepo(st *registry.State) bool {
	if skipUnlessOptedIntoCI(st) {
		return true
	}
	return strings.EqualFold(st.Input.Get("github-auth"), "none")
}

// skipCIConfirmIAM skips the IAM confirmation when we're not opted in
// OR when the caller already passed --iam-confirm (e.g. on a re-run).
// Under -y, the confirmation is inapplicable — but we never reach
// this path because offerGhActionChoiceStep itself is skipped under -y.
func skipCIConfirmIAM(s *store) func(*registry.State) bool {
	return func(st *registry.State) bool {
		if skipUnlessOptedIntoCI(st) {
			return true
		}
		// Already answered (e.g. because user retried the saga after
		// a transient failure and flagged --iam-confirm=true): skip.
		if st.Input.Get("iam-confirm") != "" {
			return true
		}
		// Non-interactive mode: no confirm prompt possible; proceed
		// silently. A -y caller who reached the CI branch is the
		// offer-gh-action flag path.
		if !s.Interactive() {
			return true
		}
		return false
	}
}

// skipCIProvisionIAM skips the actual IAM mutation steps when the
// user declined the IAM confirm, when --skip-role-create=true, or
// when --dry-run=true.
func skipCIProvisionIAM(st *registry.State) bool {
	if skipUnlessOptedIntoCI(st) {
		return true
	}
	if st.Input.Get("iam-confirm") != "" && !inputBool(st.Input, "iam-confirm") {
		return true
	}
	return skipRoleProvisioning(st)
}

// skipCIWriteWorkflow skips the workflow-file write when opted out of
// CI, when --skip-workflow=true, OR when the IAM confirm was declined.
// We never write the workflow if the role wasn't provisioned — the
// generated YAML would reference a non-existent role.
func skipCIWriteWorkflow(st *registry.State) bool {
	if skipUnlessOptedIntoCI(st) {
		return true
	}
	if st.Input.Get("iam-confirm") != "" && !inputBool(st.Input, "iam-confirm") {
		return true
	}
	return skipIfSkipWorkflow(st)
}

// confirmIAMCreateStep shows the fully rendered trust + permissions
// policies, the role name, and the AWS account, and requires explicit
// confirmation before creating anything. The policies are built by
// the preceding buildPoliciesStep and stashed on st.Data.
//
// The step writes the answer to Input["iam-confirm"]; skipCIProvisionIAM
// checks that before running the mutation steps.
func confirmIAMCreateStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		if st.Input.Get("iam-confirm") != "" {
			if !inputBool(st.Input, "iam-confirm") {
				return fmt.Errorf("IAM role creation declined by user")
			}
			return nil
		}
		acct, _ := st.Data["acct"].(string)
		roleName, _ := st.Data["iam.role_name"].(string)
		trust, _ := st.Data["iam.trust"].(string)
		perm, _ := st.Data["iam.perm"].(string)

		var b strings.Builder
		fmt.Fprintf(&b, "The following IAM role will be created in AWS account %s:\n\n", acct)
		fmt.Fprintf(&b, "  Role name   %s\n", roleName)
		fmt.Fprintf(&b, "  Role ARN    arn:aws:iam::%s:role/%s\n\n", acct, roleName)
		b.WriteString("Trust policy (who can assume the role):\n")
		b.WriteString(indent(trust, "  "))
		b.WriteString("\n\nPermissions policy (what the role can do):\n")
		b.WriteString(indent(perm, "  "))
		b.WriteString("\n")

		return &registry.NeedInput{
			Reason: b.String(),
			Fields: []registry.Field{
				{Flag: "iam-confirm", Required: true,
					Help: "create this IAM role + trust policy", Kind: registry.KindBool,
					// Default to Yes: the user already opted into CI
					// setup at the previous prompt; this confirmation
					// just surfaces the policies for review.
					Default: "true",
					Suggest: yesNoSuggest(
						"show me the JSON and stop here",
						"create the role"),
				},
			},
		}
	}
}

// indent prefixes every line of s with prefix.
func indent(s, prefix string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	return strings.Join(lines, "\n")
}

// asBool is a lenient bool parser used on Input values. Treats the
// common "yes/1/true" family as true; everything else (including empty)
// as false. Matches the existing skipIfNoWait helper's philosophy.
func asBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "y":
		return true
	}
	return false
}

func inputBool(in registry.Input, key string) bool {
	v, err := in.Bool(key)
	if err == nil {
		return v
	}
	return asBool(in.Get(key))
}

// Unused plumbing reference so `lightsail` import isn't culled if
// a future refactor removes the only call site. Safe no-op.
var _ = lightsail.BucketPrefix
