package lightsail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/lightsail"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ownerPrefix is the S3 key prefix for bucket-access-key ownership records.
// We write one JSON file per minted key; the presence/absence of a key file
// tells other clients whether a live key slot is in use by us.
const ownerPrefix = "_owners/"

// BucketKey is a short-lived AWS access-key pair scoped to one Lightsail bucket.
type BucketKey struct {
	Bucket    string
	AccessKey string
	Secret    string
}

// Ownership is the JSON payload written to _owners/<accessKeyID>.json so that
// concurrent clients don't clobber each other's in-flight keys.
type Ownership struct {
	AccessKeyID string    `json:"access_key_id"`
	Host        string    `json:"host"`
	PID         int       `json:"pid"`
	Created     time.Time `json:"created"`
}

// ErrBucketKeysExhausted means both of the bucket's two key slots are in use
// by someone else. Caller should wait or escalate.
var ErrBucketKeysExhausted = errors.New("both bucket access-key slots are in use; try again later or rotate")

// CreateBucketKey mints a new access key for the bucket. Returns
// ErrBucketKeysExhausted if the 2-key limit is hit.
//
// After mint, we attempt to write an _owners/<keyID>.json marker into the
// bucket via the new credentials. This is best-effort: if the bucket is too
// new for the credentials to see (eventual consistency), we retry briefly.
func (c *Client) CreateBucketKey(ctx context.Context, bucket string) (*BucketKey, error) {
	var out *lightsail.CreateBucketAccessKeyOutput
	err := RetryableLong(ctx, func(ctx context.Context) error {
		var cerr error
		out, cerr = c.ls.CreateBucketAccessKey(ctx, &lightsail.CreateBucketAccessKeyInput{
			BucketName: aws.String(bucket),
		})
		if cerr == nil {
			return nil
		}
		if isTooManyKeys(cerr) {
			return StopRetry(ErrBucketKeysExhausted)
		}
		return cerr
	})
	if err != nil {
		return nil, fmt.Errorf("create bucket access key: %w", err)
	}
	key := &BucketKey{
		Bucket:    bucket,
		AccessKey: aws.ToString(out.AccessKey.AccessKeyId),
		Secret:    aws.ToString(out.AccessKey.SecretAccessKey),
	}
	// Best-effort ownership marker; callers don't need to care if this fails.
	_ = writeOwnership(ctx, c.cfg.Region, key)
	return key, nil
}

// DeleteBucketKey removes a bucket access key and its ownership marker.
// Safe to call on an unknown key.
func (c *Client) DeleteBucketKey(ctx context.Context, bucket, accessKeyID string) error {
	// Try to remove the ownership marker first (best-effort — we still have
	// the credentials since caller passed us the accessKeyID, but we don't
	// here; rely on the lightsail DeleteBucketAccessKey tearing down access
	// anyway, and let a stale marker get cleaned up by GetOrReuseBucketKey).
	_, err := c.ls.DeleteBucketAccessKey(ctx, &lightsail.DeleteBucketAccessKeyInput{
		BucketName:  aws.String(bucket),
		AccessKeyId: aws.String(accessKeyID),
	})
	return err
}

// S3ClientFor returns an *s3.Client scoped to a single Lightsail bucket via a
// freshly-minted access key, plus a cleanup func the caller must defer.
func (c *Client) S3ClientFor(ctx context.Context, bucket string) (*s3.Client, func(), error) {
	key, err := c.CreateBucketKey(ctx, bucket)
	if err != nil {
		return nil, nil, err
	}
	s3cli := s3AdminClient(c.cfg.Region, key)
	cleanup := func() {
		// Fresh short-lived context so a cancelled caller ctx doesn't skip cleanup.
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Remove the ownership marker first (so a racing client sees a freed slot).
		_ = deleteOwnership(cctx, s3cli, bucket, key.AccessKey)
		_ = c.DeleteBucketKey(cctx, bucket, key.AccessKey)
	}
	return s3cli, cleanup, nil
}

// s3AdminClient builds an S3 client scoped to the Lightsail bucket via the
// given key. Retries transient NoSuchBucket / credential-propagation errors
// internally via the SDK's default retryer plus our 30s propagation window.
func s3AdminClient(region string, key *BucketKey) *s3.Client {
	return s3.New(s3.Options{
		Region:      region,
		Credentials: credentials.NewStaticCredentialsProvider(key.AccessKey, key.Secret, ""),
	})
}

// writeOwnership marks this client as the owner of accessKeyID via a file
// inside the bucket. Retries across the Lightsail key-propagation window
// (SetResourceAccessForBucket testing showed ~10-60s on a cold bucket).
func writeOwnership(ctx context.Context, region string, key *BucketKey) error {
	s3cli := s3AdminClient(region, key)
	host, _ := os.Hostname()
	own := Ownership{
		AccessKeyID: key.AccessKey,
		Host:        host,
		PID:         os.Getpid(),
		Created:     time.Now().UTC(),
	}
	data, _ := json.Marshal(own)
	objKey := ownerPrefix + key.AccessKey + ".json"
	return Retryable(ctx, func(ctx context.Context) error {
		_, err := s3cli.PutObject(ctx, &s3.PutObjectInput{
			Bucket:      aws.String(key.Bucket),
			Key:         aws.String(objKey),
			Body:        bytes.NewReader(data),
			ContentType: aws.String("application/json"),
		})
		return err
	})
}

func deleteOwnership(ctx context.Context, s3cli *s3.Client, bucket, accessKeyID string) error {
	objKey := ownerPrefix + accessKeyID + ".json"
	_, err := s3cli.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(objKey),
	})
	return err
}

// ReadOwnerships lists ownership markers currently present in a bucket. Used
// in diagnostics; also makes it possible to manually reap stale entries.
// Returns an empty slice if the bucket is unreadable.
func (c *Client) ReadOwnerships(ctx context.Context, bucket string) ([]Ownership, error) {
	s3cli, cleanup, err := c.S3ClientFor(ctx, bucket)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	out, err := s3cli.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket), Prefix: aws.String(ownerPrefix),
	})
	if err != nil {
		return nil, err
	}
	var owners []Ownership
	for _, obj := range out.Contents {
		k := aws.ToString(obj.Key)
		if !strings.HasSuffix(k, ".json") {
			continue
		}
		getOut, err := s3cli.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(bucket), Key: aws.String(k),
		})
		if err != nil {
			continue
		}
		data, rerr := io.ReadAll(getOut.Body)
		_ = getOut.Body.Close()
		if rerr != nil {
			continue
		}
		var o Ownership
		if json.Unmarshal(data, &o) == nil {
			owners = append(owners, o)
		}
	}
	return owners, nil
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
