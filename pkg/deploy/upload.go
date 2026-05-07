package deploy

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/aws/lightsailctl/pkg/lightsail"
)

// Upload PUTs the tarball at localPath to s3://bucket/key with retries to
// cover Lightsail's post-key-mint consistency window.
func Upload(ctx context.Context, s3cli *s3.Client, bucket, key, localPath string) error {
	return lightsail.RetryableLong(ctx, func(ctx context.Context) error {
		f, err := os.Open(localPath)
		if err != nil {
			return lightsail.StopRetry(err)
		}
		defer func() { _ = f.Close() }()
		fi, err := f.Stat()
		if err != nil {
			return lightsail.StopRetry(err)
		}
		_, err = s3cli.PutObject(ctx, &s3.PutObjectInput{
			Bucket:        aws.String(bucket),
			Key:           aws.String(key),
			Body:          f,
			ContentLength: aws.Int64(fi.Size()),
			ContentType:   aws.String("application/gzip"),
		})
		return err
	})
}

// WaitForHealthy polls status.json files in the env bucket until every
// container on every tagged instance is "running" AND the watcher has
// applied the specific `asset` key we just uploaded, or timeout elapses.
// It returns nil when healthy, context.DeadlineExceeded on timeout (caller
// may surface as a warning/error), or any other error for hard failures.
//
// `since` filters out status writes that happened before this deploy.
// `asset` is the S3 key of the deploy tarball (e.g. "deploy/172000-abcd.tar.gz");
// we require every reporting instance's LastDeploy.ObjectURL to end with it
// so an "idle" or stale-but-fresh-timestamp report doesn't slip through as
// healthy.
func WaitForHealthy(ctx context.Context, c *lightsail.Client, bucket, asset string, since time.Time, poll time.Duration) error {
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		statuses, err := c.ReadBucketStatuses(ctx, bucket)
		if err == nil && allHealthy(statuses, asset, since) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// allHealthy returns true when every instance's watcher report shows:
//   - a fresh timestamp (after `since`, so no leftover from a previous run),
//   - LastDeploy pointing at our specific `asset` (so the watcher has
//     downloaded and applied THIS deploy, not a prior one),
//   - rolled-up Status == "healthy" (the watcher only emits this when
//     every container is running; idle/degraded/down all fail here),
//   - at least one container, every container's Status == "running"
//     (defense-in-depth in case a future watcher version sets Status
//     differently).
//
// An empty `statuses` slice means no watchers have reported yet — return
// false so the caller keeps polling.
func allHealthy(statuses []lightsail.Status, asset string, since time.Time) bool {
	if len(statuses) == 0 {
		return false
	}
	for _, s := range statuses {
		if s.Timestamp.Before(since) {
			return false // stale report from before this deploy
		}
		if s.LastDeploy == nil || !strings.HasSuffix(s.LastDeploy.ObjectURL, asset) {
			return false // watcher hasn't applied our tarball yet
		}
		if s.Status != "healthy" {
			return false // idle / degraded / down / unknown
		}
		if len(s.Containers) == 0 {
			return false
		}
		for _, c := range s.Containers {
			if c.Status != "running" {
				return false
			}
		}
	}
	return true
}

// DumpStatusJSON writes a one-line JSON summary to w. Used by the deploy op
// to end with a compact machine-readable marker even in interactive mode.
func DumpStatusJSON(w io.Writer, asset, bucket string) {
	_ = json.NewEncoder(w).Encode(map[string]string{
		"asset":  asset,
		"bucket": bucket,
		"status": "uploaded",
	})
}
