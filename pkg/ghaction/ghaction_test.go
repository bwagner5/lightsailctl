// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package ghaction

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRemoteURL_Table(t *testing.T) {
	cases := []struct {
		name      string
		url       string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		// HTTPS
		{"https .git", "https://github.com/alice/hello.git", "alice", "hello", false},
		{"https no .git", "https://github.com/alice/hello", "alice", "hello", false},
		{"https trailing slash", "https://github.com/alice/hello/", "alice", "hello", false},
		{"http allowed", "http://github.com/alice/hello", "alice", "hello", false},

		// SCP / SSH
		{"scp form", "git@github.com:alice/hello.git", "alice", "hello", false},
		{"scp form no .git", "git@github.com:alice/hello", "alice", "hello", false},
		{"ssh:// form", "ssh://git@github.com/alice/hello.git", "alice", "hello", false},

		// Hyphenated and org names
		{"dashed owner", "git@github.com:amazon-web-services/lightsail.git", "amazon-web-services", "lightsail", false},

		// Rejected: GHES, non-github.
		{"ghes https rejected", "https://github.example.com/alice/hello.git", "", "", true},
		{"gitlab rejected", "git@gitlab.com:alice/hello.git", "", "", true},
		{"sourcehut rejected", "https://git.sr.ht/~alice/hello", "", "", true},

		// Malformed
		{"empty", "", "", "", true},
		{"garbage", "not a url at all", "", "", true},
		{"no path", "https://github.com", "", "", true},
		{"one segment", "https://github.com/alice", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := ParseRemoteURL(tc.url)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", ref)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if ref.Owner != tc.wantOwner || ref.Repo != tc.wantRepo {
				t.Errorf("got %s/%s; want %s/%s",
					ref.Owner, ref.Repo, tc.wantOwner, tc.wantRepo)
			}
			if ref.Host != "github.com" {
				t.Errorf("Host = %q; want github.com", ref.Host)
			}
		})
	}
}

func TestRepoRef_String(t *testing.T) {
	r := RepoRef{Owner: "alice", Repo: "hello", Host: "github.com"}
	if r.String() != "alice/hello" {
		t.Errorf("String() = %q", r.String())
	}
}

func TestRenderWorkflow_Golden(t *testing.T) {
	in := WorkflowInput{
		App:     "hello",
		Env:     "dev",
		Region:  "us-east-2",
		RoleARN: "arn:aws:iam::111111111111:role/lightsailctl-deploy-alice-hello-dev",
		Branch:  "main",
	}
	out, err := RenderWorkflow(in)
	if err != nil {
		t.Fatalf("RenderWorkflow: %v", err)
	}

	// Every important substitution appears literally — no placeholders
	// left behind.
	mustContain := []string{
		"name: Lightsail deploy",
		"branches: [main]",
		"id-token: write",
		"contents: read",
		"group: lightsail-deploy-hello-dev",
		"cancel-in-progress: false",
		"role-to-assume: arn:aws:iam::111111111111:role/lightsailctl-deploy-alice-hello-dev",
		"aws-region: us-east-2",
		"role-session-name: lightsailctl-deploy-${{ github.run_id }}",
		"curl -sL https://github.com/aws/lightsailctl/releases/latest/download/lightsailctl_linux_amd64.tar.gz",
		"lightsailctl deploy",
		"--name hello",
		"--env  dev",
		"--region us-east-2",
		"-y",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Errorf("rendered workflow missing %q:\n%s", want, out)
		}
	}
	// And the sentinel should NOT leak to output.
	if strings.Contains(out, ghExprSentinel) {
		t.Errorf("sentinel leaked into output:\n%s", out)
	}
}

func TestRenderWorkflow_Deterministic(t *testing.T) {
	in := WorkflowInput{
		App: "hello", Env: "dev", Region: "us-east-2",
		RoleARN: "arn:aws:iam::1:role/r", Branch: "main",
	}
	a, _ := RenderWorkflow(in)
	b, _ := RenderWorkflow(in)
	if a != b {
		t.Errorf("non-deterministic render:\n--- a:\n%s\n--- b:\n%s", a, b)
	}
}

func TestRenderWorkflow_MissingFields(t *testing.T) {
	cases := []struct {
		name string
		in   WorkflowInput
	}{
		{"no app", WorkflowInput{Env: "e", Region: "r", RoleARN: "a", Branch: "b"}},
		{"no env", WorkflowInput{App: "a", Region: "r", RoleARN: "a", Branch: "b"}},
		{"no region", WorkflowInput{App: "a", Env: "e", RoleARN: "a", Branch: "b"}},
		{"no roleARN", WorkflowInput{App: "a", Env: "e", Region: "r", Branch: "b"}},
		{"no branch", WorkflowInput{App: "a", Env: "e", Region: "r", RoleARN: "a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := RenderWorkflow(tc.in); err == nil {
				t.Errorf("want error, got nil")
			}
		})
	}
}

func TestWriteWorkflow_CreatesDirs(t *testing.T) {
	dir := t.TempDir()
	in := WorkflowInput{App: "hello", Env: "dev", Region: "us-east-2",
		RoleARN: "arn:aws:iam::1:role/r", Branch: "main"}

	path, err := WriteWorkflow(dir, in, false)
	if err != nil {
		t.Fatalf("WriteWorkflow: %v", err)
	}
	if got, want := path, filepath.Join(dir, ".github/workflows/lightsail-deploy.yml"); got != want {
		// Allow abs-path equality on different working dirs.
		if !strings.HasSuffix(got, WorkflowRelPath) {
			t.Errorf("path = %q; want suffix %q", got, WorkflowRelPath)
		}
		_ = want
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(b), "name: Lightsail deploy") {
		t.Errorf("file contents missing header")
	}
}

func TestWriteWorkflow_ExistsErrorOnDifferingContent(t *testing.T) {
	dir := t.TempDir()
	in := WorkflowInput{App: "hello", Env: "dev", Region: "us-east-2",
		RoleARN: "arn:aws:iam::1:role/r", Branch: "main"}

	// Plant a differing existing file.
	full := filepath.Join(dir, WorkflowRelPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("something else\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := WriteWorkflow(dir, in, false)
	if err == nil {
		t.Fatalf("want ExistsError, got nil")
	}
	var ee *ExistsError
	if !errorsAs(err, &ee) {
		t.Fatalf("want *ExistsError, got %T: %v", err, err)
	}

	// overwrite=true should succeed.
	if _, err := WriteWorkflow(dir, in, true); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	b, _ := os.ReadFile(full)
	if !strings.Contains(string(b), "name: Lightsail deploy") {
		t.Errorf("overwrite did not replace file")
	}
}

func TestWriteWorkflow_IdenticalIsNoop(t *testing.T) {
	dir := t.TempDir()
	in := WorkflowInput{App: "hello", Env: "dev", Region: "us-east-2",
		RoleARN: "arn:aws:iam::1:role/r", Branch: "main"}
	// First write.
	if _, err := WriteWorkflow(dir, in, false); err != nil {
		t.Fatal(err)
	}
	// Second write of identical content should succeed without ExistsError.
	if _, err := WriteWorkflow(dir, in, false); err != nil {
		t.Errorf("identical rewrite returned error: %v", err)
	}
}

// errorsAs is a local helper so we don't import "errors" just for this.
func errorsAs[T error](err error, target *T) bool {
	for err != nil {
		if x, ok := err.(T); ok {
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
