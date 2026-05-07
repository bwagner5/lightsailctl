// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package lightsail

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestStatusesCacheSingleflight exercises the memo directly — we stub
// the loader and assert that N concurrent callers within the TTL
// produce exactly 1 underlying invocation. The real FetchBucketStatuses
// method routes through c.ReadBucketStatuses which needs a live AWS
// client; we instead exercise the cache's entry/TTL logic directly.
func TestStatusesCacheSingleflight(t *testing.T) {
	cache := &statusesCache{}
	const bucket = "ls--acct--foo--dev"

	var calls int32
	load := func() ([]Status, error) {
		atomic.AddInt32(&calls, 1)
		// Simulate real work so concurrent callers queue on the entry
		// mutex and exercise the double-check TTL branch.
		time.Sleep(20 * time.Millisecond)
		return []Status{{Instance: "vm-1"}}, nil
	}

	// Inline re-implementation of FetchBucketStatuses' cache flow so
	// the test is hermetic. The production method's first two lines
	// (nil-cache defensive path + LoadOrStore) are identical.
	fetch := func() ([]Status, error) {
		v, _ := cache.m.LoadOrStore(bucket, &statusesEntry{})
		e := v.(*statusesEntry)
		e.mu.Lock()
		defer e.mu.Unlock()
		if !e.loadedAt.IsZero() && time.Since(e.loadedAt) < statusTTL {
			return e.statuses, e.err
		}
		s, err := load()
		e.statuses = s
		e.err = err
		e.loadedAt = time.Now()
		return s, err
	}

	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := fetch(); err != nil {
				t.Errorf("unexpected err: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("loader invocations = %d, want 1 (singleflight)", got)
	}

	// Force expiry and re-fetch; next call should hit the loader once more.
	v, _ := cache.m.Load(bucket)
	e := v.(*statusesEntry)
	e.mu.Lock()
	e.loadedAt = time.Now().Add(-2 * statusTTL)
	e.mu.Unlock()

	if _, err := fetch(); err != nil {
		t.Fatalf("unexpected err after expiry: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("loader invocations after expiry = %d, want 2", got)
	}
}
