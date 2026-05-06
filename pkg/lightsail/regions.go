package lightsail

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bwagner5/triad/pkg/trace"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lightsail"
	"github.com/aws/aws-sdk-go-v2/service/lightsail/types"
)

// Region describes one Lightsail-supported region.
type Region struct {
	ID          string // "us-east-2"
	DisplayName string // "Ohio"
	Continent   string // "NA", "EU", "AP", … (ISO continent code from GetRegions)
	Description string // full marketing blurb, for detail views
}

// regionsCache is the process-wide memoization layer, shared across every
// *Client (including WithRegion copies) via a pointer held on Client. Its
// only job is to turn an "N callers in the same process" workload into a
// single disk read (and at most one API round-trip) per process.
type regionsCache struct {
	mu     sync.Mutex
	loaded []Region // canonical sort order; empty until first populate
}

// sharedRegionsCache is wired into every Client by NewWithOptions. A single
// package-level pointer means the Cobra root command and the TUI session
// see the same in-memory answer without plumbing it explicitly.
var sharedRegionsCache = &regionsCache{}

//go:embed regions_snapshot.json
var regionsSnapshotRaw []byte

// diskCacheJSON models the JSON shape written to
// $UserCacheDir/lightsailctl/regions.json. Version is bumped if the
// schema ever changes so stale entries are invalidated without manual
// cleanup. The same schema is used for the embedded snapshot.
type diskCacheJSON struct {
	Version      int        `json:"version"`
	FetchedAt    time.Time  `json:"fetched_at"`
	SourceRegion string     `json:"source_region"`
	Regions      []regionJS `json:"regions"`
}

type regionJS struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Continent   string `json:"continent"`
	Description string `json:"description"`
}

const (
	diskCacheVersion = 1
	diskCacheTTL     = 30 * 24 * time.Hour // 30 days (§2.2)
)

// continentGroupLabels turns an ISO continent code into the friendly
// group heading shown in the region picker. This is the only hand-
// maintained mapping that survives from the old code, and it's stable:
// the set of continents is fixed, the Lightsail team doesn't invent new
// ones on release cadence.
var continentGroupLabels = map[string]string{
	"NA": "US", // North America — overridden below for ca-*.
	"SA": "South America",
	"EU": "Europe",
	"AS": "Asia Pacific", // some partners encode Asia as AS
	"AP": "Asia Pacific", // Lightsail GetRegions uses AP today
	"OC": "Asia Pacific", // Oceania rolls into AP for UX parity
	"AF": "Africa",
	"ME": "Middle East",
}

// idPrefixGroupOverrides lets us keep the old UX for regions where the
// continent grouping is too coarse to match shipped screenshots. Today
// this is only ca-* → "Canada" (Montreal is geographically NA but we've
// always split it into its own group). Keep this table tiny; overriding
// anything else is a policy decision, not a data decision.
var idPrefixGroupOverrides = map[string]string{
	"ca": "Canada",
}

// regionsCacheDir is the directory under which regions.json is
// written. Split out as a package var so tests can redirect it to
// t.TempDir() without touching the user's real cache.
var regionsCacheDir = defaultRegionsCacheDir

func defaultRegionsCacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "lightsailctl"), nil
}

// regionsCachePath returns the fully-qualified path to regions.json.
// Errors propagate so callers can fall through to the embedded
// snapshot when the OS refuses to produce a cache location
// (locked-down CI, read-only FS, etc.).
func regionsCachePath() (string, error) {
	dir, err := regionsCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "regions.json"), nil
}

