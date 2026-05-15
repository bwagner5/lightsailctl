// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package iamoidc

import (
	"encoding/json"
	"fmt"
	"sort"
)

// The GitHub OIDC issuer and the fixed audience the
// aws-actions/configure-aws-credentials action sends in STS
// AssumeRoleWithWebIdentity requests.
const (
	// GitHubIssuerHost is the host portion of token.actions.githubusercontent.com;
	// used in both the OIDC-provider URL and the policy condition keys.
	GitHubIssuerHost = "token.actions.githubusercontent.com"

	// GitHubIssuerURL is the full HTTPS URL AWS IAM stores as the OIDC
	// provider URL (the host without a path).
	GitHubIssuerURL = "https://" + GitHubIssuerHost

	// STSAudience is what aws-actions/configure-aws-credentials@v4 sends
	// as the aud claim; IAM pins against it via the :aud condition key.
	STSAudience = "sts.amazonaws.com"

	// InlinePolicyName is the name of the inline policy attached to the
	// generated role. Fixed so PutRolePolicy overwrites in place across
	// re-runs and so teardown knows what to detach.
	InlinePolicyName = "lightsailctl-deploy"
)

// TargetInstance describes one Lightsail instance that the CI role
// must be allowed to open firewall ports on. Multi-instance deploys
// produce multiple Resource ARNs in the LightsailFirewall statement.
type TargetInstance struct {
	Name   string
	Region string
}

// TrustPolicyInput gathers the values the trust-policy template needs.
// Only non-secret, user-facing identifiers live here — the generated
// JSON is shown to the user for confirmation before any API call.
type TrustPolicyInput struct {
	// AccountID is the AWS account where the role (and its trust
	// policy's Principal.Federated ARN) live.
	AccountID string
	// Owner and Repo together form the "owner/repo" the OIDC JWT's
	// `sub` claim identifies (repo:<owner>/<repo>:*). Case-sensitive
	// on the GitHub side; echoed verbatim into the policy.
	Owner, Repo string
	// RepositoryID is the immutable numeric GitHub repo ID. Rendered as
	// a StringEquals on the token.actions.githubusercontent.com:repository_id
	// condition key so the trust survives owner/repo renames and is not
	// bypassable by typosquatting. Leave "" only when the caller has
	// confirmed it couldn't fetch it (older GitHub, partition lag).
	RepositoryID string
	// Branch, when non-empty, tightens the trust policy to a single git
	// ref by adding token.actions.githubusercontent.com:ref =
	// refs/heads/<branch> as a StringEquals entry. Off by default.
	Branch string
}

// PermissionsPolicyInput gathers the values the permissions-policy
// template needs. Every resource name is rendered into the JSON before
// it's shown to the user.
type PermissionsPolicyInput struct {
	AccountID string
	App       string
	Env       string
	// Region is the Lightsail region that hosts the bucket + target
	// instances. Baked into every ARN so one role can't touch a
	// different region's resources in the same account.
	Region string
	// Targets is the set of instances the role must be able to call
	// PutInstancePublicPorts on. Typically one entry (the configured
	// deploy target); may be more when the user's deploy spans
	// multiple tagged instances.
	Targets []TargetInstance
}

// BuildTrustPolicy returns the trust-policy JSON as a pretty-printed
// string. The shape is stable: two consecutive calls with the same
// input produce byte-identical output (so golden tests and diffs work).
func BuildTrustPolicy(in TrustPolicyInput) (string, error) {
	if in.AccountID == "" {
		return "", fmt.Errorf("iamoidc: AccountID is required")
	}
	if in.Owner == "" || in.Repo == "" {
		return "", fmt.Errorf("iamoidc: Owner and Repo are required")
	}

	// StringEquals holds the always-present aud pin plus the repository_id
	// pin (when present) and the branch pin (when present). Use sorted
	// keys so tests are stable.
	stringEquals := map[string]string{
		GitHubIssuerHost + ":aud": STSAudience,
	}
	if in.RepositoryID != "" {
		stringEquals[GitHubIssuerHost+":repository_id"] = in.RepositoryID
	}
	if in.Branch != "" {
		stringEquals[GitHubIssuerHost+":ref"] = "refs/heads/" + in.Branch
	}

	// StringLike always carries the sub wildcard. AWS IAM identity
	// provider controls require a sub evaluation; the real scoping
	// comes from repository_id above.
	stringLike := map[string]string{
		GitHubIssuerHost + ":sub": "repo:" + in.Owner + "/" + in.Repo + ":*",
	}

	doc := policyDoc{
		Version: "2012-10-17",
		Statement: []any{
			trustStatement{
				Effect: "Allow",
				Principal: map[string]string{
					"Federated": OIDCProviderARN(in.AccountID),
				},
				Action: "sts:AssumeRoleWithWebIdentity",
				Condition: map[string]map[string]string{
					"StringEquals": sortedMap(stringEquals),
					"StringLike":   sortedMap(stringLike),
				},
			},
		},
	}
	return marshalPretty(doc)
}

