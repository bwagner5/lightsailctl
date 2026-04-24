package lightsail

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lightsail"
)

// Bucket is our slim view of a Lightsail bucket.
type Bucket struct {
	Name   string
	State  string
	Region string
}

// ListBuckets returns every Lightsail bucket visible to the caller in the
// client's configured region. No filtering is applied — callers that only
// want application buckets filter on BucketPrefix.
func (c *Client) ListBuckets(ctx context.Context) ([]Bucket, error) {
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
