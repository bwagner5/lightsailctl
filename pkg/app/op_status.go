package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/lightsail"
)

// statusOp: read <instance>_status.json from each env bucket. If --env is
// absent, show every env for the app.
func statusOp(s *store, suggest func(context.Context) ([]registry.Choice, error)) registry.Operation {
	return registry.Operation{
		Name: "status", Key: "s", Short: "show app/env health",
		Fields: []registry.Field{
			{Flag: "name", Short: "n", Help: "app name", Required: true, Suggest: suggest},
			{Flag: "env", Short: "e", Help: "environment (blank = all envs)"},
			{Flag: "format", Short: "f", Help: "short | wide | json", Default: "short"},
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
		if total == 0 {
			fw.printf("%s: %s\n", er.Env, overall)
		} else {
			fw.printf("%s: %s (%d/%d)\n", er.Env, overall, running, total)
		}
	}
	return fw.err
}

func renderWide(w io.Writer, rep Report) error {
	fw := &errWriter{w: w}
	fw.printf("ENV\tINSTANCE\tCONTAINER\tSTATUS\n")
	for _, er := range rep.Envs {
		if er.Error != "" {
			fw.printf("%s\t-\t-\terror: %s\n", er.Env, er.Error)
			continue
		}
		if len(er.Statuses) == 0 {
			fw.printf("%s\t-\t-\tno status yet\n", er.Env)
			continue
		}
		for _, s := range er.Statuses {
			if len(s.Containers) == 0 {
				fw.printf("%s\t%s\t-\t%s\n", er.Env, s.Instance, s.Status)
				continue
			}
			for _, c := range s.Containers {
				fw.printf("%s\t%s\t%s\t%s\n", er.Env, s.Instance, c.Name, c.Status)
			}
			for _, ep := range s.Endpoints {
				fw.printf("%s\t%s\t(endpoint)\t%s\n", er.Env, s.Instance, ep)
			}
		}
	}
	return fw.err
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
