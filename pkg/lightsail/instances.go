package lightsail

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lightsail"
	lstypes "github.com/aws/aws-sdk-go-v2/service/lightsail/types"
)

// Instance is our slim view of a Lightsail instance.
type Instance struct {
	Name   string
	State  string
	IP     string
	Region string
	Tags   map[string]string
	raw    *lstypes.Instance
}

// ListInstances returns every Lightsail instance in the client's region.
func (c *Client) ListInstances(ctx context.Context) ([]Instance, error) {
	var out []Instance
	var page *string
	for {
		resp, err := c.ls.GetInstances(ctx, &lightsail.GetInstancesInput{PageToken: page})
		if err != nil {
			return nil, err
		}
		for i := range resp.Instances {
			out = append(out, toInstance(&resp.Instances[i]))
		}
		if resp.NextPageToken == nil {
			return out, nil
		}
		page = resp.NextPageToken
	}
}

// GetInstance fetches a single instance by name.
func (c *Client) GetInstance(ctx context.Context, name string) (*Instance, error) {
	resp, err := c.ls.GetInstance(ctx, &lightsail.GetInstanceInput{InstanceName: aws.String(name)})
	if err != nil {
		return nil, err
	}
	i := toInstance(resp.Instance)
	return &i, nil
}

// TagInstance adds a tag key=value to the instance.
func (c *Client) TagInstance(ctx context.Context, instance, key, value string) error {
	_, err := c.ls.TagResource(ctx, &lightsail.TagResourceInput{
		ResourceName: aws.String(instance),
		Tags: []lstypes.Tag{{
			Key:   aws.String(key),
			Value: aws.String(value),
		}},
	})
	return err
}

func toInstance(in *lstypes.Instance) Instance {
	out := Instance{
		Name: aws.ToString(in.Name),
		IP:   aws.ToString(in.PublicIpAddress),
		Tags: map[string]string{},
		raw:  in,
	}
	if in.State != nil {
		out.State = aws.ToString(in.State.Name)
	}
	if in.Location != nil {
		out.Region = string(in.Location.RegionName)
	}
	for _, t := range in.Tags {
		out.Tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return out
}
