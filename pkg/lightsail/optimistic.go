package lightsail

import (
	"sync"
	"time"
)

// optimisticBucket is a bucket a caller just created. We cache it locally
// for a short window so ListBuckets returns it immediately even if the
// Lightsail API hasn't yet propagated the create. This papers over the
// "I just created this app but it doesn't show up in the list" UX wart.
type optimisticBucket struct {
	bucket Bucket
	expiry time.Time
}

// optimisticCache stores ephemeral 'I just did this, trust me' facts so
// ListBuckets / ListAppBuckets can simulate read-after-write consistency
// locally during the propagation window.
type optimisticCache struct {
	mu      sync.Mutex
	buckets []optimisticBucket
}

// addBucket remembers a bucket for the given TTL.
func (o *optimisticCache) addBucket(b Bucket, ttl time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.purgeLocked()
	// Replace any existing entry with the same name.
	for i, eb := range o.buckets {
		if eb.bucket.Name == b.Name {
			o.buckets[i] = optimisticBucket{bucket: b, expiry: time.Now().Add(ttl)}
			return
		}
	}
	o.buckets = append(o.buckets, optimisticBucket{bucket: b, expiry: time.Now().Add(ttl)})
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
		if _, ok := seen[eb.bucket.Name]; ok {
			continue
		}
		out = append(out, eb.bucket)
	}
	return out
}

// forget removes a bucket from the cache (called on delete).
func (o *optimisticCache) forget(name string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	kept := o.buckets[:0]
	for _, eb := range o.buckets {
		if eb.bucket.Name != name {
			kept = append(kept, eb)
		}
	}
	o.buckets = kept
}

func (o *optimisticCache) purgeLocked() {
	now := time.Now()
	kept := o.buckets[:0]
	for _, eb := range o.buckets {
		if eb.expiry.After(now) {
			kept = append(kept, eb)
		}
	}
	o.buckets = kept
}