// effectiveTTL is the cache's staleness cutoff. The undocumented
// LIGHTSAILCTL_REGIONS_TTL env var lets engineers force a shorter TTL
// during a manual repro; it is deliberately not documented on the
// marketing surface.
func effectiveTTL() time.Duration {
	if v := os.Getenv("LIGHTSAILCTL_REGIONS_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return diskCacheTTL
}

// Regions returns the full list of Lightsail regions in canonical sort
// order (group alphabetized by friendly name, then ID within group).
//
// Resolution order on a cold process:
//  1. In-memory memo (Client.regions). One disk-read per process max.
//  2. On-disk cache if present and < 30 days old.
//  3. GetRegions API call, written back to disk.
//  4. Embedded snapshot — guaranteed last-ditch fallback so the picker
//     and fan-out keep working offline / without creds.
//
// The returned slice is owned by the cache; callers must not mutate it.
// Copy it before sorting or filtering.
func (c *Client) Regions(ctx context.Context) ([]Region, error) {
	if c.regions == nil {
		// Defensive: Clients built outside NewWithOptions still work.
		c.regions = &regionsCache{}
	}
	c.regions.mu.Lock()
	defer c.regions.mu.Unlock()
	if len(c.regions.loaded) > 0 {
		return c.regions.loaded, nil
	}

	regs, source := c.resolveRegions(ctx)
	// Sort once, cache the sorted slice. Every downstream caller wants
	// the same order (picker, fan-out), and the sort is stable.
	sortRegions(regs)
	c.regions.loaded = regs
	trace.Trace(ctx, "lightsail regions loaded", "source", source, "count", len(regs))
	return regs, nil
}

// resolveRegions walks the disk-cache / API / snapshot chain, returning
// the best available region list and a short label describing where it
// came from (for traces). It never returns an error: the embedded
// snapshot is the universal fallback.
func (c *Client) resolveRegions(ctx context.Context) ([]Region, string) {
	// Disk first — by far the common case after the first run.
	if regs, ok := readDiskCache(ctx); ok {
		return regs, "disk"
	}
	// Disk miss or stale: try the live API.
	if regs, err := c.fetchRegionsAPI(ctx); err == nil && len(regs) > 0 {
		if werr := writeDiskCache(regs, c.fetchSourceRegion()); werr != nil {
			// Cache write failing is not fatal; the next cold start
			// will just hit the API again.
			trace.FromContext(ctx).WarnContext(ctx, "regions cache write failed",
				slog.Any("err", werr))
		}
		return regs, "api"
	}
	// API unreachable (no creds, offline, outage): fall back to the
	// embedded snapshot. This is always shippable, never empty.
	regs, err := readSnapshot()
	if err != nil {
		// Snapshot is embedded at compile-time; a parse failure would
		// be a build-broken release, not a runtime condition, but we
		// still log it rather than panic.
		trace.FromContext(ctx).ErrorContext(ctx, "regions snapshot parse failed",
			slog.Any("err", err))
		return nil, "empty"
	}
	return regs, "snapshot"
}

// RegionIDs is a thin helper returning just the IDs in canonical sort
// order. Replaces the old package-level SupportedRegions() for hot-path
// callers that hold a *Client.
func (c *Client) RegionIDs(ctx context.Context) ([]string, error) {
	regs, err := c.Regions(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(regs))
	for _, r := range regs {
		ids = append(ids, r.ID)
	}
	return prioritizeRegions(ids, c.regionHints), nil
}

// RegionLocation returns the human-readable location name for an ID,
// or "" if unknown. Uses the Client's cached region list.
func (c *Client) RegionLocation(ctx context.Context, id string) string {
	regs, err := c.Regions(ctx)
	if err != nil {
		return ""
	}
	for _, r := range regs {
		if r.ID == id {
			return r.DisplayName
		}
	}
	return ""
}

// RegionGroup returns the friendly group name ("US", "Europe", …) for
// an ID, derived from the continent code with a small override table
// for UX-visible sub-groupings (ca-* → "Canada").
func (c *Client) RegionGroup(ctx context.Context, id string) string {
	if override, ok := idPrefixGroupOverrides[regionIDPrefix(id)]; ok {
		return override
	}
	regs, err := c.Regions(ctx)
	if err == nil {
		for _, r := range regs {
			if r.ID == id {
				if g, ok := continentGroupLabels[r.Continent]; ok {
					return g
				}
			}
		}
	}
	// Unknown region, or cache load failed entirely: degrade gracefully
	// to uppercased prefix. Matches the old behavior for "xx-foo-1".
	return strings.ToUpper(regionIDPrefix(id))
}

// fetchRegionsAPI makes the live GetRegions call. The Client may be
// unpinned (global mode), in which case we build a one-off lightsail
// client pointed at the caller's best-guess region (hint → us-east-1).
// The Include* flags stay false: we don't use AZ data anywhere today
// and the payload is meaningfully smaller without it.
func (c *Client) fetchRegionsAPI(ctx context.Context) ([]Region, error) {
	svc := c.ls
	if svc == nil {
		// Global Client: build a one-off regional lightsail client so
		// the SDK has a signing region. GetRegions itself returns the
		// same global list regardless of which region services the call.
		cfg := c.cfg.Copy()
		cfg.Region = c.fetchSourceRegion()
		if cfg.Region == "" {
			return nil, errors.New("no region hint available for GetRegions")
		}
		// If the cfg was built with empty creds (no AWS creds, --help
		// path), GetRegions will fail below and the caller falls
		// through to the snapshot.
		svc = lightsail.NewFromConfig(cfg)
	}
	out, err := svc.GetRegions(ctx, &lightsail.GetRegionsInput{})
	if err != nil {
		return nil, fmt.Errorf("GetRegions: %w", err)
	}
	return fromAPIRegions(out.Regions), nil
}

// fetchSourceRegion picks the region the one-off GetRegions call will
// target. Order: pinned region, first RegionHint, us-east-1 fallback.
// us-east-1 is always reachable in the aws partition (where Lightsail
// lives) and is part of the Lightsail GA set, so it's a safe backstop.
func (c *Client) fetchSourceRegion() string {
	if c.cfg.Region != "" {
		return c.cfg.Region
	}
	for _, h := range c.regionHints {
		if h != "" {
			return h
		}
	}
	return "us-east-1"
}

// fromAPIRegions projects the SDK's fat Region struct into our slim one.
// We deliberately drop AvailabilityZones / RelationalDatabaseAZs — both
// are never requested (Include* = false) and never consumed.
func fromAPIRegions(in []types.Region) []Region {
	out := make([]Region, 0, len(in))
	for _, r := range in {
		out = append(out, Region{
			ID:          string(r.Name),
			DisplayName: aws.ToString(r.DisplayName),
			Continent:   aws.ToString(r.ContinentCode),
			Description: aws.ToString(r.Description),
		})
	}
	return out
}

// readDiskCache loads regions.json from the user cache dir. Returns
// (nil, false) on any failure: file missing, version mismatch, TTL
// expired, parse error, empty list. Callers treat a miss as "refetch";
// we deliberately do not bubble the error because none of these cases
// are interesting to users.
func readDiskCache(ctx context.Context) ([]Region, bool) {
	path, err := regionsCachePath()
	if err != nil {
		return nil, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var raw diskCacheJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		// Corrupt cache is promoted to WARN: this is a real data
		// problem (an invalid file on disk) that callers need to
		// know about, even though we degrade gracefully.
		trace.FromContext(ctx).WarnContext(ctx, "regions cache corrupt",
			slog.String("path", path), slog.Any("err", err))
		return nil, false
	}
	if raw.Version != diskCacheVersion {
		return nil, false
	}
	if time.Since(raw.FetchedAt) > effectiveTTL() {
		return nil, false
	}
	if len(raw.Regions) == 0 {
		return nil, false
	}
	return fromJSONRegions(raw.Regions), true
}

// writeDiskCache atomically writes regions.json. Concurrent writers
// are safe: each writes to its own temp file and renames. Last writer
// wins; the content is identical at steady state so interleaving is
// harmless.
func writeDiskCache(regs []Region, sourceRegion string) error {
	dir, err := regionsCacheDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	payload := diskCacheJSON{
		Version:      diskCacheVersion,
		FetchedAt:    time.Now().UTC(),
		SourceRegion: sourceRegion,
		Regions:      toJSONRegions(regs),
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	// Temp in the same dir so os.Rename is atomic on the same volume.
	f, err := os.CreateTemp(dir, "regions.json.*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	path, err := regionsCachePath()
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// readSnapshot parses the embedded regions_snapshot.json. It's the
// build-time record of what GetRegions returned when the release
// shipped; every binary carries its own copy.
func readSnapshot() ([]Region, error) {
	var raw diskCacheJSON
	if err := json.Unmarshal(regionsSnapshotRaw, &raw); err != nil {
		return nil, fmt.Errorf("parse embedded regions snapshot: %w", err)
	}
	if len(raw.Regions) == 0 {
		return nil, errors.New("embedded regions snapshot is empty")
	}
	return fromJSONRegions(raw.Regions), nil
}

func fromJSONRegions(in []regionJS) []Region {
	out := make([]Region, 0, len(in))
	for _, r := range in {
		out = append(out, Region(r))
	}
	return out
}

func toJSONRegions(in []Region) []regionJS {
	out := make([]regionJS, 0, len(in))
	for _, r := range in {
		out = append(out, regionJS(r))
	}
	return out
}

// sortRegions imposes the canonical picker order: group (alphabetized
// by friendly name) then ID within group. Stable so same-group,
// same-ID inputs stay put.
func sortRegions(regs []Region) {
	sort.SliceStable(regs, func(i, j int) bool {
		gi := groupForRegion(regs[i])
		gj := groupForRegion(regs[j])
		if gi != gj {
			return gi < gj
		}
		return regs[i].ID < regs[j].ID
	})
}

// groupForRegion is the sort key helper: applies the ID-prefix override
// first (so ca-* gets its own "Canada" bucket), then falls back to the
// continent-code table, then to the uppercased prefix.
func groupForRegion(r Region) string {
	if override, ok := idPrefixGroupOverrides[regionIDPrefix(r.ID)]; ok {
		return override
	}
	if g, ok := continentGroupLabels[r.Continent]; ok {
		return g
	}
	return strings.ToUpper(regionIDPrefix(r.ID))
}

// regionIDPrefix returns the first dash-separated segment of a region
// ID ("us-east-1" → "us"). Used by the override table and as the
// unknown-region fallback.
func regionIDPrefix(id string) string {
	if i := strings.Index(id, "-"); i > 0 {
		return id[:i]
	}
	return id
}

// prioritizeRegions moves each region in hints (that exists in all) to
// the front, preserving the order of hints. Others follow in all's
// original order. Moved from client.go when FetchRegions retired;
// preserved byte-for-byte so TestPrioritizeRegions still passes.
func prioritizeRegions(all, hints []string) []string {
	if len(hints) == 0 {
		return all
	}
	inHints := map[string]bool{}
	for _, h := range hints {
		inHints[h] = true
	}
	present := map[string]bool{}
	for _, r := range all {
		present[r] = true
	}
	out := make([]string, 0, len(all))
	for _, h := range hints {
		if present[h] {
			out = append(out, h)
		}
	}
	for _, r := range all {
		if !inHints[r] {
			out = append(out, r)
		}
	}
	return out
}

// ---------------------------------------------------------------------
// Package-level wrappers (non-breaking escape hatch, plan §2.1).
//
// These preserve the `lightsail.RegionXxx(id)` call-site shape used by
// code that holds an ID but no *Client (nothing today, but kept for the
// `--help` path documented in main.go which must never require AWS creds
// or network). They consult the embedded snapshot ONLY — never the disk
// cache, never the API — so they are pure, deterministic, and safe to
// call from any init() or help string.
// ---------------------------------------------------------------------

var snapshotOnce sync.Once
var snapshotRegions []Region

func loadSnapshot() []Region {
	snapshotOnce.Do(func() {
		if regs, err := readSnapshot(); err == nil {
			sortRegions(regs)
			snapshotRegions = regs
		}
	})
	return snapshotRegions
}

// SupportedRegions returns the region IDs baked into the binary at
// build time. Deprecated: prefer Client.RegionIDs(ctx), which reflects
// the live AWS answer. Kept for callers that have no *Client handy.
func SupportedRegions() []string {
	regs := loadSnapshot()
	out := make([]string, 0, len(regs))
	for _, r := range regs {
		out = append(out, r.ID)
	}
	return out
}

// RegionLocation returns the display name for a region from the
// embedded snapshot, or "" if unknown. Deprecated: prefer
// Client.RegionLocation(ctx, id).
func RegionLocation(id string) string {
	for _, r := range loadSnapshot() {
		if r.ID == id {
			return r.DisplayName
		}
	}
	return ""
}

// RegionGroup returns the friendly group label for a region using only
// the embedded snapshot + the local override/continent tables.
// Deprecated: prefer Client.RegionGroup(ctx, id).
func RegionGroup(id string) string {
	if override, ok := idPrefixGroupOverrides[regionIDPrefix(id)]; ok {
		return override
	}
	for _, r := range loadSnapshot() {
		if r.ID == id {
			if g, ok := continentGroupLabels[r.Continent]; ok {
				return g
			}
		}
	}
	return strings.ToUpper(regionIDPrefix(id))
}

// SortRegionsByGroup sorts region IDs using the same group→ID ordering
// the live path uses. Preserves byte-for-byte picker output for any
// caller still wired to the old API. Deprecated: prefer Client.Regions
// (already sorted) + a projection loop.
func SortRegionsByGroup(ids []string) []string {
	out := make([]string, len(ids))
	copy(out, ids)
	sort.SliceStable(out, func(i, j int) bool {
		gi, gj := RegionGroup(out[i]), RegionGroup(out[j])
		if gi != gj {
			return gi < gj
		}
		return out[i] < out[j]
	})
	return out
}
