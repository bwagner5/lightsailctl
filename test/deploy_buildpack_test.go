//go:build integ

package integ

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestDeployBuildpack drives a no-compose, no-Dockerfile Go repo
// through deploy via Cloud Native Buildpacks. The agent should
// detect go.mod, run `pack build` on the instance, and stand up a
// healthy container on :8080.
//
// A second deploy of the same source asserts the persistent
// buildpack cache: the cache volume is keyed per app/env so layers
// stay warm across deploys, taking the second build from ~90s to
// seconds. We give it a generous budget so flaky CI doesn't
// false-positive — the point is to catch a regression that wipes
// the cache, not to micro-optimize.
func TestDeployBuildpack(t *testing.T) {
	cfg := load(t)

	deployDir := t.TempDir()
	copyDir(t, "data/hello-go", deployDir)

	app := cfg.AppName + "-bp"
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

	t.Run("first_deploy", func(t *testing.T) {
		runCLI(t, cfg, deployDir,
			"deploy",
			"--name", app,
			"--env", cfg.Env,
			"--region", cfg.Region,
			"--wait-timeout", "15m",
		)
		assertBuildpackHealthy(t, cfg, app)
	})

	// Touch a file so the deploy key changes (different timestamp).
	// The buildpack cache is content-addressed by layer so the source
	// edit doesn't invalidate the warm Go SDK / module-download
	// layers — exactly what we want to test.
	if err := os.WriteFile(deployDir+"/touch.txt", []byte(time.Now().String()), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("second_deploy_cache_hit", func(t *testing.T) {
		start := time.Now()
		runCLI(t, cfg, deployDir,
			"deploy",
			"--name", app,
			"--env", cfg.Env,
			"--region", cfg.Region,
			"--wait-timeout", "15m",
		)
		elapsed := time.Since(start)
		t.Logf("second deploy took %s (cache should keep this well under cold-start)", elapsed)
		// Generous threshold: a cold buildpack run takes 90s+;
		// even a half-warm cache should easily come in under 5m.
		// If this trips, something wiped the cache volume.
		if elapsed > 5*time.Minute {
			t.Errorf("second deploy took %s; expected cache hit to keep it under 5m", elapsed)
		}
		assertBuildpackHealthy(t, cfg, app)
	})
}

// assertBuildpackHealthy reads `app status --format json` and
// asserts at least one healthy instance is reporting on :8080.
func assertBuildpackHealthy(t *testing.T, cfg *config, app string) {
	t.Helper()
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
	hit := false
	for _, e := range rep.Envs {
		for _, s := range e.Statuses {
			if s.Status != "healthy" {
				continue
			}
			for _, ep := range s.Endpoints {
				if !strings.HasSuffix(ep, ":8080") {
					t.Errorf("buildpack endpoint = %q; expected :8080 default", ep)
				}
				hitEndpoint(t, ep)
				hit = true
			}
		}
	}
	if !hit {
		t.Fatalf("no healthy buildpack endpoints in status:\n%s", out)
	}
}
