package lightsail

import (
	"context"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/lightsail"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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
type KeyCache struct {
	mu       sync.Mutex
	keys     map[string]*cachedKey
	closed   bool
	deleteFn KeyDeleteFunc
}

// NewKeyCache creates a cache that will call deleteFn for each key on Close.
func NewKeyCache(deleteFn KeyDeleteFunc) *KeyCache {
	return &KeyCache{keys: map[string]*cachedKey{}, deleteFn: deleteFn}
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
	for bucket, ck := range snapshot {
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
