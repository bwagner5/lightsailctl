package lightsail

import (
	"testing"
	"time"
)

func TestOptimisticCache_MergeAdds(t *testing.T) {
	o := &optimisticCache{}
	o.addBucket(Bucket{Name: "ls--a--b--app-x", State: "OK", Region: "us-east-1"}, time.Minute)
	got := o.merge([]Bucket{{Name: "other", Region: "us-east-1"}})
	if len(got) != 2 {
		t.Fatalf("want 2 buckets, got %d: %+v", len(got), got)
	}
}

// TestOptimisticCache_MergeDeduplicates asserts live-API results take
// precedence (we don't double-count) once the API catches up.
func TestOptimisticCache_MergeDeduplicates(t *testing.T) {
	o := &optimisticCache{}
	o.addBucket(Bucket{Name: "dup", State: "OK", Region: "us-east-1"}, time.Minute)
	got := o.merge([]Bucket{{Name: "dup", State: "OK", Region: "us-east-1"}})
	if len(got) != 1 {
		t.Fatalf("want 1 bucket (dedup), got %d: %+v", len(got), got)
	}
}

func TestOptimisticCache_Expiry(t *testing.T) {
	o := &optimisticCache{}
	o.addBucket(Bucket{Name: "stale"}, -time.Second) // already expired
	got := o.merge(nil)
	if len(got) != 0 {
		t.Fatalf("expired entries must be purged, got %+v", got)
	}
}

func TestOptimisticCache_Forget(t *testing.T) {
	o := &optimisticCache{}
	o.addBucket(Bucket{Name: "gone"}, time.Minute)
	o.forget("gone")
	if got := o.merge(nil); len(got) != 0 {
		t.Fatalf("forget should remove entry, got %+v", got)
	}
}
