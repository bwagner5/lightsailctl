//go:build integ

// Package integ is the end-to-end integration test for lightsailctl.
//
// It drives the real CLI against a real AWS account. Expensive and
// destructive — gated behind the `integ` build tag so it never runs
// alongside normal `go test ./...`.
//
// Prereqs:
//   - AWS credentials in the usual SDK locations (profile, env, etc.)
//   - An existing Lightsail instance with docker already installed
//     (blueprint: any Ubuntu+Docker AMI; port 22 open to your IP)
//   - A linux/amd64 lightsailctl binary to scp to the instance
//
// Required env vars:
//   LS_INTEG_INSTANCE     — name of the pre-existing Lightsail instance
//   LS_INTEG_REGION       — AWS region the instance lives in
//   LS_INTEG_AGENT_PATH   — local path to a linux/amd64 lightsailctl binary
//
// Optional:
//   LS_INTEG_KEEP         — "1" to skip teardown (leaves buckets+tags behind)
//
// Run:
//   make -C ../../ build   # first: build a linux binary for the agent
//   GOOS=linux GOARCH=amd64 go build -o /tmp/lightsailctl-linux .
//   LS_INTEG_INSTANCE=my-inst LS_INTEG_REGION=us-east-2 \
//     LS_INTEG_AGENT_PATH=/tmp/lightsailctl-linux \
//     go test -tags=integ -v -timeout=20m ./test/integ/...
//
// Target just one phase after a successful first run:
//   go test -tags=integ -v -run TestEndToEnd/Deploy ./test/integ/...
package integ

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	envInstance  = "LS_INTEG_INSTANCE"
	envRegion    = "LS_INTEG_REGION"
	envAgentPath = "LS_INTEG_AGENT_PATH"
	envKeep      = "LS_INTEG_KEEP"
)

// config is loaded once per test run.
type config struct {
	Instance  string
	Region    string
	AgentPath string
	AppName   string
	Env       string
	Binary    string // local lightsailctl built for `go test` host OS
	Keep      bool
}

func load(t *testing.T) *config {
	t.Helper()
	cfg := &config{
		Instance:  os.Getenv(envInstance),
		Region:    os.Getenv(envRegion),
		AgentPath: os.Getenv(envAgentPath),
		Env:       "integ",
		Keep:      os.Getenv(envKeep) == "1",
	}
	missing := []string{}
	if cfg.Instance == "" {
		missing = append(missing, envInstance)
	}
	if cfg.Region == "" {
		missing = append(missing, envRegion)
	}
	if cfg.AgentPath == "" {
		missing = append(missing, envAgentPath)
	}
	if len(missing) > 0 {
		t.Skipf("integ test skipped: set %s", strings.Join(missing, ", "))
	}
	if _, err := os.Stat(cfg.AgentPath); err != nil {
		t.Fatalf("%s: %v", envAgentPath, err)
	}
	cfg.AppName = fmt.Sprintf("integ-%d", time.Now().Unix())
	cfg.Binary = buildCLI(t)
	return cfg
}

// buildCLI builds the lightsailctl binary for the test host and returns its
// path. Built once and reused across subtests via t.Cleanup.
func buildCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "lightsailctl")
	root := repoRoot(t)
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build: %v", err)
	}
	return bin
}

// repoRoot returns the lightsailctl repo root (two up from this file).
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// wd is <repo>/test/integ
	return filepath.Dir(filepath.Dir(wd))
}

// runCLI invokes the lightsailctl binary with -y (non-interactive) so prompts
// never block, and returns combined stdout+stderr.
func runCLI(t *testing.T, cfg *config, workdir string, args ...string) string {
	t.Helper()
	full := append([]string{"-y"}, args...)
	cmd := exec.Command(cfg.Binary, full...)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(), "AWS_REGION="+cfg.Region)
	out, err := cmd.CombinedOutput()
	t.Logf("$ lightsailctl %s\n%s", strings.Join(full, " "), out)
	if err != nil {
		t.Fatalf("lightsailctl %s: %v", strings.Join(full, " "), err)
	}
	return string(out)
}

