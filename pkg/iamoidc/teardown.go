package iamoidc

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
)

// iamTagsFromMap converts a plain map into the IAM Tag shape in a
// deterministic order (alphabetical by key) so repeated runs emit the
// same CreateRole bytes.
func iamTagsFromMap(m map[string]string) []iamtypes.Tag {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// sort import-free: tiny input.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	out := make([]iamtypes.Tag, 0, len(keys))
	for _, k := range keys {
		out = append(out, iamtypes.Tag{Key: aws.String(k), Value: aws.String(m[k])})
	}
	return out
}

// DeleteRole removes the inline policy first (a required precondition
// for DeleteRole when an inline policy is attached) and then deletes
// the role. Idempotent: missing resources are treated as success.
//
// The OIDC provider is NEVER deleted here — it's shared account-wide
// and other apps may depend on it. Users who truly want it gone can
// run `aws iam delete-open-id-connect-provider` themselves.
func (p *Provisioner) DeleteRole(ctx context.Context, roleName string) error {
	if roleName == "" {
		return fmt.Errorf("iamoidc: role name required")
	}

	// 1. Delete the inline policy (best-effort).
	if _, err := p.IAM.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{
		RoleName:   aws.String(roleName),
		PolicyName: aws.String(InlinePolicyName),
	}); err != nil && !isIAMNotFound(err) {
		return fmt.Errorf("delete role policy: %w", err)
	}

	// 2. Delete the role itself. Tolerate not-found so a partial
	//    previous teardown can be retried cleanly.
	if _, err := p.IAM.DeleteRole(ctx, &iam.DeleteRoleInput{
		RoleName: aws.String(roleName),
	}); err != nil && !isIAMNotFound(err) {
		return fmt.Errorf("delete role: %w", err)
	}
	return nil
}
