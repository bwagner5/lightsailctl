// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package lightsail

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/lightsail"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bwagner5/triad/pkg/trace"
)

// cachedKey holds a minted bucket access key and its pre-built S3 client.
type cachedKey struct {
	key    *BucketKey
	s3cli  *s3.Client
	region string
}

// KeyDeleteFunc is called by Close to release each cached key.
type KeyDeleteFunc func(ctx context.Context, region, bucket, accessKeyID string, s3cli *s3.Client)

// KeyCache holds pre-fetched bucket access keys so S3ClientFor can skip
// the expensive CreateBucketAccessKey round-trip on cache hit.
//
// Concurrency model: the cache also serializes concurrent mints for the
// same bucket (singleflight). Without it, N concurrent S3ClientFor
// callers on a cold-cache bucket all observe the miss and all call
// CreateBucketAccessKey — Lightsail accepts up to 2, and the remaining
// N-2 get "reached the limit" errors. Even the two that succeed only
// record the last Put in the cache, leaking the other key in Lightsail.
type KeyCache struct {
	mu       sync.Mutex
	keys     map[string]*cachedKey
	pending  map[string]chan struct{} // in-flight mints; waiters select on the chan closing
	closed   bool
	deleteFn KeyDeleteFunc
}

// NewKeyCache creates a cache that will call deleteFn for each key on Close.
func NewKeyCache(deleteFn KeyDeleteFunc) *KeyCache {
	return &KeyCache{
		keys:     map[string]*cachedKey{},
		pending:  map[string]chan struct{}{},
		deleteFn: deleteFn,
	}
}

// Put stores a key for the given bucket. No-op if the cache is closed.
func (kc *KeyCache) Put(bucket string, key *BucketKey, s3cli *s3.Client, region string) {
	kc.mu.Lock()
	defer kc.mu.Unlock()
	if kc.closed {
		return
	}
	kc.keys[bucket] = &cachedKey{key: key, s3cli: s3cli, region: region}
}

// Get returns the cached S3 client for a bucket, or (nil, false) on miss.
func (kc *KeyCache) Get(bucket string) (*s3.Client, bool) {
	kc.mu.Lock()
	defer kc.mu.Unlock()
	if kc.closed {
		return nil, false
	}
	ck, ok := kc.keys[bucket]
	if !ok {
		return nil, false
	}
	return ck.s3cli, true
}

// ErrKeyCacheClosed is returned by getOrMint when the cache has been
// closed (process is shutting down).
var ErrKeyCacheClosed = errors.New("key cache is closed")

// getOrMint returns a cached S3 client for bucket, minting one on miss.
// At most one CreateBucketAccessKey call is in flight per bucket; other
// callers block on the same pending chan and read the cache once the
// first completes. The key is inserted into the cache BEFORE writeOwnership
// runs (writeOwnership is spawned in the background), so that a shutdown
// or cancellation during the ownership-marker write cannot leak the key
// — Close will still see the entry and delete it.
func (kc *KeyCache) getOrMint(ctx context.Context, c *Client, bucket string) (*s3.Client, error) {
	for {
		kc.mu.Lock()
		if kc.closed {
			kc.mu.Unlock()
			return nil, ErrKeyCacheClosed
		}
		if ck, ok := kc.keys[bucket]; ok {
			kc.mu.Unlock()
			trace.Trace(ctx, "key cache hit", "bucket", bucket)
			return ck.s3cli, nil
		}
		if ch, ok := kc.pending[bucket]; ok {
			kc.mu.Unlock()
			trace.Trace(ctx, "key cache wait for pending mint", "bucket", bucket)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-ch:
				// The mint completed (success or failure). Loop to re-check
				// the cache; on success we hit, on failure the pending slot
				// is gone and we'll take the mint ourselves.
				continue
			}
		}
		// We own this mint.
		ch := make(chan struct{})
		kc.pending[bucket] = ch
		kc.mu.Unlock()
		trace.Trace(ctx, "key cache mint start", "bucket", bucket, "region", c.cfg.Region)

		key, mintErr := c.mintBucketKeyRaw(ctx, bucket)
		var s3cli *s3.Client
		if mintErr == nil {
			s3cli = s3AdminClient(c.cfg.Region, key)
		}

		kc.mu.Lock()
		delete(kc.pending, bucket)
		closedNow := kc.closed
		if mintErr == nil && !closedNow {
			kc.keys[bucket] = &cachedKey{key: key, s3cli: s3cli, region: c.cfg.Region}
		}
		kc.mu.Unlock()
		close(ch)

		if mintErr != nil {
			trace.Trace(ctx, "key cache mint failed", "bucket", bucket, "region", c.cfg.Region, "err", mintErr)
			return nil, mintErr
		}
		if closedNow {
			// Cache closed during mint: release the freshly-minted key so
			// it doesn't leak in Lightsail. Use a fresh context so the
			// cleanup isn't cancelled by the original caller's ctx.
			trace.Trace(ctx, "key cache closed during mint, releasing key", "bucket", bucket, "region", c.cfg.Region, "accessKeyID", key.AccessKey)
			if kc.deleteFn != nil {
				cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				kc.deleteFn(cctx, c.cfg.Region, bucket, key.AccessKey, s3cli)
				cancel()
			}
			return nil, ErrKeyCacheClosed
		}
		trace.Trace(ctx, "key cache mint success", "bucket", bucket, "region", c.cfg.Region, "accessKeyID", key.AccessKey)
		// Best-effort ownership marker, out-of-band so the caller isn't
		// blocked on S3 eventual-consistency retries (writeOwnership wraps
		// PutObject in RetryableLong, which can take up to ~5 min on a
		// cold bucket). The key is already cached: Close will reclaim it
		// even if this goroutine never finishes.
		go func() {
			wctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if werr := writeOwnership(wctx, c.cfg.Region, key); werr != nil {
				trace.Trace(ctx, "key ownership marker write failed", "bucket", bucket, "region", c.cfg.Region, "accessKeyID", key.AccessKey, "err", werr)
			} else {
				trace.Trace(ctx, "key ownership marker written", "bucket", bucket, "region", c.cfg.Region, "accessKeyID", key.AccessKey)
			}
		}()
		return s3cli, nil
	}
}

