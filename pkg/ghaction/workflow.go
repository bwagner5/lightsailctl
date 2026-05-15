// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package ghaction

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// WorkflowInput gathers the values the deploy-workflow template renders.
// Every one is a plain public identifier; nothing secret lives here.
type WorkflowInput struct {
	// App, Env, Region are the deploy target — rendered as hardcoded
	// flags on the generated `lightsailctl deploy` line so the
	// workflow is self-contained (no GitHub vars / secrets needed).
	App    string
	Env    string
	Region string
	// RoleARN is what the job passes to
	// aws-actions/configure-aws-credentials via role-to-assume.
	RoleARN string
	// Branch is the branch that triggers the workflow. Typically "main".
	Branch string
}

// WorkflowRelPath is the canonical relative path where the workflow
// file lands. Rendered relative to the repo root so callers don't have
// to repeat it.
const WorkflowRelPath = ".github/workflows/lightsail-deploy.yml"

// ghExprSentinel is substituted back into a literal `${{ github.run_id }}`
// after Go-template rendering. GitHub Actions uses ${{ ... }} which
// collides with text/template's {{ ... }} delimiters; rather than pick
// exotic delimiters we emit a sentinel inside the template and swap it
// post-render. Kept as an unexported constant so it can't accidentally
// appear in user input.
const ghExprSentinel = "__GH_RUN_ID_EXPR__"

// workflowTemplate is the source of `.github/workflows/lightsail-deploy.yml`.
//
// Design decisions (see plan.md "The generated workflow file"):
//   - permissions: is job-level so other jobs added later can't
//     inadvertently inherit id-token: write.
//   - concurrency group name is hard-coded at render time
//     (lightsail-deploy-<app>-<env>) so the YAML is self-contained.
//   - `permissions.contents: read` is required by actions/checkout.
//   - The install step pulls from the aws/lightsailctl releases "latest"
//     path — the release pipeline already publishes that URL. If the
//     CLI's default-agent-autofetch behavior lands as a preceding PR
//     per the plan, `lightsailctl deploy` here needs no --agent-path.
const workflowTemplate = `name: Lightsail deploy

on:
  push:
    branches: [{{.Branch}}]
  workflow_dispatch:

permissions:
  id-token: write      # request GitHub OIDC JWT
  contents: read       # checkout the repo

concurrency:
  group: lightsail-deploy-{{.App}}-{{.Env}}
  cancel-in-progress: false

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Configure AWS credentials via OIDC
        uses: aws-actions/configure-aws-credentials@v4
        with:
          role-to-assume: {{.RoleARN}}
          aws-region: {{.Region}}
          role-session-name: lightsailctl-deploy-` + ghExprSentinel + `

      - name: Install lightsailctl
        run: |
          curl -sL https://github.com/aws/lightsailctl/releases/latest/download/lightsailctl_linux_amd64.tar.gz \
            | tar xz lightsailctl
          sudo mv lightsailctl /usr/local/bin/lightsailctl
          lightsailctl --version

      - name: Deploy
        run: |
          lightsailctl deploy \
            --name {{.App}} \
            --env  {{.Env}} \
            --region {{.Region}} \
            -y
`

// RenderWorkflow renders the workflow YAML. The output is
// deterministic: same input → byte-identical output.
func RenderWorkflow(in WorkflowInput) (string, error) {
	if err := validateWorkflowInput(in); err != nil {
		return "", err
	}
	first, err := renderOnce(in)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(first, ghExprSentinel, "${{ github.run_id }}"), nil
}

func renderOnce(in WorkflowInput) (string, error) {
	t, err := template.New("wf").Parse(workflowTemplate)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, in); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func validateWorkflowInput(in WorkflowInput) error {
	missing := []string{}
	if in.App == "" {
		missing = append(missing, "App")
	}
	if in.Env == "" {
		missing = append(missing, "Env")
	}
	if in.Region == "" {
		missing = append(missing, "Region")
	}
	if in.RoleARN == "" {
		missing = append(missing, "RoleARN")
	}
	if in.Branch == "" {
		missing = append(missing, "Branch")
	}
	if len(missing) > 0 {
		return fmt.Errorf("workflow input missing: %s", strings.Join(missing, ", "))
	}
	return nil
}

// WriteWorkflow writes the rendered YAML to
// <repoRoot>/.github/workflows/lightsail-deploy.yml, creating parent
// directories as needed. Returns the absolute path written. If a file
// already exists at that path and its contents differ from rendered,
// the caller receives an ExistsError so the interactive flow can show
// a diff + "overwrite?" prompt.
func WriteWorkflow(repoRoot string, in WorkflowInput, overwrite bool) (string, error) {
	content, err := RenderWorkflow(in)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(filepath.Join(repoRoot, WorkflowRelPath))
	if err != nil {
		return "", err
	}
	// Fail-on-exists unless overwrite=true.
	if !overwrite {
		if existing, rerr := os.ReadFile(abs); rerr == nil {
			if string(existing) == content {
				// Identical — nothing to do.
				return abs, nil
			}
			return abs, &ExistsError{Path: abs, Existing: string(existing), Rendered: content}
		}
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return "", err
	}
	return abs, nil
}

// ExistsError reports that the workflow file already exists on disk and
// differs from the rendered content. Callers show the diff and decide
// whether to overwrite.
type ExistsError struct {
	Path               string
	Existing, Rendered string
}

func (e *ExistsError) Error() string {
	return "workflow file already exists at " + e.Path + " (differs from rendered)"
}
