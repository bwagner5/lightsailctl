package deploy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/aws/lightsailctl/pkg/build"
)

func TestPackageExcludesBuiltinsAndExtra(t *testing.T) {
	src := t.TempDir()
	// layout:
	//   app.js           -> included
	//   .git/HEAD        -> excluded (builtin)
	//   node_modules/foo -> excluded (builtin)
	//   .venv/bin/py     -> excluded via extraIgnore
	//   src/main.go      -> included
	mustWrite(t, filepath.Join(src, "app.js"), "x")
	mustWrite(t, filepath.Join(src, ".git/HEAD"), "ref")
	mustWrite(t, filepath.Join(src, "node_modules/foo/index.js"), "x")
	mustWrite(t, filepath.Join(src, ".venv/bin/py"), "x")
	mustWrite(t, filepath.Join(src, "src/main.go"), "package main")

	var buf bytes.Buffer
	if err := PackageTo(src, build.StrategyCompose, []string{".venv"}, &buf); err != nil {
		t.Fatal(err)
	}
	got := listTar(t, &buf)
	sort.Strings(got)
	want := []string{"app.js", "src", "src/main.go"}
	sort.Strings(want)
	if !equalStrSlices(got, want) {
		t.Errorf("tar entries = %v; want %v", got, want)
	}
}

// TestPackageStrategyExcludes_Buildpack verifies that build-artifact
// directories are pruned from the upload for buildpack/Dockerfile
// strategies but not for compose. Compose users keep their existing
// (smaller exclude set) behavior.
func TestPackageStrategyExcludes_Buildpack(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "main.go"), "package main")
	mustWrite(t, filepath.Join(src, "go.mod"), "module x")
	mustWrite(t, filepath.Join(src, "target/build.jar"), "x")
	mustWrite(t, filepath.Join(src, "dist/bundle.js"), "x")
	mustWrite(t, filepath.Join(src, "build/out.bin"), "x")
	mustWrite(t, filepath.Join(src, "vendor/lib.go"), "x")

	var bp bytes.Buffer
	if err := PackageTo(src, build.StrategyBuildpack, nil, &bp); err != nil {
		t.Fatal(err)
	}
	got := listTar(t, &bp)
	sort.Strings(got)
	want := []string{"go.mod", "main.go"}
	sort.Strings(want)
	if !equalStrSlices(got, want) {
		t.Errorf("buildpack tar entries = %v; want %v", got, want)
	}

	// Compose path should keep target/dist/build/vendor (user
	// authored the compose file, they choose what's inside).
	var cp bytes.Buffer
	if err := PackageTo(src, build.StrategyCompose, nil, &cp); err != nil {
		t.Fatal(err)
	}
	composeEntries := listTar(t, &cp)
	if !contains(composeEntries, "target/build.jar") {
		t.Errorf("compose strategy unexpectedly pruned target/; entries = %v", composeEntries)
	}
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func listTar(t *testing.T, r io.Reader) []string {
	t.Helper()
	gz, err := gzip.NewReader(r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, hdr.Name)
	}
	return names
}

func equalStrSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
