// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package lightsail

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lightsail"
	lstypes "github.com/aws/aws-sdk-go-v2/service/lightsail/types"
)

// CreateInstance creates a single Lightsail instance.
func (c *Client) CreateInstance(ctx context.Context, name, az, blueprintID, bundleID, ipType, userData string, monitoring bool) error {
	in := &lightsail.CreateInstancesInput{
		InstanceNames:    []string{name},
		AvailabilityZone: aws.String(az),
		BlueprintId:      aws.String(blueprintID),
		BundleId:         aws.String(bundleID),
		// Version tag for future upgrade tooling.
		Tags: lightsailTagsFromMap(DefaultResourceTags()),
	}
	if ipType != "" {
		in.IpAddressType = lstypes.IpAddressType(ipType)
	}
	if userData != "" {
		in.UserData = aws.String(userData)
	}
	if monitoring {
		in.AddOns = []lstypes.AddOnRequest{{
			AddOnType:                lstypes.AddOnTypeAutoSnapshot,
			AutoSnapshotAddOnRequest: &lstypes.AutoSnapshotAddOnRequest{},
		}}
	}
	_, err := c.ls.CreateInstances(ctx, in)
	if err != nil {
		return fmt.Errorf("create instance %s: %w", name, err)
	}
	return nil
}

// DeleteInstance deletes a Lightsail instance by name.
func (c *Client) DeleteInstance(ctx context.Context, name string) error {
	_, err := c.ls.DeleteInstance(ctx, &lightsail.DeleteInstanceInput{
		InstanceName:      aws.String(name),
		ForceDeleteAddOns: aws.Bool(true),
	})
	if err != nil {
		return fmt.Errorf("delete instance %s: %w", name, err)
	}
	return nil
}

// StartInstance starts a stopped Lightsail instance.
func (c *Client) StartInstance(ctx context.Context, name string) error {
	_, err := c.ls.StartInstance(ctx, &lightsail.StartInstanceInput{
		InstanceName: aws.String(name),
	})
	if err != nil {
		return fmt.Errorf("start instance %s: %w", name, err)
	}
	return nil
}

// StopInstance stops a running Lightsail instance.
func (c *Client) StopInstance(ctx context.Context, name string) error {
	_, err := c.ls.StopInstance(ctx, &lightsail.StopInstanceInput{
		InstanceName: aws.String(name),
	})
	if err != nil {
		return fmt.Errorf("stop instance %s: %w", name, err)
	}
	return nil
}

// Blueprint is our slim view of a Lightsail blueprint.
type Blueprint struct {
	ID       string
	Name     string
	Type     string // "os" or "app"
	Platform string // "LINUX_UNIX" or "WINDOWS"
}

// ListBlueprints returns active Lightsail blueprints.
func (c *Client) ListBlueprints(ctx context.Context) ([]Blueprint, error) {
	var out []Blueprint
	var page *string
	for {
		resp, err := c.ls.GetBlueprints(ctx, &lightsail.GetBlueprintsInput{
			IncludeInactive: aws.Bool(false),
			PageToken:       page,
		})
		if err != nil {
			return nil, fmt.Errorf("list blueprints: %w", err)
		}
		for _, b := range resp.Blueprints {
			out = append(out, Blueprint{
				ID:       aws.ToString(b.BlueprintId),
				Name:     aws.ToString(b.Name),
				Type:     string(b.Type),
				Platform: string(b.Platform),
			})
		}
		if resp.NextPageToken == nil {
			return out, nil
		}
		page = resp.NextPageToken
	}
}

// Bundle is our slim view of a Lightsail bundle (instance size).
type Bundle struct {
	ID        string
	Name      string
	RAM       float32
	VCPUs     int32
	Disk      int32
	Transfer  int32 // GB per month
	Price     float32
	Platforms []string // e.g. ["LINUX_UNIX"], ["LINUX_UNIX","WINDOWS"]
}

// Bundle category constants.
const (
	BundleCategoryGeneralPurpose   = "general_purpose"
	BundleCategoryMemoryOptimized  = "memory_optimized"
	BundleCategoryComputeOptimized = "compute_optimized"
)

func (b Bundle) Category() string {
	if strings.HasPrefix(b.ID, "c_") {
		return BundleCategoryComputeOptimized
	}
	if strings.HasPrefix(b.ID, "m_") {
		return BundleCategoryMemoryOptimized
	}
	return BundleCategoryGeneralPurpose
}

// ListBundles returns active Lightsail bundles.
func (c *Client) ListBundles(ctx context.Context) ([]Bundle, error) {
	var out []Bundle
	var page *string
	for {
		resp, err := c.ls.GetBundles(ctx, &lightsail.GetBundlesInput{
			IncludeInactive: aws.Bool(false),
			PageToken:       page,
		})
		if err != nil {
			return nil, fmt.Errorf("list bundles: %w", err)
		}
		for _, b := range resp.Bundles {
			var platforms []string
			for _, p := range b.SupportedPlatforms {
				platforms = append(platforms, string(p))
			}
			out = append(out, Bundle{
				ID:        aws.ToString(b.BundleId),
				Name:      aws.ToString(b.Name),
				RAM:       aws.ToFloat32(b.RamSizeInGb),
				VCPUs:     aws.ToInt32(b.CpuCount),
				Disk:      aws.ToInt32(b.DiskSizeInGb),
				Transfer:  aws.ToInt32(b.TransferPerMonthInGb),
				Price:     aws.ToFloat32(b.Price),
				Platforms: platforms,
			})
		}
		if resp.NextPageToken == nil {
			return out, nil
		}
		page = resp.NextPageToken
	}
}

// PutInstancePublicPorts replaces ALL firewall rules on an instance.
func (c *Client) PutInstancePublicPorts(ctx context.Context, instance string, ports []lstypes.PortInfo) error {
	_, err := c.ls.PutInstancePublicPorts(ctx, &lightsail.PutInstancePublicPortsInput{
		InstanceName: aws.String(instance),
		PortInfos:    ports,
	})
	if err != nil {
		return fmt.Errorf("put ports on %s: %w", instance, err)
	}
	return nil
}

// ListAvailabilityZones returns AZs for the client's current region.
func (c *Client) ListAvailabilityZones(ctx context.Context) ([]string, error) {
	resp, err := c.ls.GetRegions(ctx, &lightsail.GetRegionsInput{
		IncludeAvailabilityZones: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("get regions: %w", err)
	}
	for _, r := range resp.Regions {
		if string(r.Name) == c.Region() {
			var azs []string
			for _, az := range r.AvailabilityZones {
				azs = append(azs, aws.ToString(az.ZoneName))
			}
			return azs, nil
		}
	}
	return []string{c.Region() + "a"}, nil
}
