// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package lightsail

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	lstypes "github.com/aws/aws-sdk-go-v2/service/lightsail/types"

	"github.com/aws/lightsailctl/internal"
)

// Tag key constants used on every AWS resource lightsailctl creates.
// Kept lowercase with colon separators so they read as a namespace.
const (
	// VersionTagKey records the lightsailctl version that created the
	// resource. Consumed by future upgrade tooling so a newer CLI can
	// recognize resources created by an older one and (when needed)
	// apply schema/migration work.
	VersionTagKey = "lightsailctl:version"
)

// CLIVersion returns the running lightsailctl version as a plain
// string (e.g. "v1.0.8"). Wrapper around internal.Version so pkg/lightsail
// doesn't leak the Semver type to its callers.
func CLIVersion() string {
	return internal.Version().String()
}

// DefaultResourceTags returns the set of tags every lightsailctl-owned
// AWS resource gets on creation. Currently just the version tag; the
// function shape is future-proof for additional universal tags (e.g.
// a "lightsailctl:managed" marker).
func DefaultResourceTags() map[string]string {
	return map[string]string{
		VersionTagKey: CLIVersion(),
	}
}

// lightsailTagsFromMap converts a string map into the Lightsail SDK's
// Tag slice in a deterministic order (alphabetical key) so repeat
// calls emit identical API requests.
func lightsailTagsFromMap(m map[string]string) []lstypes.Tag {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Tiny input; an in-place insertion sort is more than enough.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	out := make([]lstypes.Tag, 0, len(keys))
	for _, k := range keys {
		out = append(out, lstypes.Tag{
			Key:   aws.String(k),
			Value: aws.String(m[k]),
		})
	}
	return out
}

// mergeTagMaps returns a new map containing every key from both
// inputs, with `b` winning when keys collide. Nil maps are treated as
// empty. Result is always non-nil (empty map if both inputs are
// empty) so call sites can pass it directly to builders that expect a
// map.
func mergeTagMaps(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
