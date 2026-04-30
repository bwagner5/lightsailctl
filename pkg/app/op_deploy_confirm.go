package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/config"
)

// confirmDeployStep shows a grouped summary of the resolved deploy
// inputs and asks the user to confirm before any mutating work runs.
//
// The first invocation returns a NeedInput for a single yes/no field
// with a multi-line Reason containing the rendered summary. The second
// invocation sees "deploy-confirm=true" in Input and returns nil.
// A "no" answer aborts the saga with a clear error.
//
// The step is skipped under -y (see skipConfirm): unattended callers
// have already opted in to the whole command by setting all the flags,
// so asking them again serves no one.
func confirmDeployStep(s *store) func(context.Context, *registry.State) error {
	return func(_ context.Context, st *registry.State) error {
		if v := st.Input.Get("deploy-confirm"); v != "" {
			if asBool(v) {
				return nil
			}
			return fmt.Errorf("deploy aborted by user")
		}
		summary := renderDeploySummary(st)
		return &registry.NeedInput{
			Reason: summary,
			Fields: []registry.Field{
				{Flag: "deploy-confirm", Required: true,
					Help: "proceed with this deploy",
					Suggest: yesNoSuggest(
						"No, abort",
						"Yes, deploy now"),
				},
			},
		}
	}
}

// skipConfirm returns true when we shouldn't show the review prompt:
// non-interactive mode, or the user already passed --deploy-confirm.
func skipConfirm(s *store) func(*registry.State) bool {
	return func(st *registry.State) bool {
		if !s.Interactive() {
			return true
		}
		// Already answered (e.g. via flag) — no need to re-prompt.
		return st.Input.Get("deploy-confirm") != ""
	}
}

// renderDeploySummary composes the multi-section summary shown to the
// user at confirm time. Each section is a titled group of key/value
// pairs; empty sections are omitted. Uses plain indentation (no box
// drawing) so it reads cleanly through every terminal.
func renderDeploySummary(st *registry.State) string {
	var b strings.Builder
	b.WriteString("Review the deploy plan and confirm below.\n")

	// Application section.
	writeSection(&b, "Application", [][2]string{
		{"Name", st.Input.Get("name")},
		{"Environment", st.Input.Get("env")},
		{"Region", st.Input.Get("region")},
	})

	// Instance section — branches on strategy.
	strategy, _ := st.Data["strategy"].(string)
	switch strategy {
	case "create-new":
		rows := [][2]string{
			{"Action", "create new"},
			{"Name", st.Input.Get("__ni/name")},
			{"Region", st.Input.Get("__ni/region")},
			{"Blueprint", st.Input.Get("__ni/blueprint")},
			{"Bundle", st.Input.Get("__ni/bundle")},
		}
		writeSection(&b, "Lightsail Instance", rows)
	case "use-existing":
		writeSection(&b, "Lightsail Instance", [][2]string{
			{"Action", "use existing"},
			{"Name", st.Input.Get("instance")},
		})
	}

	// Source + agent section — small but useful for debugging.
	rows := [][2]string{}
	if cfg, _ := config.LoadFromCwd(); cfg != nil && cfg.Path != "" {
		rows = append(rows, [2]string{"Config file", cfg.Path})
	}
	if agent := st.Input.Get("agent-path"); agent != "" {
		rows = append(rows, [2]string{"Agent binary", agent})
	}
	if len(rows) > 0 {
		writeSection(&b, "Source", rows)
	}

	return b.String()
}

// writeSection writes a "Title" header followed by indented "key value"
// rows. Empty values are skipped, and if every row is empty the whole
// section is elided.
func writeSection(b *strings.Builder, title string, rows [][2]string) {
	// Filter empties.
	var kept [][2]string
	maxKey := 0
	for _, r := range rows {
		if strings.TrimSpace(r[1]) == "" {
			continue
		}
		kept = append(kept, r)
		if n := len(r[0]); n > maxKey {
			maxKey = n
		}
	}
	if len(kept) == 0 {
		return
	}
	fmt.Fprintf(b, "\n%s\n", title)
	for _, r := range kept {
		fmt.Fprintf(b, "  %-*s  %s\n", maxKey, r[0], r[1])
	}
}
