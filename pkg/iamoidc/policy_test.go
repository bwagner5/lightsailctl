package iamoidc

import (
	"encoding/json"
	"strings"
	"testing"
)

// canonical trust-policy input. Every test field here is a realistic
// value — the point is to pin the exact JSON shape so accidental
// re-ordering or key renames break a test before they break a user.
func canonicalTrustInput() TrustPolicyInput {
	return TrustPolicyInput{
		AccountID:    "111111111111",
		Owner:        "alice",
		Repo:         "hello",
		RepositoryID: "123456789",
	}
}

func TestBuildTrustPolicy_Golden(t *testing.T) {
	got, err := BuildTrustPolicy(canonicalTrustInput())
	if err != nil {
		t.Fatalf("BuildTrustPolicy: %v", err)
	}

	// Parse & re-marshal to compare structurally while remaining
	// tolerant to any future formatting-only churn in marshalPretty.
	var gotMap map[string]any
	if err := json.Unmarshal([]byte(got), &gotMap); err != nil {
		t.Fatalf("unmarshal output: %v\n%s", err, got)
	}

	wantJSON := `{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {
        "Federated": "arn:aws:iam::111111111111:oidc-provider/token.actions.githubusercontent.com"
      },
      "Action": "sts:AssumeRoleWithWebIdentity",
      "Condition": {
        "StringEquals": {
          "token.actions.githubusercontent.com:aud": "sts.amazonaws.com",
          "token.actions.githubusercontent.com:repository_id": "123456789"
        },
        "StringLike": {
          "token.actions.githubusercontent.com:sub": "repo:alice/hello:*"
        }
      }
    }
  ]
}`
	var wantMap map[string]any
	if err := json.Unmarshal([]byte(wantJSON), &wantMap); err != nil {
		t.Fatalf("unmarshal expected: %v", err)
	}
	if !jsonEqual(gotMap, wantMap) {
		t.Errorf("trust policy mismatch.\n--- got:\n%s\n--- want:\n%s", got, wantJSON)
	}
}

func TestBuildTrustPolicy_BranchPin(t *testing.T) {
	in := canonicalTrustInput()
	in.Branch = "main"
	got, err := BuildTrustPolicy(in)
	if err != nil {
		t.Fatalf("BuildTrustPolicy: %v", err)
	}
	if !strings.Contains(got, `"token.actions.githubusercontent.com:ref": "refs/heads/main"`) {
		t.Errorf("branch pin missing from trust policy:\n%s", got)
	}
}

func TestBuildTrustPolicy_NoRepoIDStillValid(t *testing.T) {
	// GovCloud-style partition lag: no repository_id available.
	// The policy should still compile, just without that key.
	in := canonicalTrustInput()
	in.RepositoryID = ""
	got, err := BuildTrustPolicy(in)
	if err != nil {
		t.Fatalf("BuildTrustPolicy: %v", err)
	}
	if strings.Contains(got, "repository_id") {
		t.Errorf("unexpected repository_id in output:\n%s", got)
	}
	if !strings.Contains(got, `"token.actions.githubusercontent.com:sub": "repo:alice/hello:*"`) {
		t.Errorf("sub wildcard missing:\n%s", got)
	}
}

func TestBuildTrustPolicy_Errors(t *testing.T) {
	cases := []struct {
		name string
		in   TrustPolicyInput
	}{
		{"no account", TrustPolicyInput{Owner: "a", Repo: "b"}},
		{"no owner", TrustPolicyInput{AccountID: "1", Repo: "b"}},
		{"no repo", TrustPolicyInput{AccountID: "1", Owner: "a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := BuildTrustPolicy(tc.in); err == nil {
				t.Errorf("want error, got nil")
			}
		})
	}
}

