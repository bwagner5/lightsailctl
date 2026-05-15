// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package lightsail

import "testing"

func TestValidateBucketName(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"ls--123--foo--dev", true},
		{"ls--123--foo", true},
		{"ls--123--foo--", false},   // trailing dash
		{"-ls--123--foo", false},    // leading dash
		{"ls--123--FOO", false},     // uppercase
		{"ls--123--foo_bar", false}, // underscore
		{"ab", false},               // too short
		{"", false},
	}
	for _, c := range cases {
		err := ValidateBucketName(c.name)
		if c.ok && err != nil {
			t.Errorf("%q: want ok, got %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%q: want error, got nil", c.name)
		}
	}
}
