package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/lightsail"
)

// statusOp: read <instance>_status.json from each env bucket. If --env is
// absent, show every env for the app.
func statusOp(s *store, suggest func(context.Context) ([]registry.Choice, error)) registry.Operation {
	return registry.Operation{
		Name: "status", Short: "show app/env health",
		Fields: []registry.Field{
			{Flag: "name", Short: "n", Help: "app name", Required: true, Suggest: suggest},
			{Flag: "env", Short: "e", Help: "environment (blank = all envs)",
				Suggest: envSuggest(s)},
			{Flag: "format", Short: "f", Help: "output format", Default: "short",
				Suggest: func(_ context.Context) ([]registry.Choice, error) {
					return []registry.Choice{
						{Value: "short", Display: "short  Summary per environment"},
						{Value: "wide", Display: "wide   Container-level detail"},
						{Value: "json", Display: "json   Raw JSON"},
					}, nil
				}},
		},
		Run: func(ctx context.Context, in registry.Input) error {
			c, err := s.ensure(ctx)
			if err != nil {
				return err
			}
			acct, err := c.AccountID(ctx)
			if err != nil {
				return err
			}
			app := in.Get("name")
			// Resolve env->region map from buckets (works globally).
			buckets, err := c.ListAppBuckets(ctx)
			if err != nil {
				return err
			}
			envs := envBucketsForApp(app, buckets, in.Get("env"))
			if len(envs) == 0 {
				return fmt.Errorf("no environments found for app %q", app)
			}
			report := buildReport(ctx, c, acct, app, envs)
			format := in.Get("format")
			if format == "" {
				format = "short"
			}
			return renderStatusTo(os.Stdout, format, report)
		},
	}
}

// envInfo holds an env's bucket name and region.
type envInfo struct{ Env, Bucket, Region string }

// envBucketsForApp filters app's env buckets down to filter (or all when "").
func envBucketsForApp(app string, buckets []lightsail.Bucket, filter string) []envInfo {
	var out []envInfo
	for _, b := range buckets {
		a, e := lightsail.ParseAppEnv(b.Name)
		if a != app || e == "" {
			continue
		}
		if filter != "" && e != filter {
			continue
		}
		out = append(out, envInfo{Env: e, Bucket: b.Name, Region: b.Region})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Env < out[j].Env })
	return out
}

// Report is the status shape. The raw status file is JSON; we give users
// multiple views of it (short / table / json / yaml) via triad's -o.
type Report struct {
	App  string      `json:"app"`
	Envs []EnvReport `json:"envs"`
}

type EnvReport struct {
	Env      string             `json:"env"`
	Bucket   string             `json:"bucket"`
	Statuses []lightsail.Status `json:"statuses"`
	Error    string             `json:"error,omitempty"`
}

func buildReport(ctx context.Context, c *lightsail.Client, acct, app string, envs []envInfo) Report {
	rep := Report{App: app}
	for _, ei := range envs {
		er := EnvReport{Env: ei.Env, Bucket: ei.Bucket}
		rc := c
		if ei.Region != "" {
			rc = c.WithRegion(ei.Region)
		}
		st, err := rc.ReadBucketStatuses(ctx, ei.Bucket)
		if err != nil {
			er.Error = err.Error()
		} else {
			er.Statuses = st
		}
		rep.Envs = append(rep.Envs, er)
	}
	return rep
}

// renderStatusTo is the testable form. Formats: short | wide | json.
func renderStatusTo(w io.Writer, format string, rep Report) error {
	switch strings.ToLower(format) {
	case "json":
		data, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(data))
		return err
	case "wide":
		return renderWide(w, rep)
	default: // short
		return renderShort(w, rep)
	}
}

func renderShort(w io.Writer, rep Report) error {
	fw := &errWriter{w: w}
	for _, er := range rep.Envs {
		if er.Error != "" {
			fw.printf("%s: error: %s\n", er.Env, er.Error)
			continue
		}
		running := 0
		total := 0
		overall := "idle"
		for _, s := range er.Statuses {
			for _, c := range s.Containers {
				total++
				if c.Status == "running" {
					running++
				}
			}
			if s.Status != "" && s.Status != "idle" {
				overall = s.Status
			}
		}
		nInst := len(er.Statuses)
		if total == 0 {
			fw.printf("%s: %s\n", er.Env, overall)
		} else if nInst > 1 {
			fw.printf("%s: %s (%d/%d on %d instances)\n", er.Env, overall, running, total, nInst)
		} else {
			fw.printf("%s: %s (%d/%d)\n", er.Env, overall, running, total)
		}
	}
	return fw.err
}

func renderWide(w io.Writer, rep Report) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fw := &errWriter{w: tw}
	fw.printf("ENV\tINSTANCE\tCONTAINER\tSTATUS\tENDPOINT\tDEPLOYED\n")
	for _, er := range rep.Envs {
		if er.Error != "" {
			fw.printf("%s\t-\t-\terror: %s\t\t\n", er.Env, er.Error)
			continue
		}
		if len(er.Statuses) == 0 {
			fw.printf("%s\t-\t-\tno status yet\t\t\n", er.Env)
			continue
		}
		for _, s := range er.Statuses {
			deployed := ""
			if s.LastDeploy != nil && !s.LastDeploy.Timestamp.IsZero() {
				deployed = s.LastDeploy.Timestamp.Format("2006-01-02 15:04")
			}
			if len(s.Containers) == 0 {
				ep := ""
				if len(s.Endpoints) > 0 {
					ep = strings.Join(s.Endpoints, ", ")
				}
				fw.printf("%s\t%s\t-\t%s\t%s\t%s\n", er.Env, s.Instance, s.Status, ep, deployed)
				continue
			}
			for i, c := range s.Containers {
				ep := ""
				if i < len(s.Endpoints) {
					ep = s.Endpoints[i]
				}
				fw.printf("%s\t%s\t%s\t%s\t%s\t%s\n", er.Env, s.Instance, c.Name, c.Status, ep, deployed)
			}
			for i := len(s.Containers); i < len(s.Endpoints); i++ {
				fw.printf("%s\t%s\t\t\t%s\t\n", er.Env, s.Instance, s.Endpoints[i])
			}
		}
	}
	if fw.err != nil {
		return fw.err
	}
	return tw.Flush()
}

// errWriter captures the first write error so callers don't have to thread
// it through every Fprintf. Matches the pattern in Effective Go.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) printf(format string, a ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintf(e.w, format, a...)
}

// envSuggest returns a Suggest func that lists known environments.
func envSuggest(s *store) func(context.Context) ([]registry.Choice, error) {
	return func(ctx context.Context) ([]registry.Choice, error) {
		c, err := s.ensure(ctx)
		if err != nil {
			return nil, err
		}
		buckets, err := c.ListAppBuckets(ctx)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		var out []registry.Choice
		for _, b := range buckets {
			if _, env := lightsail.ParseAppEnv(b.Name); env != "" && !seen[env] {
				seen[env] = true
				out = append(out, registry.Choice{Value: env})
			}
		}
		return out, nil
	}
}
