// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package lightsail

import (
	"context"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestKeyCachePutGet(t *testing.T) {
	kc := NewKeyCache(nil)
	cli := &s3.Client{}
	kc.Put("my-bucket", &BucketKey{Bucket: "my-bucket", AccessKey: "AK"}, cli, "us-east-1")

	got, ok := kc.Get("my-bucket")
	if !ok || got != cli {
		t.Fatal("expected cache hit")
	}
	if _, ok := kc.Get("other"); ok {
		t.Fatal("expected cache miss for unknown bucket")
	}
}

func TestKeyCacheCloseCallsDeleteFn(t *testing.T) {
	var mu sync.Mutex
	deleted := map[string]string{}
	deleteFn := func(_ context.Context, region, bucket, accessKeyID string, _ *s3.Client) {
		mu.Lock()
		deleted[bucket] = accessKeyID
		mu.Unlock()
	}
	kc := NewKeyCache(deleteFn)
	kc.Put("b1", &BucketKey{Bucket: "b1", AccessKey: "AK1"}, &s3.Client{}, "us-east-1")
	kc.Put("b2", &BucketKey{Bucket: "b2", AccessKey: "AK2"}, &s3.Client{}, "us-west-2")

	kc.Close()

	if len(deleted) != 2 {
		t.Fatalf("expected 2 deletes, got %d", len(deleted))
	}
	if deleted["b1"] != "AK1" || deleted["b2"] != "AK2" {
		t.Fatalf("unexpected deletes: %v", deleted)
	}
}

func TestKeyCacheAfterClose(t *testing.T) {
	kc := NewKeyCache(nil)
	kc.Close()

	// Put after close is a no-op.
	kc.Put("b", &BucketKey{Bucket: "b", AccessKey: "AK"}, &s3.Client{}, "us-east-1")
	if _, ok := kc.Get("b"); ok {
		t.Fatal("expected miss after close")
	}
}

func TestKeyCacheWarmSkipsCached(t *testing.T) {
	kc := NewKeyCache(nil)
	existing := &s3.Client{}
	kc.Put("b1", &BucketKey{Bucket: "b1", AccessKey: "AK1"}, existing, "us-east-1")

	// Warm with b1 should not overwrite the existing entry.
	// We can't easily test CreateBucketKey without a real client, but we
	// can verify the existing entry is preserved by checking Get after Warm
	// completes (Warm will fail silently on the nil client for b2).
	ctx := context.Background()
	kc.Warm(ctx, nil, []string{"b1"})

	// Give goroutine a moment to run (it should skip b1 immediately).
	got, ok := kc.Get("b1")
	if !ok || got != existing {
		t.Fatal("Warm should not overwrite existing cached entry")
	}
}
