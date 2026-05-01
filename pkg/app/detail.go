package app

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/ghaction"
)

func appDetail(item any) registry.DetailView {
	a, ok := asApp(item)
	if !ok {
		return registry.DetailView{}
	}

	sections := []registry.DetailSection{
		{Title: "Application", Rows: overviewRows(a)},
	}

	if len(a.envStatuses) > 0 {
		for _, env := range sortedEnvs(a.Envs) {
			sections = append(sections, envSection(env, a))
		}
	} else if a.Envs != "" {
		// Unenriched fallback (list view, before Get completes).
		sections = append(sections, registry.DetailSection{
			Title: "Environments",
			Rows: []registry.DetailRow{
				{Label: "Names", Value: a.Envs},
				{Label: "Instances", Value: valueOr(a.Instances, "—")},
				{Label: "Endpoints", Value: valueOr(a.Endpoints, "—")},
				{Label: "Health", Value: valueOr(a.Status, "loading…")},
			},
		})
	} else {
		sections = append(sections, registry.DetailSection{
			Title: "Environments",
			Rows: []registry.DetailRow{
				{Label: "Status", Value: "no environments yet — run deploy to create one"},
			},
		})
	}

	return registry.DetailView{Sections: sections}
}

func overviewRows(a App) []registry.DetailRow {
	rows := []registry.DetailRow{
		{Label: "Name", Value: a.Name},
		{Label: "Region", Value: valueOr(a.Region, "—")},
		{Label: "Created", Value: formatAge(a.Age)},
	}
	// Best-effort: surface the GitHub repo from the local git remote.
	if cwd, err := os.Getwd(); err == nil {
		if raw, _ := ghaction.DetectRemoteURL(cwd); raw != "" {
			if ref, err := ghaction.ParseRemoteURL(raw); err == nil {
				rows = append(rows, registry.DetailRow{
					Label: "Repository",
					Value: "https://github.com/" + ref.String(),
				})
			}
		}
	}
	return rows
}

func envSection(env string, a App) registry.DetailSection {
	statuses := a.envStatuses[env]
	title := "Environment: " + env
	var rows []registry.DetailRow

	if len(statuses) == 0 {
		rows = append(rows, registry.DetailRow{Label: "Status", Value: "no status reported"})
		return registry.DetailSection{Title: title, Rows: rows}
	}

	for _, st := range statuses {
		rows = append(rows, registry.DetailRow{Label: "Instance", Value: st.Instance})
		rows = append(rows, registry.DetailRow{Label: "Health", Value: statusBadge(st.Status)})

		if st.LastDeploy != nil && !st.LastDeploy.Timestamp.IsZero() {
			rows = append(rows, registry.DetailRow{
				Label: "Last deploy",
				Value: relativeTime(st.LastDeploy.Timestamp) + "  (" + st.LastDeploy.Timestamp.Format("Jan 2 15:04 MST") + ")",
			})
		}

		for _, c := range st.Containers {
			val := statusBadge(c.Status)
			if !c.StartedAt.IsZero() {
				val += "  up " + relativeTime(c.StartedAt)
			}
			if c.Image != "" {
				val += "  " + c.Image
			}
			rows = append(rows, registry.DetailRow{Label: "  " + c.Name, Value: val})
		}

		for _, ep := range st.Endpoints {
			rows = append(rows, registry.DetailRow{Label: "Endpoint", Value: ep})
		}
	}

	return registry.DetailSection{Title: title, Rows: rows}
}

func statusBadge(s string) string {
	switch s {
	case "healthy", "running":
		return "● " + s
	case "degraded":
		return "◐ " + s
	case "down":
		return "○ " + s
	default:
		return valueOr(s, "unknown")
	}
}

func formatAge(age string) string {
	if age == "" {
		return "—"
	}
	t, err := time.Parse(time.RFC3339, age)
	if err != nil {
		return age
	}
	return relativeTime(t) + "  (" + t.Format("Jan 2 15:04 MST") + ")"
}

func sortedEnvs(csv string) []string {
	if csv == "" {
		return nil
	}
	return strings.Split(csv, ",")
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

func valueOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func relativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
