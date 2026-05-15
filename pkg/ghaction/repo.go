// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package ghaction

import (
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
)

// RepoRef is the resolved identity of a GitHub repository.
type RepoRef struct {
	Owner string
	Repo  string
	// Host is always "github.com" in v1. Non-github.com remotes are
	// rejected by ParseRemoteURL so callers can rely on this.
	Host string
}

// String returns "<owner>/<repo>".
func (r RepoRef) String() string { return r.Owner + "/" + r.Repo }

// ParseRemoteURL turns any of the common `git remote -v` URL forms for
// github.com into a RepoRef. Returns an error for non-github.com
// remotes (GHES is out of scope for v1 — see plan.md).
//
// Accepted forms:
//
//	https://github.com/owner/repo(.git)?
//	http://github.com/owner/repo(.git)?      // rare, allowed for parity
//	git@github.com:owner/repo(.git)?
//	ssh://git@github.com/owner/repo(.git)?
//	github.com:owner/repo                    // terse, occasionally seen
func ParseRemoteURL(raw string) (RepoRef, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return RepoRef{}, fmt.Errorf("empty remote URL")
	}

	// SCP-ish form first: git@host:owner/repo[.git]
	// The SCP form has no "://" and has a colon before the path.
	if !strings.Contains(raw, "://") {
		if m := scpSSHRE.FindStringSubmatch(raw); len(m) == 4 {
			host, owner, repo := m[1], m[2], m[3]
			return newRef(host, owner, repo)
		}
	}

	// Everything else parses as a URL. We strip ssh://git@ so url.Parse
	// doesn't complain about the userinfo.
	u, err := url.Parse(raw)
	if err != nil {
		return RepoRef{}, fmt.Errorf("parse %q: %w", raw, err)
	}
	if u.Host == "" {
		return RepoRef{}, fmt.Errorf("parse %q: no host", raw)
	}
	// Strip port, userinfo already handled by net/url.
	host := hostOnly(u.Host)
	path := strings.TrimPrefix(u.Path, "/")
	segs := strings.Split(strings.TrimSuffix(path, "/"), "/")
	if len(segs) < 2 {
		return RepoRef{}, fmt.Errorf("parse %q: expected owner/repo in path, got %q", raw, u.Path)
	}
	return newRef(host, segs[0], segs[1])
}

// scpSSHRE matches the SCP-style form. Host group allows ports (:22
// would be invalid SCP anyway; we don't try to be clever).
var scpSSHRE = regexp.MustCompile(`^(?:[^@]+@)?([^:]+):([^/]+)/(.+?)(?:\.git)?/?$`)

func hostOnly(h string) string {
	// Strip :port if present.
	if i := strings.Index(h, ":"); i >= 0 {
		return h[:i]
	}
	return h
}

func newRef(host, owner, repo string) (RepoRef, error) {
	repo = strings.TrimSuffix(repo, ".git")
	if host == "" || owner == "" || repo == "" {
		return RepoRef{}, fmt.Errorf("incomplete remote: host=%q owner=%q repo=%q", host, owner, repo)
	}
	if !strings.EqualFold(host, "github.com") {
		return RepoRef{}, fmt.Errorf("unsupported remote host %q: only github.com is supported (GHES is out of scope)", host)
	}
	return RepoRef{Owner: owner, Repo: repo, Host: "github.com"}, nil
}

// DetectRemoteURL runs `git config --get remote.origin.url` in dir and
// returns the raw URL. Returns "" (no error) when the command fails;
// the caller decides whether absence is fatal.
//
// This is intentionally a shell-out rather than a libgit2 / go-git
// call: we care about the user's exact configured origin URL (including
// any url.<host>.insteadOf rewrites they have in git config), and the
// git CLI is the canonical source of truth for that. Matches how the
// rest of lightsailctl detects git state (see pkg/names).
func DetectRemoteURL(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "config", "--get", "remote.origin.url")
	out, err := cmd.Output()
	if err != nil {
		return "", nil //nolint:nilerr // absence is not an error
	}
	return strings.TrimSpace(string(out)), nil
}
