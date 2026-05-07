package app

import (
	"testing"

	"github.com/aws/lightsailctl/pkg/lightsail"
)

func TestAggregate(t *testing.T) {
	buckets := []lightsail.Bucket{
		{Name: "ls--123--foo--dev", State: "OK", Region: "us-east-1"},
		{Name: "ls--123--foo--prod", State: "OK", Region: "us-east-1"},
		{Name: "ls--123--bar--dev", State: "OK", Region: "us-east-1"},
		{Name: "other--junk", State: "OK", Region: "us-east-1"}, // ignored
	}
	got := aggregate(buckets)
	if len(got) != 2 {
		t.Fatalf("want 2 apps, got %d: %+v", len(got), got)
	}
	bar, foo := got[0].(App), got[1].(App)
	if bar.Name != "bar" || bar.Envs != "dev" {
		t.Errorf("bar: %+v", bar)
	}
	if foo.Name != "foo" || foo.Envs != "dev,prod" {
		t.Errorf("foo: %+v", foo)
	}
}

// TestEnrichParallel_* were removed along with enrichParallel — the
// TUI list path no longer enriches in a background pool; per-row
// async loaders in triad's TUI handle it. The detail/Get path still
// uses the serial enrich() helper.

// TestSplitEnvs exercises the small CSV parser used by the Field.Async
// loaders to walk an App's env list.
func TestSplitEnvs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"dev", []string{"dev"}},
		{"dev,prod", []string{"dev", "prod"}},
		{"dev,,prod", []string{"dev", "prod"}},
		{",", nil},
	}
	for _, c := range cases {
		got := splitEnvs(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("splitEnvs(%q): got %v want %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitEnvs(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

// TestMarkFinalizeLoading was removed along with the markLoading /
// finalizeLoading helpers — App status columns are now populated by
// Field.Async loaders in the TUI, which render a spinner natively and
// preserve the last value across refreshes. See the triad app_test.go
// TestRenderAsyncCell for the equivalent coverage.
