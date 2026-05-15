// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package lightsail

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bwagner5/triad/pkg/trace"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lightsail"
)

// Bucket is our slim view of a Lightsail bucket.
type Bucket struct {
	Name      string
	State     string
	Region    string
	CreatedAt time.Time
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
		out, err := c.listBucketsGlobal(ctx)
		if err != nil {
			return nil, err
		}
		if c.optimistic != nil {
			out = c.optimistic.merge(out)
		}
		return out, nil
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
		if b.CreatedAt != nil {
			bkt.CreatedAt = *b.CreatedAt
		}
		buckets = append(buckets, bkt)
	}
	if c.optimistic != nil {
		// Include optimistic entries ONLY when they match THIS region
		// exactly. An entry with an empty Region is ignored here — it
		// would otherwise surface in every region's ListBuckets and
		// produce N-way duplicates in the TUI (one row per region) as
		// each regional batch appends the same optimistic entry.
		merged := c.optimistic.merge(buckets)
		buckets = merged[:0:len(merged)]
		for _, b := range merged {
			if b.Region == c.cfg.Region {
				buckets = append(buckets, b)
			}
		}
	}
	trace.Trace(ctx, "lightsail list buckets regional", "region", c.cfg.Region, "count", len(buckets), "names", bucketNames(buckets))
	return buckets, nil
}

