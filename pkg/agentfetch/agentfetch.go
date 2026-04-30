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
//  1. If explicit is non-empty, stat+size check it and return as-is.
//  2. Else check the per-version cache under cacheBase.
//  3. Else look for a locally-built binary in the well-known dev
//     locations (./dist/lightsailctl_linux_amd64_v1/lightsailctl and
//     ./lightsailctl_linux_amd64). Lets contributors run `make
//     snapshot` and deploy without hitting GitHub.
//  4. Else download from GitHub and cache.
//
// The returned path is always absolute and guaranteed to exist.
func Resolve(ctx context.Context, explicit, version, cacheBase string) (string, error) {
	if explicit != "" {
		return statAgentBinary(explicit)
	}

	target := CachePath(cacheBase, version)
	if fi, err := os.Stat(target); err == nil && !fi.IsDir() && fi.Size() > 0 {
		return target, nil
	}

	// Look for a locally-built linux/amd64 binary before hitting
	// the network. Each well-known path is checked once; the first
	// hit wins. Dev workflows (`make snapshot`, manual
	// `GOOS=linux GOARCH=amd64 go build`) tend to land here.
	for _, p := range localAgentCandidates() {
		if p, err := statAgentBinary(p); err == nil {
			return p, nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}

	url := LinuxAMD64AssetURL(version)
	if err := downloadAndExtract(ctx, url, target); err != nil {
		return "", fmt.Errorf("could not obtain a linux/amd64 lightsailctl binary:\n"+
			"  download: %w\n\n"+
			"To proceed:\n"+
			"  • pass --agent-path /path/to/linux/amd64/lightsailctl, OR\n"+
			"  • build one locally:\n"+
			"      GOOS=linux GOARCH=amd64 go build -o ./lightsailctl_linux_amd64 .", err)
	}
	return target, nil
}

// statAgentBinary validates path points at a regular, non-empty file
// and returns its absolute path. Shared between the explicit-path and
// local-candidate branches so the "not a regular file" / "empty file"
// rejection is identical.
func statAgentBinary(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("agent binary not found at %s: %w", path, err)
	}
	if fi.IsDir() || fi.Size() == 0 {
		return "", fmt.Errorf("agent binary at %s is not a regular file", path)
	}
	abs, _ := filepath.Abs(path)
	return abs, nil
}

// localAgentCandidates returns candidate paths for a locally-built
// linux/amd64 lightsailctl binary, ordered from most-specific to
// least-specific. All are evaluated relative to the current working
// directory. Missing paths are silently ignored by the caller.
func localAgentCandidates() []string {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	return []string{
		// goreleaser's default snapshot/release layout.
		filepath.Join(cwd, "dist", "lightsailctl_linux_amd64_v1", "lightsailctl"),
		// Hand-rolled cross-compile output.
		filepath.Join(cwd, "lightsailctl_linux_amd64"),
		filepath.Join(cwd, "lightsailctl-linux-amd64"),
	}
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
