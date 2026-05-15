// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package instance

import (
	"testing"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/lightsail"
)

func TestResourceInstanceDetailView(t *testing.T) {
	res := Resource(nil, nil)
	if res.Detail == nil {
		t.Fatal("Resource Detail is nil")
	}

	view := res.Detail(Row{
		Name:       "box-a",
		State:      "running",
		Age:        "2026-04-01T12:00:00Z",
		IP:         "203.0.113.10",
		Region:     "us-east-1",
		Blueprint:  "ubuntu_24_04",
		Bundle:     "nano_3_0",
		appTargets: "api/dev, api/prod",
	})

	assertDetailRow(t, view, "Public IP", "203.0.113.10")
	assertDetailRow(t, view, "Blueprint", "ubuntu_24_04")
	assertDetailRow(t, view, "Bundle", "nano_3_0")
	assertDetailRow(t, view, "Targets", "api/dev, api/prod")
	assertDetailRow(t, view, "Next", "open SSH or deploy to an attached app")
}

func TestAppTargetsFromTags(t *testing.T) {
	got := appTargets(map[string]string{
		lightsail.TagPrefix + "api:prod": "",
		lightsail.TagPrefix + "api:dev":  "",
		lightsail.TagPrefix + ":broken":  "",
		"unrelated":                      "",
	})
	if got != "api/dev, api/prod" {
		t.Fatalf("appTargets = %q; want api/dev, api/prod", got)
	}
}

func TestInstanceDetailViewShowsUsefulEmptyState(t *testing.T) {
	view := rowDetail(Row{Name: "box-a", State: "stopped"})

	assertDetailRow(t, view, "Public IP", "none")
	assertDetailRow(t, view, "Blueprint", "unknown")
	assertDetailRow(t, view, "Bundle", "unknown")
	assertDetailRow(t, view, "Targets", "none discovered")
	assertDetailRow(t, view, "Next", "start the instance before deploys or SSH")
}

func TestInstanceDetailViewFallsBackForUnknownItem(t *testing.T) {
	view := rowDetail(struct{ Name string }{Name: "box-a"})
	if len(view.Sections) != 0 {
		t.Fatalf("unexpected detail view for unknown item: %+v", view)
	}
}

func assertDetailRow(t *testing.T, view registry.DetailView, label, want string) {
	t.Helper()
	for _, section := range view.Sections {
		for _, row := range section.Rows {
			if row.Label == label {
				if row.Value != want {
					t.Fatalf("%s = %q; want %q", label, row.Value, want)
				}
				return
			}
		}
	}
	t.Fatalf("detail row %q not found in %+v", label, view)
}
