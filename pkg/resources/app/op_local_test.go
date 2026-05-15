// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/lightsailctl/pkg/lightsail"
)

func TestFormatLastDeployNeverWhenNil(t *testing.T) {
	if got := formatLastDeploy(nil); got != "never" {
		t.Errorf("nil -> %q, want %q", got, "never")
	}
}

func TestFormatLastDeployNeverWhenZeroTime(t *testing.T) {
	d := &lightsail.DeployInfo{} // zero timestamp
	if got := formatLastDeploy(d); got != "never" {
		t.Errorf("zero time -> %q, want %q", got, "never")
	}
}

func TestFormatLastDeployRecent(t *testing.T) {
	cases := []struct {
		name    string
		age     time.Duration
		want    string
		exactly bool
	}{
		{name: "just now", age: 10 * time.Second, want: "just now", exactly: true},
		{name: "minutes", age: 5 * time.Minute, want: "m ago"},
		{name: "hours", age: 3 * time.Hour, want: "h ago"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &lightsail.DeployInfo{Timestamp: time.Now().Add(-tc.age)}
			got := formatLastDeploy(d)
			if tc.exactly {
				if got != tc.want {
					t.Errorf("age %v -> %q, want %q", tc.age, got, tc.want)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("age %v -> %q, want suffix %q", tc.age, got, tc.want)
			}
		})
	}
}

func TestFormatLastDeployOlderThanADay(t *testing.T) {
	// 3 days ago -> absolute UTC timestamp like "2026-05-03 14:22"
	d := &lightsail.DeployInfo{Timestamp: time.Now().Add(-72 * time.Hour)}
	got := formatLastDeploy(d)
	if len(got) != len("2006-01-02 15:04") {
		t.Errorf("old deploy -> %q, want YYYY-MM-DD HH:MM format", got)
	}
}
