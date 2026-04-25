package lightsail

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
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

// New loads AWS config and returns a Client. The returned Client is NOT
// pinned to a region by default — callers that need one use WithRegion()
// per-call. This matches `aws lightsail` which has a global listing model
// (the API itself is regional, but our CLI surfaces "show me every app
// across every region" by default).
//
// If region is non-empty, the Client is pinned to that region (used by
// `--region` override and by create/deploy sagas once resolved).
func New(ctx context.Context, region string) (*Client, error) {
	opts := []func(*config.LoadOptions) error{}
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	// SDK config resolution may leave Region empty if the user has no
	// profile default. That's fine for the global listing path; ops that
	// need a region will call WithRegion() explicitly and will error loudly
	// if the caller passes "".
	c := &Client{cfg: cfg, sts: sts.NewFromConfig(cfg)}
	if cfg.Region != "" {
		c.ls = lightsail.NewFromConfig(cfg)
	}
	return c, nil
}

// Region returns the effective AWS region, or "" if the Client is global
// (not pinned to any particular region).
func (c *Client) Region() string { return c.cfg.Region }

// WithRegion returns a copy of Client pinned to the given region. Safe to
// call with "" to get a (still-global) copy.
func (c *Client) WithRegion(region string) *Client {
	cfg := c.cfg.Copy()
	cfg.Region = region
	out := &Client{cfg: cfg, sts: sts.NewFromConfig(cfg)}
	if region != "" {
		out.ls = lightsail.NewFromConfig(cfg)
	}
	return out
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

// regional returns a region-scoped Client. If the caller already has a
// pinned client, it's reused; otherwise WithRegion(r) is built on demand.
// Panics if r is empty — callers must ensure they have a region before
// invoking regional ops.
func (c *Client) regional(r string) *Client {
	if r == "" {
		panic("lightsail: regional call without a region")
	}
	if c.cfg.Region == r {
		return c
	}
	return c.WithRegion(r)
}

// FetchRegions returns every AWS region visible to the caller. Uses ec2:
// DescribeRegions since Lightsail has no region-list API. Sorted with
// preferred (AWS_REGION env or profile default) first.
func (c *Client) FetchRegions(ctx context.Context) ([]string, error) {
	// EC2 DescribeRegions needs a region; us-east-1 is safe universally.
	cfg := c.cfg.Copy()
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	svc := ec2.NewFromConfig(cfg)
	out, err := svc.DescribeRegions(ctx, &ec2.DescribeRegionsInput{})
	if err != nil {
		return nil, err
	}
	var names []string
	for _, r := range out.Regions {
		names = append(names, aws.ToString(r.RegionName))
	}
	preferred := c.DefaultRegion()
	sort.SliceStable(names, func(i, j int) bool {
		if names[i] == preferred {
			return true
		}
		if names[j] == preferred {
			return false
		}
		return names[i] < names[j]
	})
	return names, nil
}

// fanOut runs fn concurrently across each region and collects results.
// Errors are logged best-effort; a single region's failure does not kill
// the walk (Lightsail is not always enabled in every region).
