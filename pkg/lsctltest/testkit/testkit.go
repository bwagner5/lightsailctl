// Package testkit provides a test registration and execution framework for
// lightsailctl integration tests.
//
// Tests are plain Go functions that read top-to-bottom like a user session:
//
//	func instanceLifecycle(t *testkit.T) {
//	    name := t.NewName("inst")
//	    t.CreateInstance(name)
//	    t.WaitForInstanceRunning(name)
//	    t.DeleteInstance(name)
//	}
//
// High-level helpers print a live banner before they start and a result
// line when they finish, so the run never looks hung.
package testkit

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
	"time"
)

// -----------------------------------------------------------------------------
// Registration
// -----------------------------------------------------------------------------

// TestFunc is the signature every registered test implements.
type TestFunc func(t *T)

// RegisteredTest pairs a name with its test function.
type RegisteredTest struct {
	Name string
	Run  TestFunc
}

var registry []RegisteredTest

// Register adds a test to the global registry. Tests read top-to-bottom
// as a user session and are responsible for creating and cleaning up any
// resources they need (honoring env.Keep for cleanup).
func Register(name string, run TestFunc) {
	registry = append(registry, RegisteredTest{Name: name, Run: run})
}

// All returns all registered tests.
func All() []RegisteredTest { return registry }

// -----------------------------------------------------------------------------
// Env
// -----------------------------------------------------------------------------

// Env is the CLI-level configuration injected into every test.
type Env struct {
	Binary      string // path to the lightsailctl binary under test
	BinaryAgent string // linux/amd64 binary scp'd to the instance
	Region      string
	Bundle      string // default instance size
	UserData    string // cloud-init script for new instances
	Keep        bool   // leave resources in place on exit
	DryRun      bool   // print commands without executing
	Verbose     bool   // also stream polling / silent CLI calls
}

// -----------------------------------------------------------------------------
// Step and Result
// -----------------------------------------------------------------------------

// Step is a single executed action within a test.
type Step struct {
	Name    string        // human label, e.g. "create instance"
	Cmd     string        // full shell command (or empty for non-CLI steps)
	Output  string        // combined stdout+stderr
	Err     error         // non-nil on failure
	Elapsed time.Duration
}

// Result is the outcome of a single test.
type Result struct {
	Name    string
	Steps   []Step
	Passed  bool
	FailMsg string
	Elapsed time.Duration
}

// TestFailure is the panic value used by Fatalf.
type TestFailure struct{ Msg string }

// -----------------------------------------------------------------------------
// Reporter
// -----------------------------------------------------------------------------

// Reporter receives live test progress. Implementations typically print
// to stdout/stderr.
type Reporter interface {
	// Plan is called once before any tests run, with the full plan.
	Plan(env Env, tests []RegisteredTest)
	// TestStart is called when a test starts.
	TestStart(name string)
	// StepStart is called when a step starts (before any work).
	StepStart(name, cmd string)
	// StepDone is called when a step finishes, passing the full Step.
	StepDone(s Step)
	// TestDone is called when a test finishes.
	TestDone(r Result)
	// Summary is called once, after all tests have finished.
	Summary(results []Result)
	// Debug is called for low-level CLI invocations that are not tracked
	// as steps (e.g. polling probes). Reporters typically suppress these
	// unless verbose mode is on.
	Debug(name, cmd, output string, err error)
}

// -----------------------------------------------------------------------------
// T — the test context
// -----------------------------------------------------------------------------

// T is the test context. It records every step and streams live progress
// to the Reporter.
type T struct {
	ctx     context.Context
	env     Env
	rep     Reporter
	steps   []Step
	failed  bool
	failMsg string
}

// NewT creates a test context.
func NewT(ctx context.Context, env Env, rep Reporter) *T {
	return &T{ctx: ctx, env: env, rep: rep}
}

func (t *T) Env() Env        { return t.env }
func (t *T) Steps() []Step   { return t.steps }
func (t *T) Failed() bool    { return t.failed }
func (t *T) FailMsg() string { return t.failMsg }

// Fatalf marks the test failed and unwinds via panic(TestFailure).
func (t *T) Fatalf(format string, args ...any) {
	t.failed = true
	t.failMsg = fmt.Sprintf(format, args...)
	panic(TestFailure{Msg: t.failMsg})
}

