package lightsail

import (
	"context"
	"fmt"
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
	// regionHints are callers' priority hints (typically AWS_REGION /
	// AWS_DEFAULT_REGION resolved in main) used to re-order FetchRegions.
	regionHints []string
}

// Options configures Client construction.
type Options struct {
	// Region pins the Client to a single AWS region. Empty = global
	// (fan-out across SupportedRegions() for list ops).
	Region string
	// RegionHints are AWS regions the caller wants queried first during
	// global fan-out (typically AWS_REGION / AWS_DEFAULT_REGION, resolved
	// in main). Order is preserved; unknown regions are dropped.
	RegionHints []string
}

// New loads AWS config and returns a Client. The returned Client is NOT
// pinned to a region by default — callers that need one use WithRegion()
// per-call. This matches `aws lightsail` which has a global listing model
// (the API itself is regional, but our CLI surfaces "show me every app
// across every region" by default).
//
// If region is non-empty, the Client is pinned to that region.
//
// Important: AWS_REGION / AWS_DEFAULT_REGION in the environment do NOT pin
// the Client. They serve as a priority hint for the fan-out order; pass
// them via Options.RegionHints (callers resolve env in main).
//
// Shim for callers that don't need to set hints. Prefer NewWithOptions.
func New(ctx context.Context, region string) (*Client, error) {
	return NewWithOptions(ctx, Options{Region: region})
}

// NewWithOptions is the full constructor.
func NewWithOptions(ctx context.Context, opts Options) (*Client, error) {
	awsOpts := []func(*config.LoadOptions) error{}
	if opts.Region != "" {
		awsOpts = append(awsOpts, config.WithRegion(opts.Region))
	}
	cfg, err := config.LoadDefaultConfig(ctx, awsOpts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	// Default config resolution may pick up AWS_REGION / profile default
	// even when we didn't ask. For the unpinned path we explicitly clear
	// that so "--region" is the only way to pin the CLI.
	if opts.Region == "" {
		cfg.Region = ""
	}
	c := &Client{
		cfg:         cfg,
		sts:         sts.NewFromConfig(cfg),
		regionHints: opts.RegionHints,
	}
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
	out := &Client{cfg: cfg, sts: sts.NewFromConfig(cfg), regionHints: c.regionHints}
	if region != "" {
		out.ls = lightsail.NewFromConfig(cfg)
	}
	return out
}

// DefaultRegion returns a sane region for first-run. Uses the SDK's already-
// resolved config value when available (which itself honors AWS_REGION /
// profile defaults), else falls back to us-east-1. Env vars are NOT read
// directly here — hermetic.
func (c *Client) DefaultRegion() string {
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

// FetchRegions returns the AWS regions where Lightsail is available,
// ordered so that regions in the Client's RegionHints (set from main via
// Options.RegionHints) come first. The list is a static allowlist from
// the Lightsail docs (see SupportedRegions); it does NOT round-trip to
// AWS. This matches the plan's "always global across Lightsail-supported
// regions" UX and keeps cold-start latency flat.
//
// --region <id> on the CLI is validated against nothing; that allows
// customers to use a newly launched Lightsail region before we ship an
// updated allowlist.
func (c *Client) FetchRegions(_ context.Context) ([]string, error) {
	return prioritizeRegions(SupportedRegions(), c.regionHints), nil
}

// prioritizeRegions moves each region in hints (that exists in all) to the
// front, preserving the order of hints. Others follow in all's original order.
func prioritizeRegions(all, hints []string) []string {
	if len(hints) == 0 {
		return all
	}
	inHints := map[string]bool{}
	for _, h := range hints {
		inHints[h] = true
	}
	out := make([]string, 0, len(all))
	// Hints first (only those that actually appear in all).
	present := map[string]bool{}
	for _, r := range all {
		present[r] = true
	}
	for _, h := range hints {
		if present[h] {
			out = append(out, h)
		}
	}
	// Then everything else, original order.
	for _, r := range all {
		if !inHints[r] {
			out = append(out, r)
		}
	}
	return out
}

// fanOut runs fn concurrently across each region and collects results.
// Errors are logged best-effort; a single region's failure does not kill
// the walk (Lightsail is not always enabled in every region).
