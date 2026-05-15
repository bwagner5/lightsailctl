// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package agentfetch

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsReleaseVersion(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		{"v1.0.7", true},
		{"v1.0.0", true},
		{"v10.20.30", true},
		{"1.0.7", false},                 // no leading v
		{"v1.0.7-rc1", false},            // prerelease
		{"v1.0.8-next", false},           // snapshot
		{"v0.0.0-20230101120000", false}, // pseudo-version
		{"", false},
		{"vv1.0.7", false},
		{"v1.0", false},
		{"v1.0.x", false},
	}
	for _, tc := range cases {
		if got := isReleaseVersion(tc.v); got != tc.want {
			t.Errorf("isReleaseVersion(%q) = %v; want %v", tc.v, got, tc.want)
		}
	}
}

func TestLinuxAMD64AssetURL(t *testing.T) {
	if got := LinuxAMD64AssetURL("v1.0.7"); got != "https://github.com/aws/lightsailctl/releases/download/v1.0.7/lightsailctl_linux_amd64.tar.gz" {
		t.Errorf("v1.0.7 url = %q", got)
	}
	// Non-release → latest.
	if got := LinuxAMD64AssetURL("v1.0.8-next"); !strings.Contains(got, "/releases/latest/download/") {
		t.Errorf("dev url = %q", got)
	}
	if got := LinuxAMD64AssetURL(""); !strings.Contains(got, "/releases/latest/download/") {
		t.Errorf("empty url = %q", got)
	}
}

func TestResolve_ExplicitPath(t *testing.T) {
	// Explicit path short-circuits even when version is empty.
	dir := t.TempDir()
	bin := filepath.Join(dir, "lightsailctl")
	if err := os.WriteFile(bin, []byte("ELF..."), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(context.Background(), bin, "", t.TempDir())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	abs, _ := filepath.Abs(bin)
	if got != abs {
		t.Errorf("got %q; want %q", got, abs)
	}
}

func TestResolve_ExplicitPathMissing(t *testing.T) {
	_, err := Resolve(context.Background(), "/nope/does/not/exist", "", t.TempDir())
	if err == nil {
		t.Fatalf("want error")
	}
}

func TestResolve_DownloadsAndCaches(t *testing.T) {
	// Build a tiny tarball server.
	tarball := buildTestTarball(t, "lightsailctl", []byte("fake-binary-bytes"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(tarball)
	}))
	defer srv.Close()

	cacheBase := t.TempDir()
	target := CachePath(cacheBase, "v1.0.7")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	// Exercise the download-and-extract path directly, bypassing the
	// GitHub URL builder (we don't want to hit the real GitHub).
	if err := downloadAndExtract(context.Background(), srv.URL, target); err != nil {
		t.Fatalf("downloadAndExtract: %v", err)
	}
	b, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read cached: %v", err)
	}
	if string(b) != "fake-binary-bytes" {
		t.Errorf("cache contents = %q", string(b))
	}
	fi, _ := os.Stat(target)
	if fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("cached file not executable: %v", fi.Mode())
	}
}

func TestResolve_CacheHitSkipsNetwork(t *testing.T) {
	cacheBase := t.TempDir()
	target := CachePath(cacheBase, "v1.0.7")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("cached"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A cache hit should return the path without any HTTP attempt; we
	// don't set up a server, so if Resolve touched the network it would
	// fail trying to reach github.com. Set a canceled context to harden:
	// if the code DID try to fetch, it'd error out on ctx.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := Resolve(ctx, "", "v1.0.7", cacheBase)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != target {
		t.Errorf("got %q; want %q", got, target)
	}
}

func TestDownloadAndExtract_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()
	err := downloadAndExtract(context.Background(), srv.URL, filepath.Join(t.TempDir(), "x"))
	if err == nil {
		t.Fatalf("want error")
	}
}

func TestDownloadAndExtract_NoMatchingEntry(t *testing.T) {
	tarball := buildTestTarball(t, "not-lightsailctl", []byte("bogus"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(tarball)
	}))
	defer srv.Close()
	err := downloadAndExtract(context.Background(), srv.URL, filepath.Join(t.TempDir(), "x"))
	if err == nil {
		t.Fatalf("want error")
	}
}

// buildTestTarball returns a gzipped tarball containing one regular
// file named `name` with the given contents.
func buildTestTarball(t *testing.T, name string, contents []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name:     name,
		Mode:     0o755,
		Size:     int64(len(contents)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(tw, bytes.NewReader(contents)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