// NewName generates a unique resource name with a short prefix.
func (t *T) NewName(prefix string) string {
	return fmt.Sprintf("lsctltest-%s-%d", prefix, time.Now().Unix())
}

// -----------------------------------------------------------------------------
// Low-level execution
// -----------------------------------------------------------------------------

// beginStep announces a step via the Reporter and returns a closer the
// caller invokes when the work is done.
func (t *T) beginStep(name, cmd string) func(output string, err error) {
	if t.rep != nil {
		t.rep.StepStart(name, cmd)
	}
	start := time.Now()
	return func(output string, err error) {
		s := Step{
			Name:    name,
			Cmd:     cmd,
			Output:  output,
			Err:     err,
			Elapsed: time.Since(start),
		}
		t.steps = append(t.steps, s)
		if t.rep != nil {
			t.rep.StepDone(s)
		}
	}
}

// RunCLI executes the lightsailctl binary with the given args. `-y` and
// `--region <region>` are prepended automatically. The step is streamed
// live: StepStart before the exec, StepDone after.
func (t *T) RunCLI(name string, args ...string) string {
	return t.runCLI(name, "", args...)
}

// RunCLIInDir is like RunCLI but sets the working directory.
func (t *T) RunCLIInDir(name, dir string, args ...string) string {
	return t.runCLI(name, dir, args...)
}

func (t *T) runCLI(name, dir string, args ...string) string {
	full := append([]string{"-y", "--region", t.env.Region}, args...)
	cmdStr := fmt.Sprintf("%s %s", filepath.Base(t.env.Binary), strings.Join(full, " "))
	if dir != "" {
		cmdStr = fmt.Sprintf("cd %s && %s", dir, cmdStr)
	}
	done := t.beginStep(name, cmdStr)

	if t.env.DryRun {
		done("(dry-run)", nil)
		return ""
	}

	// If we change working dir, the binary must be an absolute path so it
	// still resolves from the new cwd.
	bin := t.env.Binary
	if dir != "" {
		if abs, err := filepath.Abs(bin); err == nil {
			bin = abs
		}
	}
	cmd := exec.CommandContext(t.ctx, bin, full...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "AWS_REGION="+t.env.Region)
	out, err := cmd.CombinedOutput()
	done(string(out), err)
	if err != nil {
		t.Fatalf("%s failed: %v\n%s", name, err, out)
	}
	return string(out)
}

// runCLISilent executes the CLI without recording a step — used by polling
// helpers that would otherwise spam the step log. When env.Verbose is on
// the call is emitted via Reporter.Debug so the user can still see it.
func (t *T) runCLISilent(name string, args ...string) (string, error) {
	full := append([]string{"-y", "--region", t.env.Region}, args...)
	cmdStr := fmt.Sprintf("%s %s", filepath.Base(t.env.Binary), strings.Join(full, " "))
	cmd := exec.CommandContext(t.ctx, t.env.Binary, full...)
	cmd.Env = append(os.Environ(), "AWS_REGION="+t.env.Region)
	out, err := cmd.CombinedOutput()
	if t.env.Verbose && t.rep != nil {
		t.rep.Debug(name, cmdStr, string(out), err)
	}
	return string(out), err
}

// -----------------------------------------------------------------------------
// High-level helpers: Instance
// -----------------------------------------------------------------------------

// CreateInstance creates a Lightsail instance with the runner's defaults.
func (t *T) CreateInstance(name string) {
	args := []string{
		"instance", "create",
		"--name", name,
		"--region", t.env.Region,
		"--blueprint", "amazon_linux_2023",
		"--bundle", t.env.Bundle,
		"--ip-address-type", "dualstack",
	}
	if t.env.UserData != "" {
		args = append(args, "--user-data", t.env.UserData)
	}
	t.RunCLI(fmt.Sprintf("create instance %s", name), args...)
}

// WaitForInstanceRunning polls until the instance reports state=running.
func (t *T) WaitForInstanceRunning(name string) {
	name = strings.TrimSpace(name)
	label := fmt.Sprintf("wait for instance %s running", name)
	done := t.beginStep(label, fmt.Sprintf("poll: instance get %s --format json", name))

	if t.env.DryRun {
		done("(dry-run)", nil)
		return
	}

	err := poll(t.ctx, 2*time.Minute, 10*time.Second, func() (bool, error) {
		out, cerr := t.runCLISilent("poll instance state", "instance", "get", name, "-o", "json")
		if cerr != nil {
			return false, nil // retry
		}
		state, ok := instanceState(out)
		return ok && state == "running", nil
	})
	if err != nil {
		done("", err)
		t.Fatalf("%s: %v", label, err)
	}
	done("state=running", nil)
}

