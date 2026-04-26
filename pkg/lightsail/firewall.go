package lightsail

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lightsail"
	lstypes "github.com/aws/aws-sdk-go-v2/service/lightsail/types"
)

// OpenFirewallPorts ensures every port in ports is allowed inbound over TCP
// on the instance's public firewall. Existing rules (SSH, anything the user
// has added, other app ports) are preserved. Returns the number of new rules
// added.
func (c *Client) OpenFirewallPorts(ctx context.Context, instance string, ports []int) (int, error) {
	if len(ports) == 0 {
		return 0, nil
	}
	got, err := c.ls.GetInstance(ctx, &lightsail.GetInstanceInput{
		InstanceName: aws.String(instance),
	})
	if err != nil {
		return 0, err
	}
	var rules []lstypes.PortInfo
	have := map[int32]bool{}
	if got.Instance != nil && got.Instance.Networking != nil {
		for _, p := range got.Instance.Networking.Ports {
			rules = append(rules, lstypes.PortInfo{
				FromPort: p.FromPort, ToPort: p.ToPort,
				Protocol: p.Protocol, Cidrs: p.Cidrs,
			})
			if p.Protocol == lstypes.NetworkProtocolTcp && p.FromPort == p.ToPort {
				have[p.FromPort] = true
			}
		}
	}
	added := 0
	for _, port := range ports {
		p := int32(port)
		if have[p] {
			continue
		}
		rules = append(rules, lstypes.PortInfo{
			FromPort: p, ToPort: p,
			Protocol: lstypes.NetworkProtocolTcp,
			Cidrs:    []string{"0.0.0.0/0"},
		})
		added++
	}
	if added == 0 {
		return 0, nil
	}
	_, err = c.ls.PutInstancePublicPorts(ctx, &lightsail.PutInstancePublicPortsInput{
		InstanceName: aws.String(instance),
		PortInfos:    rules,
	})
	return added, err
}

// SetBucketAccessForInstance grants (allow=true) or revokes (allow=false)
// the Lightsail instance's ability to read+write the bucket via its IMDS
// credentials. When allow=true, the on-instance watcher can drop any
// static credentials file and authenticate purely through metadata.
//
// Verified via hack/test-bucket-instance-access.sh: propagation takes
// longer than 5s (30s is a safe wait before the first read).
func (c *Client) SetBucketAccessForInstance(ctx context.Context, bucket, instance string, allow bool) error {
	access := lstypes.ResourceBucketAccessDeny
	if allow {
		access = lstypes.ResourceBucketAccessAllow
	}
	return RetryableLong(ctx, func(ctx context.Context) error {
		_, err := c.ls.SetResourceAccessForBucket(ctx, &lightsail.SetResourceAccessForBucketInput{
			BucketName:   aws.String(bucket),
			ResourceName: aws.String(instance),
			Access:       access,
		})
		return err
	})
}
