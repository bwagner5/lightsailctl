package lightsail

import (
	"context"
	"strings"
	"sync"

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

// listBucketsGlobal fans out ListBuckets across every AWS region.
func (c *Client) listBucketsGlobal(ctx context.Context) ([]Bucket, error) {
	regions, err := c.FetchRegions(ctx)
	if err != nil {
		return nil, err
	}
	var (
		mu  sync.Mutex
		out []Bucket
		wg  sync.WaitGroup
	)
	for _, r := range regions {
		wg.Add(1)
		go func(region string) {
			defer wg.Done()
			rc := c.WithRegion(region)
			bs, err := rc.ListBuckets(ctx)
			if err != nil {
				return // Lightsail not enabled here, or transient error; skip.
			}
			mu.Lock()
			out = append(out, bs...)
			mu.Unlock()
		}(r)
	}
	wg.Wait()
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

// CreateBucket creates a Lightsail bucket with the small bundle. No-op (nil
// error) if the bucket already exists.
func (c *Client) CreateBucket(ctx context.Context, name string) error {
	bundle := DefaultBundle
	_, err := c.ls.CreateBucket(ctx, &lightsail.CreateBucketInput{
		BucketName: &name,
		BundleId:   &bundle,
	})
	if err != nil && isAlreadyExists(err) {
		return nil
	}
	return err
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
