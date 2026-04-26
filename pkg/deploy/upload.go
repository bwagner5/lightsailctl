package deploy

import (
	"context"
	"encoding/json"
	"io"
	"os"
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
// container on every tagged instance is "running", or timeout elapses. It
// returns nil when healthy, context.DeadlineExceeded on timeout (caller can
// downgrade to a warning), or any other error for hard failures.
//
// `since` filters out status writes that happened before this deploy — we
// only trust reports from watchers that saw our tarball.
func WaitForHealthy(ctx context.Context, c *lightsail.Client, bucket string, since time.Time, poll time.Duration) error {
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		statuses, err := c.ReadBucketStatuses(ctx, bucket)
		if err == nil && allHealthy(statuses, since) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func allHealthy(statuses []lightsail.Status, since time.Time) bool {
	if len(statuses) == 0 {
		return false
	}
	for _, s := range statuses {
		if s.Timestamp.Before(since) {
			return false // stale report
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
