// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package lightsail

import (
	"errors"
	"testing"
	"time"

	lstypes "github.com/aws/aws-sdk-go-v2/service/lightsail/types"
)

func TestIsTooManyKeys(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{
			name: "current_phrase",
			msg:  "operation error Lightsail: CreateBucketAccessKey, InvalidInputException: You have reached the limit of access keys for ls--123--app--dev",
			want: true,
		},
		{
			name: "legacy_already_has",
			msg:  "The bucket already has 2 access keys. Delete an existing key and try again.",
			want: true,
		},
		{
			name: "legacy_maximum_number",
			msg:  "You have created the maximum number of access keys for this bucket.",
			want: true,
		},
		{
			name: "mixed_case",
			msg:  "Reached The Limit Of Access Keys",
			want: true,
		},
		{
			name: "unrelated_error",
			msg:  "some other AWS error: ThrottlingException",
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := isTooManyKeys(errors.New(c.msg))
			if got != c.want {
				t.Fatalf("isTooManyKeys(%q) = %v, want %v", c.msg, got, c.want)
			}
		})
	}
}

func TestIsStaleUnusedKey(t *testing.T) {
	now := time.Date(2026, 5, 7, 13, 0, 0, 0, time.UTC)
	fresh := now.Add(-1 * time.Minute)
	old := now.Add(-10 * time.Minute)
	used := now.Add(-2 * time.Minute)
	maxAge := 5 * time.Minute

	cases := []struct {
		name      string
		createdAt *time.Time
		lastUsed  *lstypes.AccessKeyLastUsed
		want      bool
	}{
		{"nil_createdAt", nil, nil, false},
		{"fresh_unused", &fresh, nil, false},
		{"old_unused_lastUsed_nil", &old, nil, true},
		{"old_unused_lastUsedDate_nil", &old, &lstypes.AccessKeyLastUsed{}, true},
		{"old_but_used", &old, &lstypes.AccessKeyLastUsed{LastUsedDate: &used}, false},
		{"exactly_at_maxAge", tptr(now.Add(-maxAge)), nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := isStaleUnusedKey(c.createdAt, c.lastUsed, now, maxAge)
			if got != c.want {
				t.Fatalf("isStaleUnusedKey(createdAt=%v, lastUsed=%v) = %v, want %v",
					c.createdAt, c.lastUsed, got, c.want)
			}
		})
	}
}

func tptr(t time.Time) *time.Time { return &t }
