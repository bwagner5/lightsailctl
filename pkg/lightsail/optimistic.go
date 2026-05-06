package lightsail

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bwagner5/triad/pkg/trace"
)

// sharedOptimisticCache is a process-level cache consulted by every
// Client instance. Hoisted out of per-Client state because callers that
// change region during a saga (pinRegion) rebuild the Client, and the
// app-list TUI uses a different store/Client than the deploy saga.
// Sharing keeps read-after-write consistency across all of them.
//
// Backed by a small JSON file in the user cache dir so the optimistic
// view survives across process boundaries — a CLI 'deploy' that exits
// and then a TUI launch still sees the just-created bucket.
var sharedOptimisticCache = func() *optimisticCache {
	o := &optimisticCache{path: cachePath()}
	o.loadLocked()
	return o
}()

func cachePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "lightsailctl", "recent-buckets.json")
}

// optimisticBucket is a bucket a caller just created. We cache it locally
// for a short window so ListBuckets returns it immediately even if the
// Lightsail API hasn't yet propagated the create. This papers over the
// "I just created this app but it doesn't show up in the list" UX wart.
// Exported-ish field names so encoding/json round-trips when we persist.
type optimisticBucket struct {
	Bucket Bucket    `json:"bucket"`
	Expiry time.Time `json:"expiry"`
}

// optimisticCache stores ephemeral 'I just did this, trust me' facts so
// ListBuckets / ListAppBuckets can simulate read-after-write consistency
// locally during the propagation window. Persisted to disk when path is
// set so multiple processes (CLI deploy -> TUI) see the same view.
type optimisticCache struct {
	mu      sync.Mutex
	buckets []optimisticBucket
	path    string
}

// addBucket remembers a bucket for the given TTL.
func (o *optimisticCache) addBucket(b Bucket, ttl time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.purgeLocked()
	// Replace any existing entry with the same name.
	for i, eb := range o.buckets {
		if eb.Bucket.Name == b.Name {
			o.buckets[i] = optimisticBucket{Bucket: b, Expiry: time.Now().Add(ttl)}
			o.saveLocked()
			return
		}
	}
	o.buckets = append(o.buckets, optimisticBucket{Bucket: b, Expiry: time.Now().Add(ttl)})
	o.saveLocked()
}

// merge appends any cached buckets not already present in live (matched
// by Name) and returns the combined slice. live is returned unchanged
// when the cache is empty.
func (o *optimisticCache) merge(live []Bucket) []Bucket {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.purgeLocked()
	if len(o.buckets) == 0 {
		return live
	}
	seen := map[string]struct{}{}
	for _, b := range live {
		seen[b.Name] = struct{}{}
	}
	out := live
	for _, eb := range o.buckets {
		if _, ok := seen[eb.Bucket.Name]; ok {
			continue
		}
		out = append(out, eb.Bucket)
	}
	return out
}

// forget removes a bucket from the cache (called on delete).
func (o *optimisticCache) forget(name string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	kept := o.buckets[:0]
	for _, eb := range o.buckets {
		if eb.Bucket.Name != name {
			kept = append(kept, eb)
		}
	}
	o.buckets = kept
	o.saveLocked()
}

func (o *optimisticCache) purgeLocked() {
	now := time.Now()
	kept := o.buckets[:0]
	for _, eb := range o.buckets {
		if eb.Expiry.After(now) {
			kept = append(kept, eb)
		}
	}
	o.buckets = kept
}

// loadLocked reads the cache file into memory. Missing / malformed file is
// treated as empty (we're an ephemeral cache; best-effort is fine).
func (o *optimisticCache) loadLocked() {
	if o.path == "" {
		return
	}
	data, err := os.ReadFile(o.path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &o.buckets)
	o.purgeLocked()
}

// saveLocked writes the in-memory state to disk. Caller must hold o.mu.
// Disk writes are best-effort; a failure (e.g. read-only home) just
// means subsequent processes won't see the cached entries.
func (o *optimisticCache) saveLocked() {
	if o.path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(o.path), 0o755)
	data, err := json.Marshal(o.buckets)
	if err != nil {
		return
	}
	_ = os.WriteFile(o.path, data, 0o644)
}

// ForgetOptimistic removes a bucket from the optimistic cache (called
// after a successful delete so the table doesn't keep surfacing a dead
// entry for the remainder of the TTL).
func (c *Client) ForgetOptimistic(name string) {
	if c.optimistic != nil {
		c.optimistic.forget(name)
	}
}

// AnnounceBucket pre-publishes an optimistic cache entry in PENDING
// state, so a 'I just started creating' announcement makes the app
// visible in listing views before any real AWS create returns. Callers
// that also finish CreateBucket will upgrade the entry to OK state.
func (c *Client) AnnounceBucket(ctx context.Context, name, region string) {
	if c.optimistic == nil || name == "" {
		return
	}
	trace.Trace(ctx, "lightsail announce bucket", "name", name, "region", region, "path", c.optimistic.path)
	c.optimistic.addBucket(Bucket{Name: name, State: "PENDING", Region: region}, 10*time.Minute)
}
