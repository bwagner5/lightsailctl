package instance

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/bwagner5/triad/pkg/registry"

	lstypes "github.com/aws/aws-sdk-go-v2/service/lightsail/types"
)

func firewallOp(s *Store, suggest func(context.Context) ([]registry.Choice, error)) registry.Operation {
	return registry.Operation{
		Name: "firewall", Aliases: []string{"fw"}, Key: "f",
		Short:   "update instance firewall rules",
		Enabled: isRunning,
		Fields: []registry.Field{
			{Flag: "name", Short: "n", Help: "instance name", Required: true, Suggest: suggest},
			{Flag: "ports", Short: "p", Help: "ports to open (e.g. 22,80,443)", Required: true},
		},
		Steps: []registry.Step{
			{Label: "Resolve region", Do: resolveRegionStep(s)},
			{Label: "Update firewall", Do: firewallStep(s)},
		},
	}
}

func firewallStep(s *Store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		region, _ := st.Data["region"].(string)
		if region == "" {
			return fmt.Errorf("region not resolved")
		}
		rc := c.WithRegion(region)
		portsStr := st.Input.Get("ports")
		var rules []lstypes.PortInfo
		for _, p := range strings.Split(portsStr, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			port, err := strconv.Atoi(p)
			if err != nil {
				return fmt.Errorf("invalid port %q: %w", p, err)
			}
			rules = append(rules, lstypes.PortInfo{
				FromPort: int32(port),
				ToPort:   int32(port),
				Protocol: lstypes.NetworkProtocolTcp,
				Cidrs:    []string{"0.0.0.0/0"},
			})
		}
		if len(rules) == 0 {
			return fmt.Errorf("no ports specified")
		}
		return rc.PutInstancePublicPorts(ctx, st.Input.Get("name"), rules)
	}
}
