// Package app defines the triad Resource for Lightsail Applications.
//
// An "application" is a client-side aggregate: it's every bucket matching
// ls--<acct>--<app>[--<env>] grouped by <app>. Environments are strings
// hanging off an app, discoverable via the env-suffixed buckets.
package app

import (
	"context"
	"sort"
	"strings"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/lightsail"
	"github.com/aws/lightsailctl/pkg/names"
)

// App is one row in the apps table.
type App struct {
	Name   string
	Envs   string // comma-joined; "dev,prod"
	Region string
	State  string
	Bucket string // app-config bucket name (for detail view)
}

// store adapts lightsail.Client into a registry.Store. The client is built
// lazily on first call so `--help` / `--version` never touch AWS config.
type store struct {
	region string
	client *lightsail.Client
}

func (s *store) ensure(ctx context.Context) (*lightsail.Client, error) {
	if s.client != nil {
		return s.client, nil
	}
	c, err := lightsail.New(ctx, s.region)
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

// aggregate rolls a flat list of app-prefixed buckets into App rows.
func aggregate(buckets []lightsail.Bucket) []any {
	byName := map[string]*App{}
	for _, b := range buckets {
		// env bucket
		if app, env := lightsail.ParseAppEnv(b.Name); app != "" {
			a := byName[app]
			if a == nil {
				a = &App{Name: app, Region: b.Region}
				byName[app] = a
			}
			if a.Envs == "" {
				a.Envs = env
			} else {
				a.Envs += "," + env
			}
			if a.State == "" || b.State != "OK" {
				a.State = b.State
			}
			continue
		}
		// app-config bucket
		if app := lightsail.ParseAppFromAppBucket(b.Name); app != "" {
			a := byName[app]
			if a == nil {
				a = &App{Name: app, Region: b.Region}
				byName[app] = a
			}
			a.Bucket = b.Name
			if a.State == "" {
				a.State = b.State
			}
		}
	}
	out := make([]any, 0, len(byName))
	for _, a := range byName {
		a.Envs = sortedCSV(a.Envs)
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].(App).Name < out[j].(App).Name })
	return out
}

func (s *store) Get(ctx context.Context, id string) (any, error) {
	items, err := s.List(ctx, registry.Filter{})
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if a := it.(App); a.Name == id {
			return a, nil
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

// Resource builds the triad Resource. Operations layer in as we port each
// phase: Phase 1 adds status+delete; Phase 3 adds deploy; Phase 4 adds
// create; Phase 5 adds logs+local.
//
// The client is built lazily on first Store call so `--help` / `--version`
// and offline TUI launches never touch AWS config.
func Resource(region string) registry.Resource {
	fields := []registry.Field{
		{Name: "Name", Flag: "name", Short: "n", Help: "application name",
			Default: names.Random(), Table: registry.TableHint{Header: "NAME"}},
		{Name: "Envs", Flag: "envs", Help: "environments",
			Table: registry.TableHint{Header: "ENVS"}},
		{Name: "Region", Flag: "region", Help: "AWS region",
			Table: registry.TableHint{Header: "REGION"}},
		{Name: "State", Flag: "state", Help: "bucket state",
			Table: registry.TableHint{Header: "STATE"}},
		{Name: "Bucket", Flag: "bucket", Help: "app config bucket",
			Table: registry.TableHint{Header: "BUCKET", Wide: true}},
	}
	st := &store{region: region}
	suggest := registry.SuggestFrom(st, fields, "name")
	return registry.Resource{
		Name:    "app",
		Plural:  "apps",
		Aliases: []string{"application", "applications"},
		Short:   "manage Lightsail Applications",
		Fields:  fields,
		Store:   st,
		Operations: map[string]registry.Operation{
			"create": createOp(st),
			"status": statusOp(st, suggest),
			"delete": deleteOp(st, suggest),
			"deploy": deployOp(st, suggest),
			"logs":   logsOp(st, suggest),
		},
	}
}