// BuildPermissionsPolicy returns the inline permissions-policy JSON as
// a pretty-printed string.
func BuildPermissionsPolicy(in PermissionsPolicyInput) (string, error) {
	if in.AccountID == "" || in.App == "" || in.Env == "" || in.Region == "" {
		return "", fmt.Errorf("iamoidc: AccountID, App, Env, Region are required")
	}
	envBucket := fmt.Sprintf("ls--%s--%s--%s", in.AccountID, in.App, in.Env)
	bucketARN := fmt.Sprintf("arn:aws:lightsail:%s:%s:Bucket/%s", in.Region, in.AccountID, envBucket)

	// Stable ordering: sort targets by (region, name) so the same input
	// produces the same JSON.
	targets := make([]TargetInstance, len(in.Targets))
	copy(targets, in.Targets)
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Region != targets[j].Region {
			return targets[i].Region < targets[j].Region
		}
		return targets[i].Name < targets[j].Name
	})

	var firewallResources []string
	for _, t := range targets {
		if t.Name == "" || t.Region == "" {
			continue
		}
		firewallResources = append(firewallResources,
			fmt.Sprintf("arn:aws:lightsail:%s:%s:Instance/%s", t.Region, in.AccountID, t.Name))
	}

	statements := []any{
		permStatement{
			Sid:      "ReadIdentity",
			Effect:   "Allow",
			Action:   []string{"sts:GetCallerIdentity"},
			Resource: "*",
		},
		permStatement{
			Sid:    "LightsailRead",
			Effect: "Allow",
			Action: []string{
				"lightsail:GetInstances",
				"lightsail:GetInstance",
				"lightsail:GetBuckets",
			},
			Resource: "*",
		},
		permStatement{
			Sid:    "LightsailBucketKeys",
			Effect: "Allow",
			Action: []string{
				"lightsail:CreateBucketAccessKey",
				"lightsail:DeleteBucketAccessKey",
			},
			Resource: bucketARN,
		},
	}
	if len(firewallResources) > 0 {
		statements = append(statements, permStatement{
			Sid:      "LightsailFirewall",
			Effect:   "Allow",
			Action:   []string{"lightsail:PutInstancePublicPorts"},
			Resource: firewallResources,
		})
	}

	return marshalPretty(policyDoc{
		Version:   "2012-10-17",
		Statement: statements,
	})
}

// OIDCProviderARN returns the canonical ARN of the GitHub OIDC
// provider in the given AWS account. The host portion never varies.
func OIDCProviderARN(accountID string) string {
	return fmt.Sprintf("arn:aws:iam::%s:oidc-provider/%s", accountID, GitHubIssuerHost)
}

// DefaultRoleName composes the role name used when the user doesn't
// override with --role-name. Shape:
//
//	lightsailctl-deploy-<owner>-<repo>-<env>
//
// Caller is responsible for sanitizing owner/repo/env into
// IAM-role-name-safe tokens before calling this. IAM role names are
// limited to 64 characters; this function does NOT truncate — callers
// should decide what to do if the result is too long (typically: error
// with a suggestion to pass --role-name).
func DefaultRoleName(owner, repo, env string) string {
	return fmt.Sprintf("lightsailctl-deploy-%s-%s-%s", owner, repo, env)
}

// ── internal JSON shapes ───────────────────────────────────────────────

type policyDoc struct {
	Version   string `json:"Version"`
	Statement []any  `json:"Statement"`
}

type trustStatement struct {
	Effect    string                       `json:"Effect"`
	Principal map[string]string            `json:"Principal"`
	Action    string                       `json:"Action"`
	Condition map[string]map[string]string `json:"Condition"`
}

// permStatement carries either a scalar Resource string or a []string,
// which json.Marshal handles directly when the field is `any`.
type permStatement struct {
	Sid      string   `json:"Sid"`
	Effect   string   `json:"Effect"`
	Action   []string `json:"Action"`
	Resource any      `json:"Resource"`
}

// sortedMap returns m with the keys in a deterministic order.
// encoding/json already sorts map keys when marshaling, so this is
// really a no-op for JSON output — it exists as a safety net in case
// callers want to embed the map into something else later.
func sortedMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out[k] = m[k]
	}
	return out
}

func marshalPretty(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
