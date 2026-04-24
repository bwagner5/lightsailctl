package lightsail

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/lightsail"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// Client is the domain wrapper around a *lightsail.Client. All Application
// operations flow through it so retry/ownership concerns stay centralized.
type Client struct {
	cfg aws.Config
	ls  *lightsail.Client
	sts *sts.Client
}

// New loads AWS config for the given region and returns a Client. If region
// is "", SDK default resolution applies (AWS_REGION, profile, etc.).
func New(ctx context.Context, region string) (*Client, error) {
	opts := []func(*config.LoadOptions) error{}
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return &Client{
		cfg: cfg,
		ls:  lightsail.NewFromConfig(cfg),
		sts: sts.NewFromConfig(cfg),
	}, nil
}

// Region returns the effective AWS region.
func (c *Client) Region() string { return c.cfg.Region }

// WithRegion returns a copy of Client bound to a different region.
func (c *Client) WithRegion(region string) *Client {
	cfg := c.cfg.Copy()
	cfg.Region = region
	return &Client{cfg: cfg, ls: lightsail.NewFromConfig(cfg), sts: sts.NewFromConfig(cfg)}
}

// DefaultRegion picks a sane region for first-run: env var if set, else current.
func (c *Client) DefaultRegion() string {
	for _, k := range []string{"AWS_REGION", "AWS_DEFAULT_REGION"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	if c.cfg.Region != "" {
		return c.cfg.Region
	}
	return "us-east-1"
}

// AccountID returns the AWS account ID of the caller.
func (c *Client) AccountID(ctx context.Context) (string, error) {
	out, err := c.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", fmt.Errorf("get caller identity: %w", err)
	}
	return aws.ToString(out.Account), nil
}

// AppBucketName returns the app-level config bucket name: ls--<acct>--<app>.
func AppBucketName(accountID, app string) string {
	return BucketPrefix + accountID + "--" + app
}

// EnvBucketName returns the env bucket name: ls--<acct>--<app>--<env>.
func EnvBucketName(accountID, app, env string) string {
	return AppBucketName(accountID, app) + "--" + env
}

// ParseAppEnv pulls (app, env) out of a bucket name. Returns ("", "") if the
// name doesn't match the env-bucket shape (ls--<acct>--<app>--<env>).
func ParseAppEnv(bucketName string) (app, env string) {
	if !strings.HasPrefix(bucketName, BucketPrefix) {
		return "", ""
	}
	rest := strings.TrimPrefix(bucketName, BucketPrefix)
	parts := strings.SplitN(rest, "--", 3)
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return "", ""
	}
	return parts[1], parts[2]
}

// ParseAppFromAppBucket extracts <app> from an app-config bucket name
// (ls--<acct>--<app>). Returns "" if not shaped that way.
func ParseAppFromAppBucket(bucketName string) (app string) {
	if !strings.HasPrefix(bucketName, BucketPrefix) {
		return ""
	}
	rest := strings.TrimPrefix(bucketName, BucketPrefix)
	parts := strings.SplitN(rest, "--", 3)
	if len(parts) != 2 || parts[1] == "" {
		return ""
	}
	return parts[1]
}
