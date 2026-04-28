// Package instance defines the triad Resource for Lightsail Instances.
package instance

import (
	"context"
	"sort"
	"time"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/lightsail"
)

// Row is one row in the instances table.
type Row struct {
	Name      string
	State     string
	Age       string
	IP        string
	Region    string
	Blueprint string
	Bundle    string
}

type store struct {
	region      *string
	regionHints []string
	client      *lightsail.Client
}

func (s *store) currentRegion() string {
	if s.region == nil {
		return ""
	}
	return *s.region
}

func (s *store) ensure(ctx context.Context) (*lightsail.Client, error) {
	r := s.currentRegion()
	if s.client != nil && s.client.Region() == r {
		return s.client, nil
	}
	c, err := lightsail.NewWithOptions(ctx, lightsail.Options{
		Region:      r,
		RegionHints: s.regionHints,
	})
	if err != nil {
		return nil, err
	}
	s.client = c
	return c, nil
}

func (s *store) List(ctx context.Context, _ registry.Filter) ([]any, error) {
	c, err := s.ensure(ctx)
	if err != nil {
		return nil, err
	}
	instances, err := c.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]any, len(instances))
	for i, inst := range instances {
		out[i] = Row{
			Name:      inst.Name,
			State:     inst.State,
			Age:       formatTime(inst.CreatedAt),
			IP:        inst.IP,
			Region:    inst.Region,
			Blueprint: inst.Blueprint,
			Bundle:    inst.Bundle,
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].(Row).Name < out[j].(Row).Name })
	return out, nil
}

func (s *store) Get(ctx context.Context, id string) (any, error) {
	c, err := s.ensure(ctx)
	if err != nil {
		return nil, err
	}
	inst, err := c.GetInstance(ctx, id)
	if err != nil {
		return nil, err
	}
	return Row{
		Name:      inst.Name,
		State:     inst.State,
		Age:       formatTime(inst.CreatedAt),
		IP:        inst.IP,
		Region:    inst.Region,
		Blueprint: inst.Blueprint,
		Bundle:    inst.Bundle,
	}, nil
}

// Resource builds the triad Resource for Lightsail Instances.
func Resource(region *string, regionHints []string) registry.Resource {
	fields := []registry.Field{
		{Name: "Name", Flag: "name", Short: "n", Help: "instance name",
			Table: registry.TableHint{Header: "NAME"}},
		{Name: "State", Flag: "state", Help: "instance state",
			Table: registry.TableHint{Header: "STATE"}},
		{Name: "Age", Flag: "age", Help: "running duration",
			Table: registry.TableHint{Header: "AGE", Tick: true}},
		{Name: "IP", Flag: "ip", Help: "public IP",
			Table: registry.TableHint{Header: "IP"}},
		{Name: "Region", Flag: "region-field", Help: "AWS region",
			Table: registry.TableHint{Header: "REGION"}},
		{Name: "Blueprint", Flag: "blueprint", Help: "OS / image",
			Table: registry.TableHint{Header: "BLUEPRINT", Wide: true}},
		{Name: "Bundle", Flag: "bundle", Help: "instance size",
			Table: registry.TableHint{Header: "BUNDLE", Wide: true}},
	}
	st := &store{region: region, regionHints: regionHints}
	suggest := registry.SuggestFrom(st, fields, "name")
	return registry.Resource{
		Name:    "instance",
		Plural:  "instances",
		Aliases: []string{"inst", "vps"},
		Short:   "manage Lightsail instances",
		Fields:  fields,
		Store:   st,
		Operations: map[string]registry.Operation{
			"create":   createOp(st),
			"delete":   deleteOp(st, suggest),
			"start":    startOp(st, suggest),
			"stop":     stopOp(st, suggest),
			"firewall": firewallOp(st, suggest),
			"ssh":      sshOp(st, suggest),
		},
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func isRunning(item any) bool {
	r, ok := item.(Row)
	return ok && r.State == "running"
}

func isStopped(item any) bool {
	r, ok := item.(Row)
	return ok && r.State == "stopped"
}