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
		{"ca-central-1", "Canada", "Canada"},
		{"xx-foo-1", "XX", ""}, // unknown region: graceful fallback
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

// TestSupportedRegionsCoverage guarantees every region in the allowlist has
// a friendly location and a group label — so the picker never renders a
// blank cell for a supported region.
func TestSupportedRegionsCoverage(t *testing.T) {
	for _, r := range SupportedRegions() {
		if RegionLocation(r) == "" {
			t.Errorf("%s: missing location entry in regionLocations", r)
		}
		if _, ok := groupLabels[regionPrefix(r)]; !ok {
			t.Errorf("%s: prefix %q missing from groupLabels", r, regionPrefix(r))
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
