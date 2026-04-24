package app

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aws/lightsailctl/pkg/lightsail"
)

func sampleReport() Report {
	return Report{
		App: "foo",
		Envs: []EnvReport{
			{
				Env: "dev", Bucket: "ls--123--foo--dev",
				Statuses: []lightsail.Status{
					{
						Instance: "ls-inst-1", Status: "healthy", Timestamp: time.Unix(0, 0).UTC(),
						Containers: []lightsail.ContainerStatus{
							{Name: "web", Image: "nginx", Status: "running"},
							{Name: "db", Image: "postgres", Status: "running"},
						},
						Endpoints: []string{"http://1.2.3.4:80"},
					},
				},
			},
			{Env: "prod", Bucket: "ls--123--foo--prod"}, // no watchers yet
		},
	}
}

func TestRenderShort(t *testing.T) {
	var buf bytes.Buffer
	if err := renderStatusTo(&buf, "short", sampleReport()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "dev: healthy (2/2)") {
		t.Errorf("missing dev line: %s", out)
	}
	if !strings.Contains(out, "prod: idle") {
		t.Errorf("missing prod line: %s", out)
	}
}

func TestRenderWide(t *testing.T) {
	var buf bytes.Buffer
	if err := renderStatusTo(&buf, "wide", sampleReport()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"ls-inst-1\tweb\trunning", "ls-inst-1\tdb\trunning", "http://1.2.3.4:80"} {
		if !strings.Contains(out, want) {
			t.Errorf("wide missing %q:\n%s", want, out)
		}
	}
}

func TestRenderJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := renderStatusTo(&buf, "json", sampleReport()); err != nil {
		t.Fatal(err)
	}
	var got Report
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got.App != "foo" || len(got.Envs) != 2 {
		t.Errorf("roundtrip lost data: %+v", got)
	}
}
