// Package agentfetch resolves an on-instance agent binary path.
//
// The agent shipped to a Lightsail instance is the linux/amd64 build of
// `lightsailctl` itself. When the user passes --agent-path, we use that
// path as-is (historical behavior). When the user does NOT pass
// --agent-path, we auto-fetch the matching release from GitHub once,
// cache it under the user's cache dir keyed by version, and reuse it
// on subsequent deploys.
//
// Kept in its own package so pkg/app doesn't grow another coupled
// network / filesystem surface and the logic is unit-testable in
// isolation.
package agentfetch

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LinuxAMD64AssetURL returns the canonical URL for a
// lightsailctl_linux_amd64.tar.gz tarball at the given version.
//
//   - If version is a real release tag (e.g. "v1.0.7"), we hit the
//     tagged release download URL.
//   - Otherwise (e.g. "v0.0.0-dev" from a `go install ...@latest` build,
//     or any non-release build) we fall back to the "latest" path. The
//     release pipeline already publishes under /latest/download so the
//     URL is always valid.
func LinuxAMD64AssetURL(version string) string {
	v := strings.TrimSpace(version)
	// Empty / dev / snapshot builds → use the "latest" path.
	if v == "" || !isReleaseVersion(v) {
		return "https://github.com/aws/lightsailctl/releases/latest/download/lightsailctl_linux_amd64.tar.gz"
	}
	return fmt.Sprintf(
		"https://github.com/aws/lightsailctl/releases/download/%s/lightsailctl_linux_amd64.tar.gz", v)
}

// isReleaseVersion reports whether v looks like a real released tag
// (vMAJOR.MINOR.PATCH, no pre-release component). The project's
// snapshot versions look like "v1.0.8-next" which we treat as dev.
func isReleaseVersion(v string) bool {
	if !strings.HasPrefix(v, "v") {
		return false
	}
	body := strings.TrimPrefix(v, "v")
	if strings.ContainsAny(body, "-+") {
		return false
	}
	parts := strings.Split(body, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// CachePath returns the cache location the resolver uses for a given
// version. Exposed so tests and higher-level code can reason about it.
func CachePath(baseDir, version string) string {
	v := strings.TrimSpace(version)
	if v == "" {
		v = "latest"
	}
	return filepath.Join(baseDir, "lightsailctl", v, "lightsailctl_linux_amd64")
}

// Resolve returns a path to a linux/amd64 lightsailctl binary suitable
// for scp-ing to an instance. Behavior:
//
//   - If explicit is non-empty, return it as-is after a stat+size check.
//     The file is expected to be the raw ELF binary — we don't try to
//     inspect or convert.
//   - Otherwise, use the cache under cacheBase keyed by version. If the
//     cache has the file already, return its path. If not, download the
//     corresponding tarball from GitHub, extract the `lightsailctl`
//     entry into the cache path, and return it.
//
// The returned path is always absolute and guaranteed to exist.
func Resolve(ctx context.Context, explicit, version, cacheBase string) (string, error) {
	if explicit != "" {
		fi, err := os.Stat(explicit)
		if err != nil {
			return "", fmt.Errorf("agent binary not found at %s: %w", explicit, err)
		}
		if fi.IsDir() || fi.Size() == 0 {
			return "", fmt.Errorf("agent binary at %s is not a regular file", explicit)
		}
		abs, _ := filepath.Abs(explicit)
		return abs, nil
	}

	target := CachePath(cacheBase, version)
	if fi, err := os.Stat(target); err == nil && !fi.IsDir() && fi.Size() > 0 {
		return target, nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}

	url := LinuxAMD64AssetURL(version)
	if err := downloadAndExtract(ctx, url, target); err != nil {
		return "", fmt.Errorf("fetch agent binary from %s: %w", url, err)
	}
	return target, nil
}

// downloadAndExtract pulls the linux/amd64 tarball from url and
// extracts the `lightsailctl` entry into destFile.
func downloadAndExtract(ctx context.Context, url, destFile string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	// Modest timeout so a hung GitHub connection doesn't stall a
	// deploy forever. The tarball is ~8 MB; 5 minutes is plenty.
	httpClient := &http.Client{Timeout: 5 * time.Minute}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("http %s", resp.Status)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("no lightsailctl entry found in tarball")
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		// The tarball has a few files (LICENSE, lightsailctl, README);
		// we want the binary. Protect against path-escape.
		name := filepath.Base(hdr.Name)
		if name != "lightsailctl" {
			continue
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA { //nolint:staticcheck // TypeRegA is deprecated but still produced.
			return fmt.Errorf("unexpected tar entry type %d for lightsailctl", hdr.Typeflag)
		}
		out, err := os.OpenFile(destFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		_, cerr := io.Copy(out, tr)
		closeErr := out.Close()
		if cerr != nil {
			return cerr
		}
		if closeErr != nil {
			return closeErr
		}
		return nil
	}
}

// UserCacheDir returns a sensible default cache root:
// os.UserCacheDir() on systems that support it; falls back to a
// ~/.cache-like path if the UserCacheDir call fails. Exposed so
// callers don't have to duplicate the fallback logic.
func UserCacheDir() string {
	if d, err := os.UserCacheDir(); err == nil {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "lightsailctl-cache")
	}
	return filepath.Join(home, ".cache")
}