// TestEndToEnd runs the full create->deploy->status->curl->delete cycle.
// Each phase is its own t.Run so a single phase can be re-executed with
//   go test -tags=integ -run TestEndToEnd/Deploy ./test/integ/...
// after an earlier run left the app in a deployable state.
func TestEndToEnd(t *testing.T) {
	cfg := load(t)

	// Always best-effort teardown at the end of the whole test.
	t.Cleanup(func() {
		if cfg.Keep {
			t.Logf("LS_INTEG_KEEP=1 — skipping teardown. Clean up manually: lightsailctl app delete --name %s", cfg.AppName)
			return
		}
		_ = exec.Command(cfg.Binary, "-y", "app", "delete", "--name", cfg.AppName).Run()
	})

	t.Run("Create", func(t *testing.T) {
		out := runCLI(t, cfg, t.TempDir(),
			"app", "create",
			"--name", cfg.AppName,
			"--env", cfg.Env,
			"--region", cfg.Region,
			"--instance", cfg.Instance,
			"--agent-path", cfg.AgentPath,
		)
		if !strings.Contains(out, "Save lightsail.conf") && !strings.Contains(out, "lightsail.conf") {
			t.Errorf("create output missing save-config step:\n%s", out)
		}
	})

	// workdir for Deploy: contains the compose fixture.
	deployDir := t.TempDir()
	copyDir(t, "testdata/hello", deployDir)

	t.Run("Deploy", func(t *testing.T) {
		runCLI(t, cfg, deployDir,
			"deploy",
			"--name", cfg.AppName,
			"--env", cfg.Env,
			"--region", cfg.Region,
			"--wait-timeout", "5m",
		)
	})

	t.Run("Status", func(t *testing.T) {
		out := runCLI(t, cfg, t.TempDir(),
			"app", "status",
			"--name", cfg.AppName,
			"--env", cfg.Env,
			"--format", "json",
		)
		var rep struct {
			App  string `json:"app"`
			Envs []struct {
				Env      string `json:"env"`
				Statuses []struct {
					Instance   string `json:"instance"`
					Status     string `json:"status"`
					Endpoints  []string `json:"endpoints"`
					Containers []struct {
						Name   string `json:"name"`
						Status string `json:"status"`
					} `json:"containers"`
				} `json:"statuses"`
			} `json:"envs"`
		}
		// Strip any ANSI prefix noise before the first '{'.
		if i := strings.Index(out, "{"); i >= 0 {
			out = out[i:]
		}
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatalf("status json: %v\n%s", err, out)
		}
		if rep.App != cfg.AppName {
			t.Errorf("status.app = %q; want %q", rep.App, cfg.AppName)
		}
		if len(rep.Envs) == 0 || len(rep.Envs[0].Statuses) == 0 {
			t.Fatalf("status has no envs/statuses yet:\n%s", out)
		}
		healthy := false
		for _, s := range rep.Envs[0].Statuses {
			if s.Status == "healthy" && len(s.Containers) > 0 {
				healthy = true
				// Also hit the endpoint if we have one.
				for _, ep := range s.Endpoints {
					hitEndpoint(t, ep)
				}
			}
		}
		if !healthy {
			t.Errorf("no healthy instance in status:\n%s", out)
		}
	})

	t.Run("Delete", func(t *testing.T) {
		if cfg.Keep {
			t.Skip("LS_INTEG_KEEP=1")
		}
		runCLI(t, cfg, t.TempDir(),
			"app", "delete",
			"--name", cfg.AppName,
		)
		// A second delete should succeed no-op-ishly (buckets gone, tags gone).
		out := runCLI(t, cfg, t.TempDir(),
			"app", "delete",
			"--name", cfg.AppName,
		)
		_ = out
	})
}

// hitEndpoint does a best-effort HTTP GET and logs the outcome. Non-fatal on
// failure — the compose app needs time to boot, and Lightsail firewall
// propagation is eventually consistent.
func hitEndpoint(t *testing.T, url string) {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	var lastErr error
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			_ = resp.Body.Close()
			t.Logf("GET %s -> %d; body[:%d]=%q", url, resp.StatusCode, len(body), string(body))
			if resp.StatusCode >= 200 && resp.StatusCode < 400 {
				return
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		select {
		case <-time.After(5 * time.Second):
		case <-context.Background().Done():
			return
		}
	}
	t.Logf("GET %s never succeeded: %v", url, lastErr)
}

// copyDir copies src's contents into dst (shallow — one level of files).
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
