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
				out = append(out, TargetRef{Instance: inst.Name, Env: env})
			}
		}
		if len(keys) == 0 {
			continue
		}
		_, err := c.ls.UntagResource(ctx, &lightsail.UntagResourceInput{
			ResourceName: aws.String(inst.Name),
			TagKeys:      keys,
		})
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// TargetRef is an (instance, env) pair discovered from instance tags.
type TargetRef struct {
	Instance string
	Env      string
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

// DeleteBucket force-deletes a Lightsail bucket.
func (c *Client) DeleteBucket(ctx context.Context, name string) error {
	force := true
	_, err := c.ls.DeleteBucket(ctx, &lightsail.DeleteBucketInput{
		BucketName:  aws.String(name),
		ForceDelete: &force,
	})
	return err
}

// ResetFirewallIfUnused resets an instance's firewall to SSH-only when the
// instance is no longer tagged for any Lightsail app. No-op if the instance
// still has other ls:app:* tags.
func (c *Client) ResetFirewallIfUnused(ctx context.Context, instanceName string) error {
	inst, err := c.GetInstance(ctx, instanceName)
	if err != nil {
		return err
	}
	for k := range inst.Tags {
		if strings.HasPrefix(k, TagPrefix) {
			return nil // still in use
		}
	}
	_, err = c.ls.PutInstancePublicPorts(ctx, &lightsail.PutInstancePublicPortsInput{
		InstanceName: aws.String(instanceName),
		PortInfos: []lstypes.PortInfo{
			{FromPort: 22, ToPort: 22, Protocol: lstypes.NetworkProtocolTcp, Cidrs: []string{"0.0.0.0/0"}},
		},
	})
	return err
}
