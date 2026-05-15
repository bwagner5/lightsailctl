// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"fmt"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/lightsail"
)

// RegionSwitchOp returns a triad GlobalOp that lets TUI users switch the
// CLI's effective region at runtime. Bound to key "r" for "region".
//
// The field is Required so the wizard always fires — even when the input
// happens to be "". Triad's missing-required check skips fields that are
// already set; since the TUI passes an empty Input, the picker opens every
// time the op is invoked.
func RegionSwitchOp(region *string) registry.Operation {
	return registry.Operation{
		Name: "region", Key: "r", Short: "switch region (blank = global)",
		Fields: []registry.Field{
			{
				Flag: "region", Required: true, Help: "AWS region (blank = global)",
				Suggest: func(_ context.Context) ([]registry.Choice, error) {
					// Uses the embedded-snapshot wrappers intentionally:
					// this picker runs inside triad's GlobalOp hook
					// which only gets the *string region pointer — no
					// *lightsail.Client in scope. The snapshot is
					// refreshed at release time via `make regions-
					// snapshot`, so the picker lags at most one release
					// behind AWS. Fan-out (buckets/instances) and the
					// create wizard use the live Client.Regions path.
					regions := lightsail.SortRegionsByGroup(lightsail.SupportedRegions())
					// Format each region as "<group>   <id>   <location>"
					// with group-aligned columns.
					maxID := len("global")
					for _, r := range regions {
						if len(r) > maxID {
							maxID = len(r)
						}
					}
					maxGroup := 0
					for _, r := range regions {
						if g := lightsail.RegionGroup(r); len(g) > maxGroup {
							maxGroup = len(g)
						}
					}
					out := []registry.Choice{{
						Value:   "global",
						Display: fmt.Sprintf("%-*s  %-*s  %s", maxGroup, "—", maxID, "global", "all regions"),
					}}
					for _, r := range regions {
						loc := lightsail.RegionLocation(r)
						out = append(out, registry.Choice{
							Value:   r,
							Display: fmt.Sprintf("%-*s  %-*s  %s", maxGroup, lightsail.RegionGroup(r), maxID, r, loc),
						})
					}
					return out, nil
				},
			},
		},
		Run: func(_ context.Context, in registry.Input) error {
			if region == nil {
				return fmt.Errorf("no region pointer wired")
			}
			v := in.Get("region")
			if v == "global" {
				v = ""
			}
			*region = v
			return nil
		},
	}
}

// ContextLabel returns a func suitable for tui.Options.Context that shows
// the current region or "global" when unpinned.
func ContextLabel(region *string) func() string {
	return func() string {
		if region == nil || *region == "" {
			return "global"
		}
		return *region
	}
}
