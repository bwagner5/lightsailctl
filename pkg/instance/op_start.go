package instance

import (
	"context"
	"fmt"

	"github.com/bwagner5/triad/pkg/registry"
)

func startOp(s *store, suggest func(context.Context) ([]registry.Choice, error)) registry.Operation {
	return registry.Operation{
		Name: "start", Key: "s", Short: "start a stopped instance",
		Enabled: isStopped,
		Fields: []registry.Field{
			{Flag: "name", Short: "n", Help: "instance name", Required: true, Suggest: suggest},
		},
		Steps: []registry.Step{
			{Label: "Resolve region", Do: resolveRegionStep(s)},
			{Label: "Start instance", Do: startInstanceStep(s)},
		},
	}
}

func startInstanceStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		region, _ := st.Data["region"].(string)
		if region == "" {
			return fmt.Errorf("region not resolved")
		}
		return c.WithRegion(region).StartInstance(ctx, st.Input.Get("name"))
	}
}
