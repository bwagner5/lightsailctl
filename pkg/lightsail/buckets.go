package lightsail

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lightsail"
)

// Bucket is our slim view of a Lightsail bucket.
type Bucket struct {
	Name   string
	State  string
	Region string
}

// ListBuckets returns every Lightsail bucket visible to the caller.
//
// When the Client is pinned to a region (WithRegion or --region), lists
// that region only. When unpinned (the default TUI/list path), fans out
// across every region and returns the union. Lightsail is not enabled in
// every region; per-region errors are silently skipped so one dead region
// doesn't kill the global list.
func (c *Client) ListBuckets(ctx context.Context) ([]Bucket, error) {
	if c.Region() == "" {
		return c.listBucketsGlobal(ctx)
	}
	out, err := c.ls.GetBuckets(ctx, &lightsail.GetBucketsInput{})
	if err != nil {
		return nil, err
	}
	buckets := make([]Bucket, 0, len(out.Buckets))
	for _, b := range out.Buckets {
		bkt := Bucket{Name: aws.ToString(b.Name)}
		if b.State != nil {
			bkt.State = aws.ToString(b.State.Code)
		}
		if b.Location != nil {
			bkt.Region = string(b.Location.RegionName)
		}
		buckets = append(buckets, bkt)
	}
	return buckets, nil
}

// StreamBuckets fans out ListBuckets across regions and pushes one batch
// per region as it completes. When the Client is pinned, emits a single
// batch and closes. The channel is closed when all regions have reported.
// Per-region errors are delivered as Batch{Err}; caller decides how to
// surface them.
func (c *Client) StreamBuckets(ctx context.Context) <-chan BucketBatch {
	out := make(chan BucketBatch, 16)
	if c.Region() != "" {
		go func() {
			defer close(out)
			bs, err := c.ListBuckets(ctx)
			out <- BucketBatch{Region: c.Region(), Buckets: bs, Err: err}
		}()
		return out
	}
	go func() {
		defer close(out)
		regions, err := c.FetchRegions(ctx)
		if err != nil {
			out <- BucketBatch{Err: err}
			return
		}
		var wg sync.WaitGroup
		for _, r := range regions {
			wg.Add(1)
			go func(region string) {
				defer wg.Done()
				bs, err := c.WithRegion(region).ListBuckets(ctx)
				select {
				case <-ctx.Done():
				case out <- BucketBatch{Region: region, Buckets: bs, Err: err}:
				}
			}(r)
		}
		wg.Wait()
	}()
	return out
}

// BucketBatch is one region's contribution to a StreamBuckets call.
type BucketBatch struct {
	Region  string
	Buckets []Bucket
	Err     error
}

// listBucketsGlobal fans out ListBuckets across every AWS region.
func (c *Client) listBucketsGlobal(ctx context.Context) ([]Bucket, error) {
	var out []Bucket
	for b := range c.StreamBuckets(ctx) {
		if b.Err != nil {
			continue // Lightsail not enabled here, or transient; skip.
		}
		out = append(out, b.Buckets...)
	}
	return out, nil
}

// ListAppBuckets returns only the buckets that match our naming scheme
// (ls--<acct>--<app>[--<env>]).
func (c *Client) ListAppBuckets(ctx context.Context) ([]Bucket, error) {
	all, err := c.ListBuckets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Bucket, 0, len(all))
	for _, b := range all {
		if strings.HasPrefix(b.Name, BucketPrefix) {
			out = append(out, b)
		}
	}
	return out, nil
}

// CreateBucket creates a Lightsail bucket with the small bundle, then
// blocks until the bucket reports state=OK (or timeout). Lightsail
// CreateBucket returns before the bucket is actually usable, so callers
// that will immediately mint access keys or call SetResourceAccessForBucket
// would otherwise hit eventual-consistency errors. No-op (nil error) if
// the bucket already exists.
func (c *Client) CreateBucket(ctx context.Context, name string) error {
	bundle := DefaultBundle
	_, err := c.ls.CreateBucket(ctx, &lightsail.CreateBucketInput{
		BucketName: &name,
		BundleId:   &bundle,
	})
	if err != nil && !isAlreadyExists(err) {
		return err
	}
	return c.WaitForBucketReady(ctx, name)
}

// WaitForBucketReady polls GetBucket until its state.code == "OK" or a
// 3-minute timeout elapses. Bucket creation is eventually-consistent in
// Lightsail; in practice it takes ~15-60s to become usable.
func (c *Client) WaitForBucketReady(ctx context.Context, name string) error {
	deadline := time.Now().Add(3 * time.Minute)
	delay := 2 * time.Second
	for {
		out, err := c.ls.GetBuckets(ctx, &lightsail.GetBucketsInput{
			BucketName: aws.String(name),
		})
		if err == nil {
			for _, b := range out.Buckets {
				if b.Name != nil && *b.Name == name && b.State != nil {
					if aws.ToString(b.State.Code) == "OK" {
						return nil
					}
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("bucket %q not ready after 3m", name)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if delay < 10*time.Second {
			delay += 2 * time.Second
		}
	}
}

func isAlreadyExists(err error) bool {
	s := err.Error()
	for _, needle := range []string{"already exists", "Bucket with name"} {
		if strings.Contains(strings.ToLower(s), strings.ToLower(needle)) {
			return true
		}
	}
	return false
}
