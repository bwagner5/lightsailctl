package app

import (
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/bwagner5/triad/pkg/registry"
	"github.com/bwagner5/triad/pkg/ui/theme"

	"github.com/aws/lightsailctl/pkg/ghaction"
	"github.com/aws/lightsailctl/pkg/lightsail"
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
			sections = append(sections, envSections(env, a)...)
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

// envSections renders ONE environment into one or two sections:
// an "Environment: <env>" section (instance / health / last deploy),
// followed by a "Services (<env>)" section per instance that has at
// least one container. Services is split out so container rows sit
// under an explicit heading rather than free-floating under the env.
func envSections(env string, a App) []registry.DetailSection {
	statuses := a.envStatuses[env]
	envTitle := "Environment: " + env

	if len(statuses) == 0 {
		return []registry.DetailSection{{
			Title: envTitle,
			Rows:  []registry.DetailRow{{Label: "Status", Value: "not deployed"}},
		}}
	}

	var out []registry.DetailSection
	for _, st := range statuses {
		envRows := []registry.DetailRow{
			{Label: "Instance", Value: st.Instance},
			{Label: "Health", Value: statusBadge(st.Status)},
		}
		if st.LastDeploy != nil && !st.LastDeploy.Timestamp.IsZero() {
			envRows = append(envRows, registry.DetailRow{
				Label: "Last deploy",
				Value: relativeTime(st.LastDeploy.Timestamp) + "  (" + st.LastDeploy.Timestamp.Format("Jan 2 15:04 MST") + ")",
			})
		}
		out = append(out, registry.DetailSection{Title: envTitle, Rows: envRows})

		if len(st.Containers) == 0 {
			// Fall back to flat endpoints if we have any but no
			// containers to attach them to (rare, but keeps the
			// information visible).
			if len(st.Endpoints) > 0 {
				var epRows []registry.DetailRow
				for _, ep := range st.Endpoints {
					epRows = append(epRows, registry.DetailRow{Label: "  Endpoint", Value: ep})
				}
				out = append(out, registry.DetailSection{Title: "Endpoints (" + env + ")", Rows: epRows})
			}
			continue
		}

		svcRows := serviceRows(st)
		out = append(out, registry.DetailSection{Title: "Services (" + env + ")", Rows: svcRows})
	}
	return out
}

// serviceRows builds the indented per-service rows:
//
//	svc1: ● running  up 4m ago
//	  Endpoint: http://1.2.3.4:8080
//
// It prefers ContainerStatus.Service (the compose service name) over
// Name (the concrete container), because the service name is what
// the user wrote in compose.yml and what they use with `docker
// compose logs <svc>`. Image is shown only when it's a real registry
// reference (has "/" or ":"), not when compose auto-generated it from
// the project/service pair (e.g. "current-svc1").
func serviceRows(st lightsail.Status) []registry.DetailRow {
	var rows []registry.DetailRow
	for _, c := range st.Containers {
		label := "  " + serviceLabel(c)
		val := statusBadge(c.Status)
		if !c.StartedAt.IsZero() {
			val += "  up " + relativeTime(c.StartedAt)
		}
		if showImage(c) {
			val += "  " + c.Image
		}
		rows = append(rows, registry.DetailRow{Label: label, Value: val})

		// Prefer per-container endpoints. Fall back to nothing if
		// unset; the env-level Endpoints have already been split
		// across containers by the watcher.
		for _, ep := range c.Endpoints {
			rows = append(rows, registry.DetailRow{Label: "    Endpoint", Value: ep})
		}
	}

	// If the watcher reported env-level endpoints but none were
	// attributed to containers (older watchers, pre per-container
	// Endpoints), append them as unattributed rows so the information
	// isn't lost.
	if !anyContainerEndpoints(st.Containers) {
		for _, ep := range st.Endpoints {
			rows = append(rows, registry.DetailRow{Label: "  Endpoint", Value: ep})
		}
	}
	return rows
}

// serviceLabel returns the name to show on a container row. Prefers
// the compose service name; falls back to the concrete container name.
func serviceLabel(c lightsail.ContainerStatus) string {
	if c.Service != "" {
		return c.Service
	}
	return c.Name
}

// showImage returns true when c.Image is worth displaying. Compose
// auto-generates names like "<project>-<service>" for built (vs.
// pulled) images, which duplicates the service name and clutters the
// view. We treat anything without a tag or registry path as
// auto-generated and hide it.
func showImage(c lightsail.ContainerStatus) bool {
	if c.Image == "" {
		return false
	}
	if strings.Contains(c.Image, "/") || strings.Contains(c.Image, ":") {
		return true
	}
	return false
}

func anyContainerEndpoints(cs []lightsail.ContainerStatus) bool {
	for _, c := range cs {
		if len(c.Endpoints) > 0 {
			return true
		}
	}
	return false
}

// statusBadge returns a colorized "<symbol> <status>" string. Intended
// for DetailRow.Value, which the TUI renders as-is (lipgloss escapes
// pass through). CLI paths don't use this — they show the raw status
// word via op_status.go's table renderers.
func statusBadge(s string) string {
	sym, style := badgeStyle(s)
	if style == nil {
		return valueOr(s, "unknown")
	}
	return style.Render(sym + " " + s)
}

// badgeStyle maps a status word to its (glyph, style) pair. Returns a
// nil style for unknown/empty statuses; the caller falls back to plain
// text.
//
//	● healthy / running / ok       → green (Success)
//	◐ starting / restarting /       → yellow (Warning)
//	   created / degraded /
//	   pending / idle
//	○ down / unhealthy / exited /   → red (Danger)
//	   dead / failed / error
//	○ paused / removing             → muted
func badgeStyle(s string) (string, *lipgloss.Style) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "healthy", "running", "ok":
		st := theme.OK
		return "●", &st
	case "starting", "restarting", "created", "degraded", "pending", "idle":
		st := theme.Warn
		return "◐", &st
	case "down", "unhealthy", "exited", "dead", "failed", "error":
		st := theme.Err
		return "○", &st
	case "paused", "removing":
		st := theme.MutedText
		return "○", &st
	default:
		return "", nil
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
