package app

import (
	"context"
	"fmt"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/lightsail"
)

// RegionSwitchOp returns a triad GlobalOp that lets TUI users switch the
// CLI's effective region at runtime. Bound to key "g" (for "global" →
// pick a scope).
//
// The field is Required so the wizard always fires — even when the input
// happens to be "". Triad's missing-required check skips fields that are
// already set; since the TUI passes an empty Input, the picker opens every
// time the op is invoked.
func RegionSwitchOp(region *string) registry.Operation {
	return registry.Operation{
		Name: "region", Key: "g", Short: "switch region (blank = global)",
		Fields: []registry.Field{
			{
				Flag: "region", Required: true, Help: "AWS region (blank = global)",
				Suggest: func(ctx context.Context) ([]registry.Choice, error) {
					c, err := lightsail.New(ctx, "")
					if err != nil {
						return nil, err
					}
					regions, err := c.FetchRegions(ctx)
					if err != nil {
						return nil, err
					}
					// Lead with "global" so it's pickable from the list.
					out := []registry.Choice{{Value: "global", Display: "global (all regions)"}}
					for _, r := range regions {
						out = append(out, registry.Choice{Value: r, Display: r})
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
