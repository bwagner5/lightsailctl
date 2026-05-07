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

// envSections renders one environment as a single "Environment: <env>"
// section whose rows encode the full instance → service → endpoint
// hierarchy via label indentation. Tree shape:
//
//	Environment: dev
//	  <instance-a>:   ● healthy  deployed 5m ago
//	    <svc1>:       ● running  up 5m ago
//	      Endpoint:   http://…:8080
//	    <svc2>:       ● running  up 5m ago
//	      Endpoint:   http://…:8081
//	  <instance-b>:   ● healthy  deployed 5m ago
//	    <svc1>:       ● running  up 5m ago
//	      Endpoint:   http://…:8080
//
// Rationale: the prior layout emitted two sections per instance
// ("Environment: dev" + "Services (dev)"), so a two-instance env
// produced four sibling sections with the same "Environment: dev"
// heading repeated twice. Users couldn't tell at a glance which
// services belonged to which instance, and the duplicated headings
// read as a rendering bug rather than a hierarchy. One section per
// env with indented rows gives a single anchor and makes ownership
// obvious.
func envSections(env string, a App) []registry.DetailSection {
	statuses := a.envStatuses[env]
	title := "Environment: " + env

	if len(statuses) == 0 {
		return []registry.DetailSection{{
			Title: title,
			Rows:  []registry.DetailRow{{Label: "Status", Value: "not deployed"}},
		}}
	}

	var rows []registry.DetailRow
	for _, st := range statuses {
		rows = append(rows, instanceRows(st)...)
	}
	return []registry.DetailSection{{Title: title, Rows: rows}}
}

// instanceRows renders one Status (= one instance's watcher report)
// as a header row plus nested service / endpoint rows. Indentation
// convention: 0 = instance, 2 = service, 4 = endpoint.
func instanceRows(st lightsail.Status) []registry.DetailRow {
	// Header row: instance name as label, health + last-deploy as value.
	// Keeping the timestamp inside the value (instead of a dedicated
	// "Last deploy" row) keeps each instance block compact and avoids
	// repeating three label columns per instance.
	//
	// The label suffix " (instance)" makes the row's role explicit so
	// readers can tell at a glance that "burning-nebula" is a
	// Lightsail instance rather than, say, another environment name
	// or a service. Indented child rows (services, endpoints) carry
	// no suffix because their indentation already signals their kind.
	val := statusBadge(st.Status)
	if st.LastDeploy != nil && !st.LastDeploy.Timestamp.IsZero() {
		val += "  deployed " + relativeTime(st.LastDeploy.Timestamp) +
			"  (" + st.LastDeploy.Timestamp.Format("Jan 2 15:04 MST") + ")"
	}
	rows := []registry.DetailRow{{Label: st.Instance + " (instance)", Value: val}}

	// Services & endpoints indented under the instance. If there are
	// no containers yet (e.g. idle, still bootstrapping) but we have
	// env-level endpoints, surface them as unattributed rows under
	// the instance so the information isn't lost.
	if len(st.Containers) == 0 {
		for _, ep := range st.Endpoints {
			rows = append(rows, registry.DetailRow{Label: "  Endpoint", Value: ep})
		}
		return rows
	}
	for _, c := range st.Containers {
		cval := statusBadge(c.Status)
		if !c.StartedAt.IsZero() {
			cval += "  up " + relativeTime(c.StartedAt)
		}
		if showImage(c) {
			cval += "  " + c.Image
		}
		rows = append(rows, registry.DetailRow{Label: "  " + serviceLabel(c), Value: cval})
		for _, ep := range c.Endpoints {
			rows = append(rows, registry.DetailRow{Label: "    Endpoint", Value: ep})
		}
	}
	// Compat: older watchers set Status.Endpoints but not per-container
	// Endpoints. Attach those to the instance (indented one level past
	// the instance, same as Container endpoints' first level) so the
	// user can still see them.
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
