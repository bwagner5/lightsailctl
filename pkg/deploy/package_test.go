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
	if err := PackageTo(src, []string{".venv"}, &buf); err != nil {
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
