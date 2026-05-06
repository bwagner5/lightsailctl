package lightsail

import (
	"context"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// cachedKey holds a minted bucket access key and its pre-built S3 client.
type cachedKey struct {
	key    *BucketKey
	s3cli  *s3.Client
	region string
}

// KeyDeleteFunc is called by Close to release each cached key.
// Signature matches the cleanup needs: (ctx, region, bucket, accessKeyID, s3cli).
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
	// Snapshot and clear under lock, then release outside lock.
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
