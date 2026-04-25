package names

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DefaultAppName returns a sensible default for a fresh app name:
//  1. The name of the current git repository (derived from the remote
//     origin URL, or the repo root directory when there is no remote).
//  2. If no git repo is present, a space-themed random name.
//
// The resulting name is lowercased and trimmed so it is safe to use in
// bucket names / tag values without further munging.
func DefaultAppName() string {
	if n := gitRepoName(); n != "" {
		return sanitize(n)
	}
	return Random()
}

// gitRepoName walks up from cwd looking for a .git dir. If found, it first
// tries to pull the last segment of origin's URL out of .git/config; if
// that isn't available, it falls back to the directory name that contains
// the .git dir.
func gitRepoName() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		gitDir := filepath.Join(dir, ".git")
		if fi, err := os.Stat(gitDir); err == nil && fi.IsDir() {
			if name := parseOriginName(filepath.Join(gitDir, "config")); name != "" {
				return name
			}
			return filepath.Base(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// originURL pattern matches both git@host:owner/repo.git and
// https://host/owner/repo.git. We only care about the last path segment.
var repoSegment = regexp.MustCompile(`[:/]([^:/]+?)(?:\.git)?\s*$`)

// parseOriginName reads .git/config and extracts the repo name from the
// [remote "origin"] section's url. Returns "" when not found.
func parseOriginName(configPath string) string {
	f, err := os.Open(configPath) // #nosec G304 -- path is a fixed .git/config next to the cwd walk
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	inOrigin := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			inOrigin = line == `[remote "origin"]`
			continue
		}
		if !inOrigin || !strings.HasPrefix(line, "url") {
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		url := strings.TrimSpace(line[eq+1:])
		if m := repoSegment.FindStringSubmatch(url); len(m) == 2 {
			return m[1]
		}
	}
	return ""
}

// sanitize lowercases and replaces non-alphanumeric runs with a single
// dash so the name slots cleanly into S3 bucket names and DNS-like
// constraints.
func sanitize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteRune('-')
				lastDash = true
			}
		}
	}
	out := b.String()
	return strings.Trim(out, "-")
}
