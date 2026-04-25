package lightsail

import (
	"strings"
	"testing"
)

func TestRegionGroupAndLocation(t *testing.T) {
	cases := []struct {
		region     string
		wantGroup  string
		wantLocSub string // substring expected in location (or "" for unknown)
	}{
		{"us-east-1", "US", "Virginia"},
		{"eu-west-2", "Europe", "London"},
		{"ap-northeast-1", "Asia Pacific", "Tokyo"},
		{"sa-east-1", "South America", "Paulo"},
		{"af-south-1", "Africa", "Cape Town"},
		{"xx-foo-1", "XX", ""}, // unknown, falls through to prefix
	}
	for _, c := range cases {
		if got := RegionGroup(c.region); got != c.wantGroup {
			t.Errorf("RegionGroup(%q) = %q; want %q", c.region, got, c.wantGroup)
		}
		loc := RegionLocation(c.region)
		if c.wantLocSub == "" && loc != "" {
			t.Errorf("RegionLocation(%q) unexpectedly = %q", c.region, loc)
		}
		if c.wantLocSub != "" && !strings.Contains(loc, c.wantLocSub) {
			t.Errorf("RegionLocation(%q) = %q; want substring %q", c.region, loc, c.wantLocSub)
		}
	}
}

func TestSortRegionsByGroup(t *testing.T) {
	in := []string{"us-west-2", "eu-west-1", "us-east-1", "ap-south-1", "us-east-2", "eu-central-1"}
	got := SortRegionsByGroup(in)
	// Groups alphabetized by friendly name: "Asia Pacific" < "Europe" < "US".
	want := []string{"ap-south-1", "eu-central-1", "eu-west-1", "us-east-1", "us-east-2", "us-west-2"}
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %v want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("pos %d: got %q want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}
