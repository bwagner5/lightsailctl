//go:build integ

package integ

import (
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// TestDeployDockerfile drives a no-compose, Dockerfile-only repo
// through deploy and asserts the agent built it, synthesized a
// compose file, and the resulting container responds 2xx.
//
// Two fixture variants:
//   - data/hello-dockerfile: EXPOSE 8080, hits port 8080.
//   - data/hello-dockerfile-port3000: EXPOSE 3000, hits port 3000.
//     Regression test for the auto-detect-port path.
func TestDeployDockerfile(t *testing.T) {
	cfg := load(t)
	for _, fx := range []struct {
		name     string
		fixture  string
		wantPort int
	}{
		{"port_8080", "data/hello-dockerfile", 8080},
		{"port_3000_via_expose", "data/hello-dockerfile-port3000", 3000},
	} {
		t.Run(fx.name, func(t *testing.T) {
			deployDockerfileFixture(t, cfg, fx.fixture, fx.wantPort)
		})
	}
}

func deployDockerfileFixture(t *testing.T, cfg *config, fixture string, wantPort int) {
	t.Helper()
	deployDir := t.TempDir()
	copyDir(t, fixture, deployDir)

	app := cfg.AppName + "-df-" + strconv.Itoa(wantPort)

	t.Cleanup(func() {
		if cfg.Keep {
			return
		}
		cmd := exec.Command(cfg.Binary, "-y", "app", "delete", "--name", app)
		cmd.Env = append(os.Environ(), "AWS_REGION="+cfg.Region)
		_ = cmd.Run()
	})

	runCLI(t, cfg, deployDir,
		"app", "create",
		"--name", app,
		"--env", cfg.Env,
		"--region", cfg.Region,
		"--instance", cfg.Instance,
		"--agent-path", cfg.AgentPath,
	)

	runCLI(t, cfg, deployDir,
		"deploy",
		"--name", app,
		"--env", cfg.Env,
		"--region", cfg.Region,
		"--wait-timeout", "10m",
	)

	out := runCLI(t, cfg, t.TempDir(),
		"app", "status",
		"--name", app,
		"--env", cfg.Env,
		"--format", "json",
	)
	if i := strings.Index(out, "{"); i >= 0 {
		out = out[i:]
	}
	var rep struct {
		Envs []struct {
			Statuses []struct {
				Status    string   `json:"status"`
				Endpoints []string `json:"endpoints"`
			} `json:"statuses"`
		} `json:"envs"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("status json: %v\n%s", err, out)
	}

	wantSuffix := ":" + strconv.Itoa(wantPort)
	hit := false
	for _, e := range rep.Envs {
		for _, s := range e.Statuses {
			for _, ep := range s.Endpoints {
				if !strings.HasSuffix(ep, wantSuffix) {
					t.Errorf("endpoint = %q; expected suffix %q (want port %d)", ep, wantSuffix, wantPort)
				}
				hitEndpoint(t, ep)
				hit = true
			}
		}
	}
	if !hit {
		t.Fatalf("no endpoints in status report:\n%s", out)
	}
}
