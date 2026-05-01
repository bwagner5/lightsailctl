package app

import (
	"strings"

	"github.com/bwagner5/triad/pkg/registry"
)

func appDetail(item any) registry.DetailView {
	a, ok := asApp(item)
	if !ok {
		return registry.DetailView{}
	}

	return registry.DetailView{Sections: []registry.DetailSection{
		{
			Title: "Overview",
			Rows: []registry.DetailRow{
				{Label: "Name", Value: valueOr(a.Name, "unknown")},
				{Label: "Region", Value: valueOr(a.Region, "unknown")},
				{Label: "State", Value: valueOr(a.State, "unknown")},
				{Label: "Created", Value: valueOr(a.Age, "unknown")},
				{Label: "Config bucket", Value: valueOr(a.Bucket, "not created")},
			},
		},
		{
			Title: "Environments",
			Rows: []registry.DetailRow{
				{Label: "Names", Value: valueOr(a.Envs, "none")},
				{Label: "Health", Value: valueOr(a.Status, "not reported yet")},
			},
		},
		{
			Title: "Runtime",
			Rows: []registry.DetailRow{
				{Label: "Instances", Value: valueOr(a.Instances, "none discovered")},
				{Label: "Endpoints", Value: valueOr(a.Endpoints, "none reported")},
				{Label: "Next", Value: appNext(a)},
			},
		},
	}}
}

func asApp(item any) (App, bool) {
	switch v := item.(type) {
	case App:
		return v, true
	case *App:
		if v != nil {
			return *v, true
		}
	}
	return App{}, false
}

func appNext(a App) string {
	switch {
	case strings.TrimSpace(a.Envs) == "":
		return "deploy to create the first environment"
	case strings.TrimSpace(a.Instances) == "":
		return "deploy or attach an instance"
	case strings.TrimSpace(a.Status) == "":
		return "wait for health, then check status"
	default:
		return "open logs or deploy a new version"
	}
}

func valueOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
