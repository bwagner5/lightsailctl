// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/lightsail"
	"github.com/aws/lightsailctl/pkg/names"
)

// App is one row in the apps table.
type App struct {
	Name      string
	Envs      string // comma-joined; "dev,prod"
	Region    string
	State     string
	Age       string
	Instances string // comma-joined target instances discovered via tags
	Endpoints string // comma-joined http://ip:port from status files
	Status    string // rolled-up health per env (e.g. "dev: healthy (2/2)")

	// envStatuses is populated by enrich (via Get) for the detail view.
	// Not shown in the table — only used by appDetail to render per-
	// instance container breakdowns. Keyed by env name.
	envStatuses map[string][]lightsail.Status

	// envBuckets maps env name → bucket name. Populated by aggregate
	// so enrich can look up status files without needing the account ID.
	envBuckets map[string]string
}

// store adapts lightsail.Client into a registry.Store. The client is built
// lazily on first call so `--help` / `--version` never touch AWS config.
//
// region is a pointer so main.go can bind it to a persistent --region flag
// that is only parsed at Execute time, long after Resource() runs.
// The empty string means "global" — list buckets/instances across every
// AWS region.
//
// regionHints are plumbed through to the Lightsail client so it can
// prioritize the fan-out order without any env-var reads below main.
type store struct {
	region      *string
	regionHints []string
	client      *lightsail.Client
	// nonInteractive is a pointer to the CLI's -y / --no-interactive
	// flag state, plumbed from main.go. The offer-CI tail step on
	// deploy reads it to know whether it's safe to prompt. Nil =
	// treated as interactive (legacy constructor compatibility).
	nonInteractive *bool
}

// Interactive reports whether the CLI is currently running with a
// user attached (i.e. not -y). Safe to call with a nil pointer.
func (s *store) Interactive() bool {
	if s.nonInteractive == nil {
		return true
	}
	return !*s.nonInteractive
}

func (s *store) currentRegion() string {
	if s.region == nil {
		return ""
	}
	return *s.region
}

func (s *store) ensure(ctx context.Context) (*lightsail.Client, error) {
	r := s.currentRegion()
	// Re-build if the requested region changed since last call.
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
	buckets, err := c.ListAppBuckets(ctx)
	if err != nil {
		return nil, err
	}
	return aggregate(buckets), nil
}

// StreamList fans out across regions and emits one batch per region as
// it completes. Rows carry only the fields derivable from the bucket
// list itself (Name, Envs, Region, State, Age). Everything else
// (Status, Instances, Endpoints) is populated per-row by Field.Async
// loaders in the TUI, which hit ReadBucketStatuses via the shared
// statuses cache — one real S3 round-trip per bucket per refresh no
// matter how many columns derive from it.
func (s *store) StreamList(ctx context.Context, _ registry.Filter) <-chan registry.Batch {
	out := make(chan registry.Batch, 16)
	go func() {
		defer close(out)
		c, err := s.ensure(ctx)
		if err != nil {
			out <- registry.Batch{Err: err}
			return
		}
		for b := range c.StreamBuckets(ctx) {
			if b.Err != nil {
				// Best-effort: skip regions where Lightsail isn't enabled
				// (error would otherwise show as a toast for every such region).
				continue
			}
			var appBuckets []lightsail.Bucket
			for _, bucket := range b.Buckets {
				if strings.HasPrefix(bucket.Name, lightsail.BucketPrefix) {
					appBuckets = append(appBuckets, bucket)
				}
			}
			if len(appBuckets) == 0 {
				continue
			}
			rows := aggregate(appBuckets)
			select {
			case <-ctx.Done():
				return
			case out <- registry.Batch{Items: rows}:
			}
		}
	}()
	return out
}

