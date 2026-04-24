package lightsail

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/lightsail"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// BucketKey is a short-lived AWS access-key pair scoped to one Lightsail bucket.
type BucketKey struct {
	Bucket    string
	AccessKey string
	Secret    string
}

// ErrBucketKeysExhausted means both of the bucket's two key slots are in use
// by someone else. Caller should wait or escalate.
var ErrBucketKeysExhausted = errors.New("both bucket access-key slots are in use; try again later or rotate")

// CreateBucketKey mints a new access key for the bucket. Returns
// ErrBucketKeysExhausted if the 2-key limit is hit.
func (c *Client) CreateBucketKey(ctx context.Context, bucket string) (*BucketKey, error) {
	out, err := c.ls.CreateBucketAccessKey(ctx, &lightsail.CreateBucketAccessKeyInput{
		BucketName: aws.String(bucket),
	})
	if err != nil {
		// Lightsail wraps the "too many keys" error in a plain ServiceException.
		if isTooManyKeys(err) {
			return nil, ErrBucketKeysExhausted
		}
		return nil, fmt.Errorf("create bucket access key: %w", err)
	}
	return &BucketKey{
		Bucket:    bucket,
		AccessKey: aws.ToString(out.AccessKey.AccessKeyId),
		Secret:    aws.ToString(out.AccessKey.SecretAccessKey),
	}, nil
}

// DeleteBucketKey removes a bucket access key. Safe to call on an unknown key.
func (c *Client) DeleteBucketKey(ctx context.Context, bucket, accessKeyID string) error {
	_, err := c.ls.DeleteBucketAccessKey(ctx, &lightsail.DeleteBucketAccessKeyInput{
		BucketName:  aws.String(bucket),
		AccessKeyId: aws.String(accessKeyID),
	})
	return err
}

// S3ClientFor returns an *s3.Client scoped to a single Lightsail bucket via a
// freshly-minted access key, plus a cleanup func the caller must defer.
//
// The returned client is regional-agnostic (uses our client's region); the
// bucket's own region is only relevant for cross-region reads which we don't
// perform here.
func (c *Client) S3ClientFor(ctx context.Context, bucket string) (*s3.Client, func(), error) {
	key, err := c.CreateBucketKey(ctx, bucket)
	if err != nil {
		return nil, nil, err
	}
	s3cli := s3.New(s3.Options{
		Region:      c.cfg.Region,
		Credentials: credentials.NewStaticCredentialsProvider(key.AccessKey, key.Secret, ""),
	})
	cleanup := func() {
		// Best-effort; use a fresh short-lived context so a cancelled caller
		// ctx doesn't skip the cleanup.
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = c.DeleteBucketKey(cctx, bucket, key.AccessKey)
	}
	return s3cli, cleanup, nil
}

func isTooManyKeys(err error) bool {
	// Lightsail returns a ServiceException like:
	//   "The bucket already has 2 access keys. Delete an existing key..."
	// Match on the text since the SDK doesn't expose a typed error for it.
	s := err.Error()
	for _, needle := range []string{"already has 2 access keys", "maximum number of access keys"} {
		if containsFold(s, needle) {
			return true
		}
	}
	return false
}

// containsFold is strings.Contains with case-insensitive matching.
func containsFold(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			a, b := s[i+j], substr[j]
			if a >= 'A' && a <= 'Z' {
				a += 32
			}
			if b >= 'A' && b <= 'Z' {
				b += 32
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
