// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package lightsail

import (
	"context"
	"sync"
	"time"
)

// statusTTL is how long a memoized FetchBucketStatuses result is considered
// fresh. Three async loaders (Status, Instances, Endpoints) fire
// approximately simultaneously on every TUI refresh for the same env
// bucket; the TTL only needs to cover the worst-case loader start-time
// skew, which is milliseconds. 5 s is conservative and ensures that the
// second and third loaders hit the memo while guaranteeing the very next
// refresh (TUI polls every 10 s) re-fetches for real.
const statusTTL = 5 * time.Second

// statusesCache memoizes ReadBucketStatuses per bucket. One entry per
// bucket; the entry's mutex serializes concurrent refreshes so only
// one real S3 round-trip happens per bucket per TTL window. Safe across
// WithRegion copies (held via pointer on Client).
type statusesCache struct {
	m sync.Map // bucket → *statusesEntry
}

type statusesEntry struct {
	mu       sync.Mutex
	statuses []Status
	err      error
	loadedAt time.Time
}

var sharedStatusesCache = &statusesCache{}

// FetchBucketStatuses returns the bucket's parsed *_status.json objects,
// cached for statusTTL. When multiple callers hit the same bucket within
// the TTL the first does the work and the rest share the result; a call
// past the TTL boundary does a fresh fetch under the entry's mutex so
// concurrent callers at expiry still produce only one S3 round-trip.
//
// Propagates the error from ReadBucketStatuses — the cache memoizes
// failures too, on the theory that a broken bucket on one loader will
// still be broken on the next two within ~ms.
func (c *Client) FetchBucketStatuses(ctx context.Context, bucket string) ([]Status, error) {
	cache := c.statuses
	if cache == nil {
		// Defensive: callers that built a Client outside NewWithOptions.
		// Fall back to a direct read; no memoization.
		return c.ReadBucketStatuses(ctx, bucket)
	}
	v, _ := cache.m.LoadOrStore(bucket, &statusesEntry{})
	e := v.(*statusesEntry)
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.loadedAt.IsZero() && time.Since(e.loadedAt) < statusTTL {
		return e.statuses, e.err
	}
	statuses, err := c.ReadBucketStatuses(ctx, bucket)
	e.statuses = statuses
	e.err = err
	e.loadedAt = time.Now()
	return statuses, err
}