// bucketNames is a trace helper that returns just the names so log
// lines stay readable.
func bucketNames(bs []Bucket) []string {
	out := make([]string, 0, len(bs))
	for _, b := range bs {
		out = append(out, b.Name+"/"+b.State)
	}
	return out
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
		regions, err := c.RegionIDs(ctx)
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
				if err != nil {
					// Regions that require opt-in, or partial outages,
					// surface here as SDK errors. Log at INFO with the
					// region so the user can tell at a glance which
					// region produced each StatusCode 400 line. Caller
					// (StreamList) still swallows the Batch.Err for the
					// table render — this log is the diagnostic trail.
					trace.FromContext(ctx).InfoContext(ctx, "list buckets failed for region",
						"region", region, "err", err)
				}
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

// GetBucket looks up a single bucket by name via the live Lightsail API
// (GetBuckets with a BucketName filter). It deliberately bypasses the
// optimistic cache so callers that need authoritative "does this bucket
// actually exist?" answers (e.g. deploy's create-if-missing gate) are
// not fooled by a PENDING entry this process just announced.
//
// Returns (nil, nil) when the bucket does not exist. The client must be
// pinned to a region for this call; use a region-pinned sub-client
// (c.WithRegion) when the caller knows the region.
func (c *Client) GetBucket(ctx context.Context, name string) (*Bucket, error) {
	out, err := c.ls.GetBuckets(ctx, &lightsail.GetBucketsInput{
		BucketName: aws.String(name),
	})
	if err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	for _, b := range out.Buckets {
		if aws.ToString(b.Name) != name {
			continue
		}
		bkt := Bucket{Name: aws.ToString(b.Name)}
		if b.State != nil {
			bkt.State = aws.ToString(b.State.Code)
		}
		if b.Location != nil {
			bkt.Region = string(b.Location.RegionName)
		}
		if b.CreatedAt != nil {
			bkt.CreatedAt = *b.CreatedAt
		}
		return &bkt, nil
	}
	return nil, nil
}

// CreateBucket creates a Lightsail bucket with the small bundle, then
// blocks until the bucket reports state=OK (or timeout). Lightsail
// CreateBucket returns before the bucket is actually usable, so callers
// that will immediately mint access keys or call SetResourceAccessForBucket
// would otherwise hit eventual-consistency errors. No-op (nil error) if
// the bucket already exists.
func (c *Client) CreateBucket(ctx context.Context, name string) error {
	if err := ValidateBucketName(name); err != nil {
		return err
	}
	bundle := DefaultBundle
	_, err := c.ls.CreateBucket(ctx, &lightsail.CreateBucketInput{
		BucketName: &name,
		BundleId:   &bundle,
		// Tag every lightsailctl-created resource with the CLI
		// version that made it, so future upgrade tooling can
		// identify and migrate them.
		Tags: lightsailTagsFromMap(DefaultResourceTags()),
	})
	if err != nil && !isAlreadyExists(err) {
		return err
	}
	// Record the optimistic hit IMMEDIATELY — before the readiness wait,
	// which can block for minutes. We want the bucket to show in the
	// table as soon as AWS accepted CreateBucket, so the TUI feels
	// snappy. State starts as "PENDING" and is upgraded once the bucket
	// is ready (state=OK).
	if c.optimistic != nil {
		c.optimistic.addBucket(Bucket{Name: name, State: "PENDING", Region: c.cfg.Region}, 10*time.Minute)
	}
	if werr := c.WaitForBucketReady(ctx, name); werr != nil {
		return werr
	}
	if c.optimistic != nil {
		c.optimistic.addBucket(Bucket{Name: name, State: "OK", Region: c.cfg.Region}, 10*time.Minute)
	}
	return nil
}

// WaitForBucketReady polls GetBucket until its state.code == "OK" or a
// 10-minute timeout elapses. Bucket creation is eventually-consistent in
// Lightsail; observed propagation is usually 15-60s but can extend into
// minutes on busy regions or for newly-enabled opt-in regions.
func (c *Client) WaitForBucketReady(ctx context.Context, name string) error {
	deadline := time.Now().Add(10 * time.Minute)
	delay := 2 * time.Second
	var lastState string
	for {
		out, err := c.ls.GetBuckets(ctx, &lightsail.GetBucketsInput{
			BucketName: aws.String(name),
		})
		if err == nil {
			for _, b := range out.Buckets {
				if b.Name != nil && *b.Name == name && b.State != nil {
					lastState = aws.ToString(b.State.Code)
					if lastState == "OK" {
						return nil
					}
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("bucket %q not ready after 10m (last state: %q)", name, lastState)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if delay < 15*time.Second {
			delay += 2 * time.Second
		}
	}
}

// ValidateBucketName enforces Lightsail's bucket-name rules at the last
// mile so a bad name built from empty/odd Input components fails before
// the API call instead of as an opaque server error.
//
// Rules (from the Lightsail docs): 3-54 chars, lowercase letters /
// digits / hyphens only, must begin and end with a letter or digit, no
// consecutive hyphens. Our internal naming scheme never produces
// uppercase, dots, or underscores, but can produce empty segments
// (e.g. ls--123--  if app name ever slips through as "") so the
// trailing-dash check is the one that actually catches real bugs.
func ValidateBucketName(name string) error {
	if len(name) < 3 || len(name) > 54 {
		return fmt.Errorf("bucket name %q: length must be 3-54", name)
	}
	if !isAlnum(name[0]) {
		return fmt.Errorf("bucket name %q: must start with a letter or digit", name)
	}
	if !isAlnum(name[len(name)-1]) {
		return fmt.Errorf("bucket name %q: must not end with %q", name, name[len(name)-1])
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case isAlnum(c):
			continue
		case c == '-':
			if i+1 < len(name) && name[i+1] == '-' {
				// Our naming uses '--' intentionally as a separator
				// (ls--<acct>--<app>--<env>), so skip over the pair.
				i++
			}
		default:
			return fmt.Errorf("bucket name %q: invalid character %q", name, c)
		}
	}
	return nil
}

func isAlnum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
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

// IsNotFound reports whether err looks like a Lightsail "doesn't exist"
// response. Used by delete paths to treat already-gone resources as
// success — partial or retried deletes shouldn't fail because something
// a previous run already removed.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, needle := range []string{
		"does not exist",
		"notfound",
		"not found",
		"no such",
	} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
