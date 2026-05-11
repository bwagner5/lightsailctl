//go:build integ

package integ

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// buildCLI compiles the lightsailctl binary for the test host so the
// integ tests can drive it as a subprocess. Cached per `go test` run
// in a tmpdir; multiple sub-tests reuse the same artifact.
func buildCLI(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "lightsailctl")
	root := repoRoot(t)
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build lightsailctl: %v\n%s", err, b)
	}
	return out
}
