package app

import (
	"testing"

	"github.com/bwagner5/triad/pkg/registry"
)

func TestResourceAppDetailView(t *testing.T) {
	res := Resource(nil, nil)
	if res.Detail == nil {
		t.Fatal("Resource Detail is nil")
	}

	view := res.Detail(App{
		Name:      "api",
		Envs:      "dev,prod",
		Region:    "us-east-1",
		State:     "OK",
		Age:       "2026-04-01T12:00:00Z",
		Bucket:    "ls--123--api",
		Instances: "box-a,box-b",
		Endpoints: "http://203.0.113.10:8080",
		Status:    "dev: 2/2",
	})

	assertDetailRow(t, view, "Config bucket", "ls--123--api")
	assertDetailRow(t, view, "Names", "dev,prod")
	assertDetailRow(t, view, "Health", "dev: 2/2")
	assertDetailRow(t, view, "Instances", "box-a,box-b")
	assertDetailRow(t, view, "Endpoints", "http://203.0.113.10:8080")
	assertDetailRow(t, view, "Next", "open logs or deploy a new version")
}

func TestAppDetailViewShowsUsefulEmptyState(t *testing.T) {
	view := appDetail(App{Name: "api"})

	assertDetailRow(t, view, "Config bucket", "not created")
	assertDetailRow(t, view, "Names", "none")
	assertDetailRow(t, view, "Health", "not reported yet")
	assertDetailRow(t, view, "Instances", "none discovered")
	assertDetailRow(t, view, "Endpoints", "none reported")
	assertDetailRow(t, view, "Next", "deploy to create the first environment")
}

func TestAppDetailViewFallsBackForUnknownItem(t *testing.T) {
	view := appDetail(struct{ Name string }{Name: "api"})
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