// enrich mutates each App row in rows with Instances (from ls:app:<name>:<env>
// tags) and Endpoints + Status (from <instance>_status.json files in the
// env buckets). Best-effort: any failure leaves fields blank.
//
// Used only by the single-row Get path (detail view). The TUI list view
// populates Status / Instances / Endpoints via Field.Async loaders that
// share a per-bucket memoized read through Client.FetchBucketStatuses.
func enrich(ctx context.Context, c *lightsail.Client, rows []any) {
	for i, it := range rows {
		a := it.(App)
		enrichOne(ctx, c, &a)
		rows[i] = a
	}
}

// statusLoader returns a Field.Async that summarizes container health
// per env. Reads each env bucket's status files through the Client's
// short-TTL memo, so the three async loaders on the same bucket
// (Status / Instances / Endpoints) share one S3 round-trip per refresh.
func statusLoader(s *store) func(ctx context.Context, item any) (string, error) {
	return func(ctx context.Context, item any) (string, error) {
		a, ok := item.(App)
		if !ok {
			return "", nil
		}
		c, rerr := s.ensure(ctx)
		if rerr != nil {
			return "", rerr
		}
		rc := c.WithRegion(a.Region)
		var parts []string
		for _, env := range splitEnvs(a.Envs) {
			bucket := a.envBuckets[env]
			if bucket == "" {
				continue
			}
			statuses, err := rc.FetchBucketStatuses(ctx, bucket)
			if err != nil || len(statuses) == 0 {
				continue
			}
			healthy, total := 0, 0
			for _, st := range statuses {
				for _, ctr := range st.Containers {
					total++
					if ctr.Status == "running" {
						healthy++
					}
				}
			}
			if total == 0 {
				continue
			}
			if len(statuses) > 1 {
				parts = append(parts, fmt.Sprintf("%s: %d/%d on %d instances", env, healthy, total, len(statuses)))
			} else {
				parts = append(parts, fmt.Sprintf("%s: %d/%d", env, healthy, total))
			}
		}
		return strings.Join(parts, " · "), nil
	}
}

// instancesLoader returns a Field.Async that lists the instance names
// currently reporting status in any env bucket. Derived from the same
// ReadBucketStatuses memo as Status / Endpoints — no separate
// ListInstances / tag lookup. Trade-off: an instance tagged as a
// target but not yet reporting a status file won't appear in the
// table; the detail view still surfaces targets via the tag path.
func instancesLoader(s *store) func(ctx context.Context, item any) (string, error) {
	return func(ctx context.Context, item any) (string, error) {
		a, ok := item.(App)
		if !ok {
			return "", nil
		}
		c, rerr := s.ensure(ctx)
		if rerr != nil {
			return "", rerr
		}
		rc := c.WithRegion(a.Region)
		names := map[string]struct{}{}
		for _, env := range splitEnvs(a.Envs) {
			bucket := a.envBuckets[env]
			if bucket == "" {
				continue
			}
			statuses, err := rc.FetchBucketStatuses(ctx, bucket)
			if err != nil {
				continue
			}
			for _, st := range statuses {
				if st.Instance != "" {
					names[st.Instance] = struct{}{}
				}
			}
		}
		if len(names) == 0 {
			return "", nil
		}
		out := make([]string, 0, len(names))
		for n := range names {
			out = append(out, n)
		}
		sort.Strings(out)
		return strings.Join(out, ","), nil
	}
}

// endpointsLoader returns a Field.Async that concatenates live endpoint
// URLs across every env's status files. Shares the ReadBucketStatuses
// memo with Status and Instances.
func endpointsLoader(s *store) func(ctx context.Context, item any) (string, error) {
	return func(ctx context.Context, item any) (string, error) {
		a, ok := item.(App)
		if !ok {
			return "", nil
		}
		c, rerr := s.ensure(ctx)
		if rerr != nil {
			return "", rerr
		}
		rc := c.WithRegion(a.Region)
		var endpoints []string
		for _, env := range splitEnvs(a.Envs) {
			bucket := a.envBuckets[env]
			if bucket == "" {
				continue
			}
			statuses, err := rc.FetchBucketStatuses(ctx, bucket)
			if err != nil {
				continue
			}
			for _, st := range statuses {
				endpoints = append(endpoints, st.Endpoints...)
			}
		}
		if len(endpoints) == 0 {
			return "", nil
		}
		return strings.Join(dedupeStrings(endpoints), ","), nil
	}
}

