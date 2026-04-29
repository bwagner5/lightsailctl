package lightsail

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withTempCacheDir redirects regionsCacheDir at $TMPDIR for the duration
// of a test so nothing hits the user's real cache. Returns the dir so
// callers can seed/inspect cache files.
func withTempCacheDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := regionsCacheDir
	regionsCacheDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { regionsCacheDir = orig })
	// Also reset the process-wide memo so each test starts cold.
	orig2 := sharedRegionsCache
	sharedRegionsCache = &regionsCache{}
	t.Cleanup(func() { sharedRegionsCache = orig2 })
	return dir
}

// seedCache writes a regions.json to dir with the given regions and
// fetched_at timestamp, bypassing the live Client. Used to simulate
// "warm disk" states.
func seedCache(t *testing.T, dir string, regs []Region, fetchedAt time.Time) {
	t.Helper()
	payload := diskCacheJSON{
		Version:      diskCacheVersion,
		FetchedAt:    fetchedAt,
		SourceRegion: "us-east-1",
		Regions:      toJSONRegions(regs),
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "regions.json"), b, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// TestSnapshotLoads exercises the build-time embedded snapshot: every
// shipped region must have ID / display / continent fields populated, so
// the offline fallback always produces a usable picker.
func TestSnapshotLoads(t *testing.T) {
	regs, err := readSnapshot()
	if err != nil {
		t.Fatalf("readSnapshot: %v", err)
	}
	if len(regs) == 0 {
		t.Fatal("snapshot is empty; embed directive or shipped file is broken")
	}
	for _, r := range regs {
		if r.ID == "" || r.DisplayName == "" || r.Continent == "" {
			t.Errorf("snapshot region with missing fields: %+v", r)
		}
	}
}

// TestSortRegionsCanonicalOrder asserts the picker's group → ID order,
// with the ca-central-1 override placing Canada in its own group
// (sandwiched between "Asia Pacific" and "Europe" alphabetically).
func TestSortRegionsCanonicalOrder(t *testing.T) {
	// Unsorted input covering four groups including the ca override.
	in := []Region{
		{ID: "us-west-2", Continent: "NA"},
		{ID: "eu-west-1", Continent: "EU"},
		{ID: "ca-central-1", Continent: "NA"},
		{ID: "us-east-1", Continent: "NA"},
		{ID: "ap-south-1", Continent: "AP"},
		{ID: "eu-central-1", Continent: "EU"},
	}
	sortRegions(in)
	want := []string{
		// Asia Pacific first ("Asia Pacific" < "Canada" alphabetically).
		"ap-south-1",
		// Canada (override fires on ca-* prefix).
		"ca-central-1",
		// Europe.
		"eu-central-1", "eu-west-1",
		// US (NA without override).
		"us-east-1", "us-west-2",
	}
	got := make([]string, len(in))
	for i, r := range in {
		got[i] = r.ID
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("sortRegions\n  got:  %v\n  want: %v", got, want)
	}
}

// TestGroupForRegionFallbacks checks each degradation path of group
// resolution: override > continent > uppercased prefix.
func TestGroupForRegionFallbacks(t *testing.T) {
	cases := []struct {
		region Region
		want   string
	}{
		{Region{ID: "ca-central-1", Continent: "NA"}, "Canada"},         // override wins
		{Region{ID: "us-east-1", Continent: "NA"}, "US"},                // continent
		{Region{ID: "ap-south-1", Continent: "AP"}, "Asia Pacific"},     // AP code
		{Region{ID: "ap-southeast-3", Continent: "OC"}, "Asia Pacific"}, // OC folded into AP
		{Region{ID: "zz-west-1", Continent: ""}, "ZZ"},                  // unknown prefix
		{Region{ID: "mx-east-1", Continent: "UNKNOWN"}, "MX"},           // unknown continent
	}
	for _, c := range cases {
		if got := groupForRegion(c.region); got != c.want {
			t.Errorf("groupForRegion(%+v) = %q; want %q", c.region, got, c.want)
		}
	}
}

// TestPackageWrappersUseSnapshot ensures the deprecated package-level
// helpers keep working against the embedded snapshot even with no
// cache, no creds, no network. Covers the non-breaking escape hatch
// documented in plan §2.1.
func TestPackageWrappersUseSnapshot(t *testing.T) {
	withTempCacheDir(t) // ensures SupportedRegions doesn't accidentally read disk
	ids := SupportedRegions()
	if len(ids) == 0 {
		t.Fatal("SupportedRegions empty; snapshot wrapper is broken")
	}
	// us-east-1 must exist in the shipped snapshot (plan acknowledges
	// its display changed from "N. Virginia" to "Virginia").
	if RegionLocation("us-east-1") == "" {
		t.Error("RegionLocation(us-east-1) = \"\"; snapshot missing us-east-1")
	}
	if g := RegionGroup("ca-central-1"); g != "Canada" {
		t.Errorf("RegionGroup(ca-central-1) = %q; want Canada (override)", g)
	}
	if g := RegionGroup("xx-foo-1"); g != "XX" {
		t.Errorf("RegionGroup(xx-foo-1) = %q; want XX (fallback)", g)
	}
	// SortRegionsByGroup must group Canada between AP and Europe.
	sorted := SortRegionsByGroup([]string{"us-east-1", "ca-central-1", "ap-south-1", "eu-west-1"})
	want := []string{"ap-south-1", "ca-central-1", "eu-west-1", "us-east-1"}
	for i := range want {
		if sorted[i] != want[i] {
			t.Errorf("SortRegionsByGroup[%d] = %q; want %q (full: %v)", i, sorted[i], want[i], sorted)
		}
	}
}

// TestDiskCacheWarmHit seeds a fresh cache and confirms Client.Regions
// reads it without hitting the API. A nil Client.ls + no creds would
// fail the API path, so a successful return proves the disk short-
// circuit works.
func TestDiskCacheWarmHit(t *testing.T) {
	dir := withTempCacheDir(t)
	seeded := []Region{
		{ID: "us-east-1", DisplayName: "Virginia", Continent: "NA"},
		{ID: "eu-west-1", DisplayName: "Ireland", Continent: "EU"},
	}
	seedCache(t, dir, seeded, time.Now())

	c := &Client{regions: &regionsCache{}}
	got, err := c.Regions(t.Context())
	if err != nil {
		t.Fatalf("Regions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d regions; want 2 (disk cache miss?)", len(got))
	}
}

// TestDiskCacheStaleFallsThroughToSnapshot simulates "cache older than
// TTL + API unreachable" (nil Client.ls, no region hints, empty cfg —
// fetchSourceRegion returns us-east-1 but the SDK call fails without
// creds in CI). The snapshot is the guaranteed backstop.
func TestDiskCacheStaleFallsThroughToSnapshot(t *testing.T) {
	dir := withTempCacheDir(t)
	// Mark cache as 40 days old — beyond the 30-day TTL.
	seedCache(t, dir, []Region{
		{ID: "us-east-1", DisplayName: "Virginia", Continent: "NA"},
	}, time.Now().Add(-40*24*time.Hour))

	c := &Client{regions: &regionsCache{}}
	// Prevent the API attempt by making fetchSourceRegion return "".
	// Empty region hints + empty cfg.Region defaults to us-east-1 inside
	// fetchRegionsAPI, which would try to build a real SDK call. In
	// CI that fails cleanly; locally it might succeed. Either way the
	// snapshot is a valid answer, and the test should tolerate both.
	got, err := c.Regions(t.Context())
	if err != nil {
		t.Fatalf("Regions: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no regions returned; snapshot fallback is broken")
	}
	// us-east-1 is in both the stale cache AND the snapshot, so it must
	// appear regardless of which source won.
	found := false
	for _, r := range got {
		if r.ID == "us-east-1" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("us-east-1 missing from resolved list: %+v", got)
	}
}

// TestDiskCacheCorruptFallsThroughToSnapshot writes garbage to the
// cache path and asserts Client.Regions still returns a non-empty list.
// A corrupt cache must never wedge the CLI.
func TestDiskCacheCorruptFallsThroughToSnapshot(t *testing.T) {
	dir := withTempCacheDir(t)
	if err := os.WriteFile(filepath.Join(dir, "regions.json"), []byte("not json {"), 0o644); err != nil {
		t.Fatalf("write garbage: %v", err)
	}

	c := &Client{regions: &regionsCache{}}
	got, err := c.Regions(t.Context())
	if err != nil {
		t.Fatalf("Regions: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no regions; corrupt cache wedged the loader")
	}
}

// TestDiskCacheVersionMismatch ensures future schema changes invalidate
// old entries instead of crashing. Writes a cache with version=999 and
// confirms we skip it.
func TestDiskCacheVersionMismatch(t *testing.T) {
	dir := withTempCacheDir(t)
	bogus := []byte(`{"version": 999, "fetched_at": "2026-01-01T00:00:00Z", "regions": [{"id":"x","display_name":"X","continent":"NA"}]}`)
	if err := os.WriteFile(filepath.Join(dir, "regions.json"), bogus, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, ok := readDiskCache(); ok {
		t.Error("readDiskCache accepted version=999 entry; expected rejection")
	}
}

// TestEffectiveTTLRespectsEnvOverride verifies the undocumented
// LIGHTSAILCTL_REGIONS_TTL env var short-circuits the 30-day default
// when an engineer sets it during a repro.
func TestEffectiveTTLRespectsEnvOverride(t *testing.T) {
	t.Setenv("LIGHTSAILCTL_REGIONS_TTL", "1h")
	if got, want := effectiveTTL(), time.Hour; got != want {
		t.Errorf("effectiveTTL() = %v; want %v", got, want)
	}
	t.Setenv("LIGHTSAILCTL_REGIONS_TTL", "garbage")
	if got := effectiveTTL(); got != diskCacheTTL {
		t.Errorf("effectiveTTL(garbage) = %v; want default %v", got, diskCacheTTL)
	}
}

// TestRegionIDsPrioritizesHints feeds a synthetic cache through Regions
// and confirms RegionIDs re-orders the returned list per the hints
// plumbed from main's env resolution.
func TestRegionIDsPrioritizesHints(t *testing.T) {
	dir := withTempCacheDir(t)
	seedCache(t, dir, []Region{
		{ID: "us-east-1", DisplayName: "Virginia", Continent: "NA"},
		{ID: "eu-west-1", DisplayName: "Ireland", Continent: "EU"},
		{ID: "ap-south-1", DisplayName: "Mumbai", Continent: "AP"},
	}, time.Now())

	c := &Client{
		regions:     &regionsCache{},
		regionHints: []string{"eu-west-1"},
	}
	ids, err := c.RegionIDs(t.Context())
	if err != nil {
		t.Fatalf("RegionIDs: %v", err)
	}
	if len(ids) == 0 || ids[0] != "eu-west-1" {
		t.Errorf("RegionIDs[0] = %q; want eu-west-1 first (hints: %v)", ids, c.regionHints)
	}
}

// TestPrioritizeRegions is retained from the old client_test.go since
// prioritizeRegions moved into regions.go along with RegionIDs. Same
// cases, unchanged behavior.
func TestPrioritizeRegions(t *testing.T) {
	all := []string{"ap-south-1", "eu-west-1", "us-east-1", "us-east-2"}
	cases := []struct {
		name  string
		hints []string
		want  []string
	}{
		{"no hints", nil, []string{"ap-south-1", "eu-west-1", "us-east-1", "us-east-2"}},
		{"single hint", []string{"us-east-2"}, []string{"us-east-2", "ap-south-1", "eu-west-1", "us-east-1"}},
		{"two hints preserve order", []string{"eu-west-1", "us-east-1"},
			[]string{"eu-west-1", "us-east-1", "ap-south-1", "us-east-2"}},
		{"unknown hint dropped", []string{"fake-1", "us-east-2"}, []string{"us-east-2", "ap-south-1", "eu-west-1", "us-east-1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := prioritizeRegions(all, c.hints)
			if len(got) != len(c.want) {
				t.Fatalf("len=%d want=%d: %v", len(got), len(c.want), got)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("got %v; want %v", got, c.want)
				}
			}
		})
	}
}
