// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package lightsail

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lightsail"
	lstypes "github.com/aws/aws-sdk-go-v2/service/lightsail/types"
)

// Instance is our slim view of a Lightsail instance.
type Instance struct {
	Name      string
	State     string
	IP        string
	Region    string
	Blueprint string
	Bundle    string
	CreatedAt time.Time
	Tags      map[string]string
	raw       *lstypes.Instance
}

// ListInstances returns every Lightsail instance visible to the caller.
// When the Client is unpinned, fans out across all regions (same model as
// ListBuckets).
func (c *Client) ListInstances(ctx context.Context) ([]Instance, error) {
	if c.Region() == "" {
		return c.listInstancesGlobal(ctx)
	}
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

func (c *Client) listInstancesGlobal(ctx context.Context) ([]Instance, error) {
	regions, err := c.RegionIDs(ctx)
	if err != nil {
		return nil, err
	}
	var (
		mu  sync.Mutex
		out []Instance
		wg  sync.WaitGroup
	)
	for _, r := range regions {
		wg.Add(1)
		go func(region string) {
			defer wg.Done()
			rc := c.WithRegion(region)
			is, err := rc.ListInstances(ctx)
			if err != nil {
				return
			}
			mu.Lock()
			out = append(out, is...)
			mu.Unlock()
		}(r)
	}
	wg.Wait()
	return out, nil
}

// GetInstance fetches a single instance by name. When the client is global
// (no region pinned), it fans out via ListInstances to find the instance.
func (c *Client) GetInstance(ctx context.Context, name string) (*Instance, error) {
	if c.Region() == "" {
		instances, err := c.ListInstances(ctx)
		if err != nil {
			return nil, err
		}
		for i := range instances {
			if instances[i].Name == name {
				return &instances[i], nil
			}
		}
		return nil, fmt.Errorf("instance %q not found", name)
	}
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

// UntagInstance removes a single tag key from the instance.
func (c *Client) UntagInstance(ctx context.Context, instance, key string) error {
	_, err := c.ls.UntagResource(ctx, &lightsail.UntagResourceInput{
		ResourceName: aws.String(instance),
		TagKeys:      []string{key},
	})
	return err
}

func toInstance(in *lstypes.Instance) Instance {
	out := Instance{
		Name:      aws.ToString(in.Name),
		IP:        aws.ToString(in.PublicIpAddress),
		Blueprint: aws.ToString(in.BlueprintId),
		Bundle:    aws.ToString(in.BundleId),
		Tags:      map[string]string{},
		raw:       in,
	}
	if in.State != nil {
		out.State = aws.ToString(in.State.Name)
	}
	if in.CreatedAt != nil {
		out.CreatedAt = *in.CreatedAt
	}
	if in.Location != nil {
		out.Region = string(in.Location.RegionName)
	}
	for _, t := range in.Tags {
		out.Tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return out
}
