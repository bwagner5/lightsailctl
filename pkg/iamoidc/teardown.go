package iamoidc

import (
	"context"
	"fmt"
	"strings"

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

// RoleNamePrefix is prepended to every CI-deploy role this CLI creates.
// Used by FindRolesForApp as the ListRoles filter so we only paginate
// through CI roles, not every role in the account.
const RoleNamePrefix = "lightsailctl-deploy-"

// TagKeyApp is the IAM tag used to record the Lightsail app name on
// CI-deploy roles. Matches the tag set in EnsureRole's RoleSpec.
const TagKeyApp = "lightsailctl:app"

// FindRolesForApp returns every IAM role whose name starts with
// RoleNamePrefix and whose lightsailctl:app tag equals appName.
//
// Used by `app delete` to tear down the CI role(s) a
// previously-enabled GitHub Actions workflow left behind. Returns an
// empty slice with no error when the IAM listing succeeds but nothing
// matches (common: the user never enabled CI).
//
// Implementation: IAM has no native "list roles by tag" API, so we
// paginate ListRoles with the PathPrefix filter ("/" — all roles)
// and client-side filter by name prefix. For each matching role we
// fetch tags via ListRoleTags. This is O(N) over account-wide roles,
// but the name-prefix filter narrows the ListRoleTags calls to just
// the lightsailctl-deploy-* subset.
func (p *Provisioner) FindRolesForApp(ctx context.Context, appName string) ([]string, error) {
	if appName == "" {
		return nil, fmt.Errorf("iamoidc: app name required")
	}
	var out []string
	var marker *string
	for {
		page, err := p.IAM.ListRoles(ctx, &iam.ListRolesInput{
			Marker: marker,
		})
		if err != nil {
			return nil, fmt.Errorf("list roles: %w", err)
		}
		for _, r := range page.Roles {
			name := aws.ToString(r.RoleName)
			if !strings.HasPrefix(name, RoleNamePrefix) {
				continue
			}
			tags, terr := p.IAM.ListRoleTags(ctx, &iam.ListRoleTagsInput{
				RoleName: aws.String(name),
			})
			if terr != nil {
				// Don't let a single role's tag read fail the whole
				// discovery. A role we can't introspect is a role we
				// can't delete safely anyway.
				continue
			}
			for _, t := range tags.Tags {
				if aws.ToString(t.Key) == TagKeyApp && aws.ToString(t.Value) == appName {
					out = append(out, name)
					break
				}
			}
		}
		if !page.IsTruncated {
			break
		}
		marker = page.Marker
	}
	return out, nil
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
