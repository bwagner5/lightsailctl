package instance

import (
	"sort"
	"strings"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/lightsail"
)

func rowDetail(item any) registry.DetailView {
	r, ok := asRow(item)
	if !ok {
		return registry.DetailView{}
	}

	return registry.DetailView{Sections: []registry.DetailSection{
		{
			Title: "Overview",
			Rows: []registry.DetailRow{
				{Label: "Name", Value: valueOr(r.Name, "unknown")},
				{Label: "State", Value: valueOr(r.State, "unknown")},
				{Label: "Region", Value: valueOr(r.Region, "unknown")},
				{Label: "Created", Value: valueOr(r.Age, "unknown")},
				{Label: "Public IP", Value: valueOr(r.IP, "none")},
			},
		},
		{
			Title: "Plan",
			Rows: []registry.DetailRow{
				{Label: "Blueprint", Value: valueOr(r.Blueprint, "unknown")},
				{Label: "Bundle", Value: valueOr(r.Bundle, "unknown")},
			},
		},
		{
			Title: "Apps",
			Rows: []registry.DetailRow{
				{Label: "Targets", Value: valueOr(r.appTargets, "none discovered")},
				{Label: "Next", Value: rowNext(r)},
			},
		},
	}}
}

func asRow(item any) (Row, bool) {
	switch v := item.(type) {
	case Row:
		return v, true
	case *Row:
		if v != nil {
			return *v, true
		}
	}
	return Row{}, false
}

func appTargets(tags map[string]string) string {
	if len(tags) == 0 {
		return ""
	}
	var targets []string
	for key := range tags {
		if !strings.HasPrefix(key, lightsail.TagPrefix) {
			continue
		}
		target := strings.TrimPrefix(key, lightsail.TagPrefix)
		parts := strings.SplitN(target, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			continue
		}
		targets = append(targets, parts[0]+"/"+parts[1])
	}
	sort.Strings(targets)
	return strings.Join(targets, ", ")
}

func rowNext(r Row) string {
	switch {
	case r.State != "" && r.State != "running":
		return "start the instance before deploys or SSH"
	case strings.TrimSpace(r.IP) == "":
		return "wait for a public IP before SSH"
	case strings.TrimSpace(r.appTargets) == "":
		return "deploy an app to attach this instance"
	default:
		return "open SSH or deploy to an attached app"
	}
}

func valueOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