// splitEnvs parses a comma-joined env list and drops empty entries so
// loaders don't need to repeat the strings.Split / empty-skip boilerplate.
func splitEnvs(csv string) []string {
	if csv == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// enrichOne fills Instances / Endpoints / Status + envStatuses for a
// single App using region-pinned client c. Best-effort — partial data
// leaves the corresponding fields blank.
func enrichOne(ctx context.Context, c *lightsail.Client, a *App) {
	instances := map[string]struct{}{}
	var endpoints []string
	var statusParts []string
	for _, env := range strings.Split(a.Envs, ",") {
		if env == "" {
			continue
		}
		targets, _ := c.FindTargetsForAppEnv(ctx, a.Name, env)
		for _, t := range targets {
			instances[t.Name] = struct{}{}
		}
		// Status from env bucket (best-effort; bucket may not exist yet).
		envBucket := a.envBuckets[env]
		if envBucket == "" {
			continue
		}
		if a.envStatuses == nil {
			a.envStatuses = map[string][]lightsail.Status{}
		}
		statuses, err := c.ReadBucketStatuses(ctx, envBucket)
		if err != nil {
			a.envStatuses[env] = nil
			continue
		}
		a.envStatuses[env] = statuses
		healthy, total := 0, 0
		for _, st := range statuses {
			for _, ctr := range st.Containers {
				total++
				if ctr.Status == "running" {
					healthy++
				}
			}
			endpoints = append(endpoints, st.Endpoints...)
		}
		if total > 0 {
			if len(statuses) > 1 {
				statusParts = append(statusParts, fmt.Sprintf("%s: %d/%d on %d instances", env, healthy, total, len(statuses)))
			} else {
				statusParts = append(statusParts, fmt.Sprintf("%s: %d/%d", env, healthy, total))
			}
		}
	}
	if len(instances) > 0 {
		names := make([]string, 0, len(instances))
		for n := range instances {
			names = append(names, n)
		}
		sort.Strings(names)
		a.Instances = strings.Join(names, ",")
	}
	if len(endpoints) > 0 {
		a.Endpoints = strings.Join(dedupeStrings(endpoints), ",")
	}
	if len(statusParts) > 0 {
		a.Status = strings.Join(statusParts, " · ")
	}
}

func dedupeStrings(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// aggregate rolls a flat list of app-prefixed buckets into App rows.
// Apps are discovered by grouping env buckets by their app name prefix.
func aggregate(buckets []lightsail.Bucket) []any {
	byName := map[string]*App{}
	created := map[string]time.Time{} // earliest bucket CreatedAt per app
	for _, b := range buckets {
		app, env := lightsail.ParseAppEnv(b.Name)
		if app == "" || env == "" {
			continue
		}
		a := byName[app]
		if a == nil {
			a = &App{Name: app, Region: b.Region, envBuckets: map[string]string{}}
			byName[app] = a
		}
		if a.Envs == "" {
			a.Envs = env
		} else {
			a.Envs += "," + env
		}
		a.envBuckets[env] = b.Name
		if a.State == "" || b.State != "OK" {
			a.State = b.State
		}
		if !b.CreatedAt.IsZero() && (created[app].IsZero() || b.CreatedAt.Before(created[app])) {
			created[app] = b.CreatedAt
		}
	}
	out := make([]any, 0, len(byName))
	for _, a := range byName {
		a.Envs = sortedCSV(a.Envs)
		if t, ok := created[a.Name]; ok && !t.IsZero() {
			a.Age = t.Format(time.RFC3339)
		}
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].(App).Name < out[j].(App).Name })
	return out
}