// DeleteInstance deletes a Lightsail instance. Does not fail the test if
// the instance is already gone (best-effort cleanup).
func (t *T) DeleteInstance(name string) {
	label := fmt.Sprintf("delete instance %s", name)
	args := []string{"instance", "delete", "--name", name}
	cmdStr := fmt.Sprintf("%s -y --region %s %s",
		filepath.Base(t.env.Binary), t.env.Region, strings.Join(args, " "))
	done := t.beginStep(label, cmdStr)

	if t.env.DryRun {
		done("(dry-run)", nil)
		return
	}

	out, err := t.runCLISilent("delete instance", args...)
	done(out, err)
	// Deletion failures are reported but not fatal, so teardown can proceed.
}

// AssertInstanceGone polls until `instance get` reports the instance no
// longer exists.
func (t *T) AssertInstanceGone(name string) {
	label := fmt.Sprintf("assert instance %s gone", name)
	done := t.beginStep(label, fmt.Sprintf("poll: instance get %s (expect not found)", name))

	if t.env.DryRun {
		done("(dry-run)", nil)
		return
	}

	err := poll(t.ctx, 2*time.Minute, 10*time.Second, func() (bool, error) {
		out, cerr := t.runCLISilent("poll instance gone", "instance", "get", name, "-o", "json")
		if cerr != nil {
			// exec error usually means "not found"
			return true, nil
		}
		return instanceGone(out), nil
	})
	if err != nil {
		done("", err)
		t.Fatalf("%s: %v", label, err)
	}
	done("instance deleted", nil)
}

// -----------------------------------------------------------------------------
// High-level helpers: App / Deploy
// -----------------------------------------------------------------------------

// DeployApp runs `deploy` in the app directory. When the app's env
// bucket doesn't yet exist the deploy command auto-creates it (and
// tags the instance, installs the agent, etc.), so a fresh repo's
// first deploy bootstraps everything in one shot. `instance` is the
// target Lightsail instance; the CLI infers region from it.
func (t *T) DeployApp(appName, appEnv, instance, appDir string) {
	agentPath := t.env.BinaryAgent
	if abs, err := filepath.Abs(agentPath); err == nil {
		agentPath = abs
	}
	t.RunCLIInDir(fmt.Sprintf("deploy app %s", appName), appDir,
		"deploy",
		"--name", appName,
		"--env", appEnv,
		"--instance", instance,
		"--agent-path", agentPath,
		"--region", t.env.Region,
		"--wait-timeout", "2m",
	)
}

// DeleteApp runs `app delete`. Best-effort: failures are recorded but
// don't fail the test.
func (t *T) DeleteApp(appName string) {
	label := fmt.Sprintf("delete app %s", appName)
	args := []string{"app", "delete", "--name", appName}
	cmdStr := fmt.Sprintf("%s -y --region %s %s",
		filepath.Base(t.env.Binary), t.env.Region, strings.Join(args, " "))
	done := t.beginStep(label, cmdStr)

	if t.env.DryRun {
		done("(dry-run)", nil)
		return
	}
	out, err := t.runCLISilent("delete app", args...)
	done(out, err)
}

// AppEndpoints returns all endpoints from `app status --format json`.
// In dry-run, returns nil and records no step work.
func (t *T) AppEndpoints(appName, appEnv string) []string {
	if t.env.DryRun {
		// RunCLI already records a dry-run step; we just can't produce endpoints.
		_ = t.RunCLI(fmt.Sprintf("status %s", appName),
			"app", "status",
			"--name", appName,
			"--env", appEnv,
			"--format", "json",
		)
		return nil
	}
	out := t.RunCLI(fmt.Sprintf("status %s", appName),
		"app", "status",
		"--name", appName,
		"--env", appEnv,
		"--format", "json",
	)
	// Strip any prefix noise before the first '{'.
	if i := strings.Index(out, "{"); i >= 0 {
		out = out[i:]
	}
	return extractEndpoints(out)
}

