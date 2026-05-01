package app

import (
	"testing"
	"time"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/lightsail"
)

func TestResourceAppDetailView(t *testing.T) {
	res := Resource(nil, nil)
	if res.Detail == nil {
		t.Fatal("Resource Detail is nil")
	}

	now := time.Now()
	view := res.Detail(App{
		Name:   "api",
		Envs:   "dev",
		Region: "us-east-1",
		State:  "OK",
		Age:    "2026-04-01T12:00:00Z",
		envStatuses: map[string][]lightsail.Status{
			"dev": {{
				Instance:  "box-a",
				Status:    "healthy",
				Endpoints: []string{"http://203.0.113.10:8080"},
				LastDeploy: &lightsail.DeployInfo{
					Timestamp: now.Add(-5 * time.Minute),
				},
				Containers: []lightsail.ContainerStatus{
					{Name: "web", Status: "running", StartedAt: now.Add(-5 * time.Minute)},
				},
			}},
		},
		envBuckets: map[string]string{"dev": "ls--123--api--dev"},
	})

	assertDetailRow(t, view, "Name", "api")
	assertDetailRow(t, view, "Region", "us-east-1")
	assertDetailRow(t, view, "Instance", "box-a")
	assertDetailRow(t, view, "Health", "● healthy")
	assertDetailRow(t, view, "Endpoint", "http://203.0.113.10:8080")
	// Container row is indented
	assertDetailRow(t, view, "  web", "● running  up 5m ago")
}

func TestAppDetailViewShowsUsefulEmptyState(t *testing.T) {
	view := appDetail(App{Name: "api"})
	assertDetailRow(t, view, "Name", "api")
	assertDetailRow(t, view, "Status", "no environments yet — run deploy to create one")
}

func TestAppDetailViewFallsBackForUnknownItem(t *testing.T) {
	view := appDetail(struct{ Name string }{Name: "api"})
	if len(view.Sections) != 0 {
		t.Fatalf("unexpected detail view for unknown item: %+v", view)
	}
}

func TestAppDetailUnenrichedFallback(t *testing.T) {
	view := appDetail(App{
		Name:      "api",
		Envs:      "dev",
		Instances: "box-a",
		Endpoints: "http://1.2.3.4:8080",
		Status:    "dev: 1/1",
	})
	assertDetailRow(t, view, "Instances", "box-a")
	assertDetailRow(t, view, "Endpoints", "http://1.2.3.4:8080")
	assertDetailRow(t, view, "Health", "dev: 1/1")
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