func (s *store) Get(ctx context.Context, id string) (any, error) {
	c, err := s.ensure(ctx)
	if err != nil {
		return nil, err
	}
	items, err := s.List(ctx, registry.Filter{})
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if a := it.(App); a.Name == id {
			// Enrich just this one row on demand — cheap enough for a
			// single app and lets the detail view populate
			// Instances / Endpoints / Status without blocking the list.
			row := []any{a}
			enrich(ctx, c.WithRegion(a.Region), row)
			return row[0], nil
		}
	}
	return nil, notFound(id)
}

type notFound string

func (n notFound) Error() string { return "app not found: " + string(n) }

func sortedCSV(csv string) string {
	if csv == "" {
		return ""
	}
	parts := strings.Split(csv, ",")
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// Resource builds the triad Resource. The region pointer is bound to a
// persistent --region flag in main.go; "" means global (fan out across
// all regions for list ops). regionHints are env-resolved in main and
// used to reorder fan-out — no env reads happen below main.
//
// Operations layer in as we port each phase: Phase 1 adds status+delete;
// Phase 3 adds deploy; Phase 4 adds create; Phase 5 adds logs+local.
//
// The client is built lazily on first Store call so `--help` / `--version`
// and offline TUI launches never touch AWS config.
// Resource builds the triad Resource. See ResourceWithOptions for the
// full-featured constructor; this thin wrapper keeps the legacy
// positional-args shape working.
func Resource(region *string, regionHints []string) registry.Resource {
	return ResourceWithOptions(region, regionHints, nil)
}

// ResourceWithOptions is the full constructor. nonInteractive is a
// pointer to the caller's -y / --no-interactive flag state. When set
// and true, the deploy saga's offer-CI tail step is skipped — we
// don't silently provision IAM for an unattended session. Passing nil
// is equivalent to "always interactive".
func ResourceWithOptions(region *string, regionHints []string, nonInteractive *bool) registry.Resource {
	st := &store{region: region, regionHints: regionHints, nonInteractive: nonInteractive}
	fields := []registry.Field{
		{Name: "Name", Flag: "name", Short: "n", Help: "application name",
			Prefill: names.DefaultAppName, Table: registry.TableHint{Header: "NAME"}},
		{Name: "Envs", Flag: "envs", Help: "environments",
			Table: registry.TableHint{Header: "ENVS"}},
		{Name: "Region", Flag: "region-field", Help: "AWS region",
			Table: registry.TableHint{Header: "REGION"}},
		{Name: "State", Flag: "state", Help: "bucket state",
			Table: registry.TableHint{Header: "STATE"}},
		{Name: "Age", Flag: "age", Help: "created",
			Table: registry.TableHint{Header: "AGE", Tick: true}},
		{Name: "Status", Flag: "status", Help: "rolled-up health",
			Table: registry.TableHint{Header: "STATUS"},
			Async: statusLoader(st)},
		{Name: "Instances", Flag: "instances", Help: "target instances",
			Table: registry.TableHint{Header: "INSTANCES", Wide: true},
			Async: instancesLoader(st)},
		{Name: "Endpoints", Flag: "endpoints", Help: "live endpoints",
			Table: registry.TableHint{Header: "ENDPOINTS", Wide: true},
			Async: endpointsLoader(st)},
	}
	suggest := registry.SuggestFrom(st, fields, "name")
	return registry.Resource{
		Name:    "app",
		Plural:  "apps",
		Aliases: []string{"application", "applications"},
		Short:   "manage Lightsail Applications",
		Fields:  fields,
		Store:   st,
		Detail:  appDetail,
		Shutdown: func() {
			if st.client != nil {
				st.client.CloseKeyCache()
			}
		},
		Operations: map[string]registry.Operation{
			"create":            createOp(st),
			"delete":            deleteOp(st, suggest),
			"deploy":            deployOp(st),
			"logs":              logsOp(st, suggest),
			"add-target":        addTargetOp(st),
			"remove-target":     removeTargetOp(st, suggest),
			"enable-gh-action":  enableGhActionOp(st, suggest),
			"disable-gh-action": disableGhActionOp(st, suggest),
		},
	}
}
