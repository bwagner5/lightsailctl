// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package iamoidc

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
)

// RoleSpec is the minimum shape EnsureRole needs to reconcile a role.
type RoleSpec struct {
	Name              string // e.g. lightsailctl-deploy-alice-hello-dev
	TrustPolicy       string // JSON (typically BuildTrustPolicy output)
	PermissionsPolicy string // JSON (typically BuildPermissionsPolicy output)
	// Description is written on CreateRole; if the role already exists
	// we don't touch it (users may have edited it manually).
	Description string
	// Tags are applied on CreateRole and on idempotent re-runs via
	// TagRole. Keep small — IAM caps tags at 50 per role.
	Tags map[string]string
}

// EnsureRoleResult reports what happened during EnsureRole. Callers
// log these for the saga output.
type EnsureRoleResult struct {
	ARN             string
	Created         bool // false = found existing, trust policy reconciled
	TrustReconciled bool // true = existing role's trust policy was rewritten
	PolicyAttached  bool // true = inline policy was written (always true on success)
}

// EnsureRole creates the role if missing, or reconciles its trust
// policy if present, and always rewrites the inline permissions
// policy (PutRolePolicy is idempotent by name).
//
// Trust-policy reconciliation overwrites the existing policy in place;
// this keeps the "re-run after --branch change" loop simple. If a user
// has manually edited the trust policy out-of-band, we will overwrite
// their edit — which is why disable-gh-action is a separate step, and
// why the documented knobs are --branch / --role-name.
func (p *Provisioner) EnsureRole(ctx context.Context, spec RoleSpec) (EnsureRoleResult, error) {
	if spec.Name == "" {
		return EnsureRoleResult{}, fmt.Errorf("iamoidc: role name required")
	}
	if spec.TrustPolicy == "" {
		return EnsureRoleResult{}, fmt.Errorf("iamoidc: trust policy required")
	}
	if spec.PermissionsPolicy == "" {
		return EnsureRoleResult{}, fmt.Errorf("iamoidc: permissions policy required")
	}

	got, err := p.IAM.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(spec.Name)})
	switch {
	case err == nil:
		// Role exists — reconcile trust policy and rewrite inline policy.
		if _, uerr := p.IAM.UpdateAssumeRolePolicy(ctx, &iam.UpdateAssumeRolePolicyInput{
			RoleName:       aws.String(spec.Name),
			PolicyDocument: aws.String(spec.TrustPolicy),
		}); uerr != nil {
			return EnsureRoleResult{}, fmt.Errorf("update trust policy: %w", uerr)
		}
		if err := p.putInlinePolicy(ctx, spec.Name, spec.PermissionsPolicy); err != nil {
			return EnsureRoleResult{}, err
		}
		arn := aws.ToString(got.Role.Arn)
		return EnsureRoleResult{ARN: arn, Created: false, TrustReconciled: true, PolicyAttached: true}, nil

	case isIAMNotFound(err):
		// Create-path.
		created, cerr := p.IAM.CreateRole(ctx, &iam.CreateRoleInput{
			RoleName:                 aws.String(spec.Name),
			AssumeRolePolicyDocument: aws.String(spec.TrustPolicy),
			Description:              strPtrOrNil(spec.Description),
			Tags:                     iamTagsFromMap(spec.Tags),
		})
		if cerr != nil {
			if isIAMEntityExists(cerr) {
				// Race: another caller created it. Fall back to
				// reconciliation path.
				return p.EnsureRole(ctx, spec)
			}
			return EnsureRoleResult{}, fmt.Errorf("create role: %w", cerr)
		}
		if err := p.putInlinePolicy(ctx, spec.Name, spec.PermissionsPolicy); err != nil {
			return EnsureRoleResult{}, err
		}
		return EnsureRoleResult{
			ARN:            aws.ToString(created.Role.Arn),
			Created:        true,
			PolicyAttached: true,
		}, nil

	default:
		return EnsureRoleResult{}, fmt.Errorf("get role: %w", err)
	}
}

func (p *Provisioner) putInlinePolicy(ctx context.Context, roleName, policy string) error {
	_, err := p.IAM.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:       aws.String(roleName),
		PolicyName:     aws.String(InlinePolicyName),
		PolicyDocument: aws.String(policy),
	})
	if err != nil {
		return fmt.Errorf("put role policy: %w", err)
	}
	return nil
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return aws.String(s)
}