// AssertEndpoints2xx fetches each endpoint and asserts HTTP 2xx. Retries
// for up to 2 minutes per endpoint. In dry-run this is a no-op.
func (t *T) AssertEndpoints2xx(endpoints []string) {
	if t.env.DryRun {
		done := t.beginStep("assert endpoints 2xx", "curl each endpoint, expect 2xx")
		done("(dry-run)", nil)
		return
	}
	if len(endpoints) == 0 {
		t.Fatalf("no endpoints to check")
	}
	for _, ep := range endpoints {
		t.assertHTTP(ep, func(code int) bool { return code >= 200 && code < 300 }, "2xx")
	}
}

func (t *T) assertHTTP(url string, check func(int) bool, label string) {
	done := t.beginStep(fmt.Sprintf("GET %s", url),
		fmt.Sprintf("curl -s -o /dev/null -w '%%{http_code}' %s", url))

	if t.env.DryRun {
		done("(dry-run)", nil)
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	deadline := time.Now().Add(2 * time.Minute)
	var lastErr error
	var lastCode int
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			t.sleep(5 * time.Second)
			continue
		}
		_, _ = io.ReadAll(io.LimitReader(resp.Body, 2048))
		_ = resp.Body.Close()
		lastCode = resp.StatusCode
		if check(resp.StatusCode) {
			done(fmt.Sprintf("%d", resp.StatusCode), nil)
			return
		}
		lastErr = fmt.Errorf("status %d", resp.StatusCode)
		t.sleep(5 * time.Second)
	}
	done(fmt.Sprintf("%d", lastCode), lastErr)
	t.Fatalf("GET %s: expected %s, last: %v", url, label, lastErr)
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func (t *T) sleep(d time.Duration) {
	select {
	case <-time.After(d):
	case <-t.ctx.Done():
	}
}

func poll(ctx context.Context, timeout, interval time.Duration, fn func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		done, err := fn()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		select {
		case <-time.After(interval):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fmt.Errorf("timed out after %s", timeout)
}

// extractEndpoints parses `app status --format json` output and returns
// all endpoint URLs.
func extractEndpoints(raw string) []string {
	var rep struct {
		Envs []struct {
			Statuses []struct {
				Endpoints []string `json:"endpoints"`
			} `json:"statuses"`
		} `json:"envs"`
	}
	if err := json.Unmarshal([]byte(raw), &rep); err != nil {
		return nil
	}
	var eps []string
	for _, env := range rep.Envs {
		for _, s := range env.Statuses {
			eps = append(eps, s.Endpoints...)
		}
	}
	return eps
}

// instanceState parses `instance get -o json` output and returns the
// instance state in lowercase (e.g. "running", "pending"). The second
// return value is false if the JSON could not be parsed or no "state"
// field was found. The output may be either a single object or a
// one-element array (the CLI uses the latter). Field-name and value
// comparisons are case-insensitive so "State" / "state" and
// "Running" / "running" are all accepted.
func instanceState(raw string) (string, bool) {
	// Accept both `{...}` and `[{...}]`.
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "[") {
		var arr []map[string]any
		if err := json.Unmarshal([]byte(raw), &arr); err != nil || len(arr) == 0 {
			return "", false
		}
		return stateFromMap(arr[0])
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return "", false
	}
	return stateFromMap(obj)
}

func stateFromMap(obj map[string]any) (string, bool) {
	for k, v := range obj {
		if !strings.EqualFold(k, "state") {
			continue
		}
		s, ok := v.(string)
		if !ok {
			return "", false
		}
		return strings.ToLower(strings.TrimSpace(s)), true
	}
	return "", false
}

// instanceGone reports whether `instance get -o json` output indicates
// the instance no longer exists. The CLI may return an empty array,
// `null`, `{}`, or an empty string depending on the path taken.
func instanceGone(raw string) bool {
	raw = strings.TrimSpace(raw)
	switch raw {
	case "", "null", "{}", "[]":
		return true
	}
	if strings.HasPrefix(raw, "[") {
		var arr []any
		if err := json.Unmarshal([]byte(raw), &arr); err == nil && len(arr) == 0 {
			return true
		}
	}
	return false
}

// Logf writes a free-form line to the Reporter (no step recorded).
// Useful for narrating intent between helpers.
func (t *T) Logf(format string, args ...any) {
	// Emit as a zero-duration "note" step so it shows up in the stream.
	msg := fmt.Sprintf(format, args...)
	if t.rep != nil {
		t.rep.StepStart("note", msg)
		t.rep.StepDone(Step{Name: "note", Cmd: msg})
	}
}