// Close releases all cached keys. Uses a fresh 30s context so a cancelled
// caller context doesn't skip cleanup. Errors are swallowed (best-effort).
func (kc *KeyCache) Close() {
	kc.mu.Lock()
	if kc.closed {
		kc.mu.Unlock()
		return
	}
	kc.closed = true
	snapshot := kc.keys
	kc.keys = map[string]*cachedKey{}
	kc.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	trace.Trace(ctx, "key cache closing", "keys", len(snapshot))
	for bucket, ck := range snapshot {
		trace.Trace(ctx, "key cache releasing key", "bucket", bucket, "region", ck.region, "accessKeyID", ck.key.AccessKey)
		if kc.deleteFn != nil {
			kc.deleteFn(ctx, ck.region, bucket, ck.key.AccessKey, ck.s3cli)
		}
	}
}

// sharedKeyCache is a process-level cache shared across all Client instances
// (including WithRegion copies). Close is called at process shutdown to
// release all held keys.
var sharedKeyCache = NewKeyCache(func(ctx context.Context, region, bucket, accessKeyID string, s3cli *s3.Client) {
	// Remove ownership marker first so racing clients see a freed slot.
	_ = deleteOwnership(ctx, s3cli, bucket, accessKeyID)
	// Delete the access key via Lightsail API. Best-effort.
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return
	}
	_, _ = lightsail.NewFromConfig(cfg).DeleteBucketAccessKey(ctx, &lightsail.DeleteBucketAccessKeyInput{
		BucketName:  aws.String(bucket),
		AccessKeyId: aws.String(accessKeyID),
	})
})

// CloseKeyCache releases all cached bucket access keys. Call on shutdown.
func (c *Client) CloseKeyCache() {
	if c.keyCache != nil {
		c.keyCache.Close()
	}
}

// WarmKeyCache pre-fetches access keys for the given buckets in the background.
// Errors are silently swallowed. Buckets already in the cache are skipped.
func (c *Client) WarmKeyCache(ctx context.Context, buckets []string) {
	if c.keyCache != nil {
		c.keyCache.Warm(ctx, c, buckets)
	}
}

// Warm pre-fetches access keys for buckets concurrently in the background.
// All errors (including key-limit exhaustion) are silently swallowed.
// Routes through getOrMint so it shares the singleflight with S3ClientFor.
func (kc *KeyCache) Warm(ctx context.Context, c *Client, buckets []string) {
	go func() {
		sem := make(chan struct{}, 5)
		var wg sync.WaitGroup
		for _, bucket := range buckets {
			select {
			case <-ctx.Done():
				return
			default:
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(b string) {
				defer wg.Done()
				defer func() { <-sem }()
				_, _ = kc.getOrMint(ctx, c, b)
			}(bucket)
		}
		wg.Wait()
	}()
}
