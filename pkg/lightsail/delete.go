package lightsail

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lightsail"
	lstypes "github.com/aws/aws-sdk-go-v2/service/lightsail/types"
)

// UntagInstancesForApp removes every ls:app:<app>:<env> tag from every
// instance. Called during app deletion to disassociate all targets.
// Returns the list of (instance, env) pairs that were untagged so callers
// can drive per-env cleanup.
//
// Works globally: if the parent Client is unpinned, ListInstances fans out
// across every region; each Untag call is dispatched to a regional client
// FindTargetsForApp returns every instance currently tagged for the app
// (one TargetRef per env tag). Read-only counterpart to
// UntagInstancesForApp for steps that want to report counts before
// taking destructive action.
func (c *Client) FindTargetsForApp(ctx context.Context, appName string) ([]TargetRef, error) {
	instances, err := c.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	prefix := TagPrefix + appName + ":"
	var out []TargetRef
	for _, inst := range instances {
		for k := range inst.Tags {
			if strings.HasPrefix(k, prefix) {
				env := strings.TrimPrefix(k, prefix)
				out = append(out, TargetRef{Instance: inst.Name, Env: env, Region: inst.Region})
			}
		}
	}
	return out, nil
}

// scoped to the instance's own region.
func (c *Client) UntagInstancesForApp(ctx context.Context, appName string) ([]TargetRef, error) {
	instances, err := c.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	prefix := TagPrefix + appName + ":"
	var out []TargetRef
	for _, inst := range instances {
		var keys []string
		for k := range inst.Tags {
			if strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
				env := strings.TrimPrefix(k, prefix)
				out = append(out, TargetRef{Instance: inst.Name, Env: env, Region: inst.Region})
			}
		}
		if len(keys) == 0 {
			continue
		}
		rc := c.regional(inst.Region)
		_, err := rc.ls.UntagResource(ctx, &lightsail.UntagResourceInput{
			ResourceName: aws.String(inst.Name),
			TagKeys:      keys,
		})
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// TargetRef is an (instance, env, region) triple discovered from instance tags.
type TargetRef struct {
	Instance string
	Env      string
	Region   string
}

// FindTargetsForAppEnv returns every instance tagged as a target of the given
// app+env.
func (c *Client) FindTargetsForAppEnv(ctx context.Context, appName, envName string) ([]Instance, error) {
	instances, err := c.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	key := TagPrefix + appName + ":" + envName
	var out []Instance
	for _, inst := range instances {
		if _, ok := inst.Tags[key]; ok {
			out = append(out, inst)
		}
	}
	return out, nil
}

// DeleteBucket force-deletes a Lightsail bucket in the given region.
// Buckets are regional, so callers must pass the bucket's region (the
// value Bucket.Region carries).
func (c *Client) DeleteBucket(ctx context.Context, name, region string) error {
	force := true
	_, err := c.regional(region).ls.DeleteBucket(ctx, &lightsail.DeleteBucketInput{
		BucketName:  aws.String(name),
		ForceDelete: &force,
	})
	if c.optimistic != nil {
		c.optimistic.forget(name)
	}
	return err
}

// ResetFirewallIfUnused resets an instance's firewall to SSH-only when the
// instance is no longer tagged for any Lightsail app. No-op if the instance
// still has other ls:app:* tags. Requires the instance's region.
func (c *Client) ResetFirewallIfUnused(ctx context.Context, instanceName, region string) error {
	rc := c.regional(region)
	inst, err := rc.GetInstance(ctx, instanceName)
	if err != nil {
		return err
	}
	for k := range inst.Tags {
		if strings.HasPrefix(k, TagPrefix) {
			return nil // still in use
		}
	}
	_, err = rc.ls.PutInstancePublicPorts(ctx, &lightsail.PutInstancePublicPortsInput{
		InstanceName: aws.String(instanceName),
		PortInfos: []lstypes.PortInfo{
			{FromPort: 22, ToPort: 22, Protocol: lstypes.NetworkProtocolTcp, Cidrs: []string{"0.0.0.0/0"}},
		},
	})
	return err
}
