package instance

import (
	"context"
	"fmt"

	"github.com/bwagner5/triad/pkg/registry"
)

func deleteOp(s *store, suggest func(context.Context) ([]registry.Choice, error)) registry.Operation {
	return registry.Operation{
		Name: "delete", Aliases: []string{"rm"}, Key: "ctrl+d",
		Short:   "delete a Lightsail instance",
		Confirm: "Delete this instance? This is irreversible.",
		Fields: []registry.Field{
			{Flag: "name", Short: "n", Help: "instance name", Required: true, Suggest: suggest},
		},
		Steps: []registry.Step{
			{Label: "Resolve region", Do: resolveRegionStep(s)},
			{Label: "Delete instance", Do: deleteInstanceStep(s)},
		},
	}
}

func resolveRegionStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		inst, err := c.GetInstance(ctx, st.Input.Get("name"))
		if err != nil {
			return fmt.Errorf("resolve region: %w", err)
		}
		st.Data["region"] = inst.Region
		return nil
	}
}

func deleteInstanceStep(s *store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		region := st.Data["region"].(string)
		return c.WithRegion(region).DeleteInstance(ctx, st.Input.Get("name"))
	}
}