func TestBuildPermissionsPolicy_Golden(t *testing.T) {
	in := PermissionsPolicyInput{
		AccountID: "111111111111",
		App:       "hello",
		Env:       "dev",
		Region:    "us-east-2",
		Targets: []TargetInstance{
			{Name: "my-box", Region: "us-east-2"},
		},
	}
	got, err := BuildPermissionsPolicy(in)
	if err != nil {
		t.Fatalf("BuildPermissionsPolicy: %v", err)
	}

	// Structural assertions — the exact ordering of keys in maps
	// depends on encoding/json, but statements/Action arrays ARE ordered.
	var gotMap map[string]any
	if err := json.Unmarshal([]byte(got), &gotMap); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, got)
	}
	stmts, ok := gotMap["Statement"].([]any)
	if !ok || len(stmts) != 4 {
		t.Fatalf("want 4 statements, got %d\n%s", len(stmts), got)
	}

	// Sid order is deterministic by construction.
	wantSids := []string{"ReadIdentity", "LightsailRead", "LightsailBucketKeys", "LightsailFirewall"}
	for i, want := range wantSids {
		s := stmts[i].(map[string]any)
		if sid, _ := s["Sid"].(string); sid != want {
			t.Errorf("statement %d Sid = %q; want %q", i, sid, want)
		}
	}

	// The bucket ARN must be fully rendered — no placeholders.
	wantBucket := "arn:aws:lightsail:us-east-2:111111111111:Bucket/ls--111111111111--hello--dev"
	if !strings.Contains(got, wantBucket) {
		t.Errorf("env bucket ARN missing:\n%s", got)
	}
	// Instance ARN likewise.
	wantInstance := "arn:aws:lightsail:us-east-2:111111111111:Instance/my-box"
	if !strings.Contains(got, wantInstance) {
		t.Errorf("instance ARN missing:\n%s", got)
	}
}

func TestBuildPermissionsPolicy_MultiTarget(t *testing.T) {
	in := PermissionsPolicyInput{
		AccountID: "111111111111",
		App:       "hello",
		Env:       "dev",
		Region:    "us-east-2",
		Targets: []TargetInstance{
			// Unsorted on input — builder must sort for stable output.
			{Name: "zebra", Region: "us-east-2"},
			{Name: "alpha", Region: "us-east-2"},
		},
	}
	got, err := BuildPermissionsPolicy(in)
	if err != nil {
		t.Fatalf("BuildPermissionsPolicy: %v", err)
	}
	// Alpha should appear before zebra in the Resource array.
	ai := strings.Index(got, "Instance/alpha")
	zi := strings.Index(got, "Instance/zebra")
	if ai < 0 || zi < 0 {
		t.Fatalf("missing instance ARNs:\n%s", got)
	}
	if ai > zi {
		t.Errorf("targets not sorted: alpha at %d, zebra at %d", ai, zi)
	}
}

func TestBuildPermissionsPolicy_NoTargetsDropsFirewallStmt(t *testing.T) {
	in := PermissionsPolicyInput{
		AccountID: "111111111111",
		App:       "hello",
		Env:       "dev",
		Region:    "us-east-2",
		// Targets omitted. Deploys that skip firewall edits (no ports
		// in docker-compose.yml) still need to pass; the CI role just
		// won't include the LightsailFirewall statement.
	}
	got, err := BuildPermissionsPolicy(in)
	if err != nil {
		t.Fatalf("BuildPermissionsPolicy: %v", err)
	}
	if strings.Contains(got, "LightsailFirewall") {
		t.Errorf("firewall statement present with no targets:\n%s", got)
	}
}

func TestBuildPermissionsPolicy_Errors(t *testing.T) {
	// Every required field missing one at a time.
	base := PermissionsPolicyInput{AccountID: "1", App: "a", Env: "e", Region: "r"}
	cases := []struct {
		name string
		mut  func(*PermissionsPolicyInput)
	}{
		{"no account", func(in *PermissionsPolicyInput) { in.AccountID = "" }},
		{"no app", func(in *PermissionsPolicyInput) { in.App = "" }},
		{"no env", func(in *PermissionsPolicyInput) { in.Env = "" }},
		{"no region", func(in *PermissionsPolicyInput) { in.Region = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			tc.mut(&in)
			if _, err := BuildPermissionsPolicy(in); err == nil {
				t.Errorf("want error, got nil")
			}
		})
	}
}

func TestDefaultRoleName(t *testing.T) {
	got := DefaultRoleName("alice", "hello", "dev")
	want := "lightsailctl-deploy-alice-hello-dev"
	if got != want {
		t.Errorf("DefaultRoleName = %q; want %q", got, want)
	}
}

func TestOIDCProviderARN(t *testing.T) {
	got := OIDCProviderARN("111111111111")
	want := "arn:aws:iam::111111111111:oidc-provider/token.actions.githubusercontent.com"
	if got != want {
		t.Errorf("OIDCProviderARN = %q; want %q", got, want)
	}
}

// jsonEqual does a deep structural compare on two decoded maps.
func jsonEqual(a, b any) bool {
	am, _ := json.Marshal(a)
	bm, _ := json.Marshal(b)
	return string(am) == string(bm)
}
