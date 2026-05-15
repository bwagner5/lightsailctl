// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package ghaction

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/google/go-github/v67/github"
)

// RepoMetadata is the minimal subset of a GitHub repository we care
// about: the immutable numeric ID (used as the `repository_id` trust
// condition) and whether the repo is private (informational only —
// OIDC works the same way for either).
type RepoMetadata struct {
	Owner        string
	Repo         string
	RepositoryID string // string form of the numeric ID
	Private      bool
}

// FetchRepoMetadata hits GET /repos/{owner}/{repo} via go-github and
// returns the minimal metadata the saga needs. If token is "" the
// request is unauthenticated — sufficient for public repos, will fail
// with 404 (not 403) for private ones, which we translate into a
// clearer error.
//
// The token is only ever used for this one read. We never persist it,
// log it, or send it anywhere else.
func FetchRepoMetadata(ctx context.Context, token, owner, repo string) (RepoMetadata, error) {
	client := newGHClient(token)
	r, resp, err := client.Repositories.Get(ctx, owner, repo)
	if err != nil {
		return RepoMetadata{}, classifyGHError(err, resp, owner, repo)
	}
	return RepoMetadata{
		Owner:        owner,
		Repo:         repo,
		RepositoryID: strconv.FormatInt(r.GetID(), 10),
		Private:      r.GetPrivate(),
	}, nil
}

// AuthenticatedUser returns the GitHub login associated with token, or
// "" if the call fails (which callers should treat as "unknown user").
// Used for the "Using gh CLI session (user: alice, ...)" disclosure.
func AuthenticatedUser(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", fmt.Errorf("no token")
	}
	client := newGHClient(token)
	u, _, err := client.Users.Get(ctx, "")
	if err != nil {
		return "", err
	}
	return u.GetLogin(), nil
}

// newGHClient builds a go-github client with token auth. go-github/v67
// takes an *http.Client; we build one with a tiny RoundTripper that
// injects the Authorization header.
func newGHClient(token string) *github.Client {
	if token == "" {
		return github.NewClient(nil)
	}
	return github.NewClient(&http.Client{
		Transport: &authRT{token: token, base: http.DefaultTransport},
	})
}

type authRT struct {
	token string
	base  http.RoundTripper
}

func (a *authRT) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone to avoid mutating the caller's request.
	r2 := req.Clone(req.Context())
	r2.Header.Set("Authorization", "Bearer "+a.token)
	return a.base.RoundTrip(r2)
}

// classifyGHError produces a human-friendly error for common GitHub
// API failures the saga surfaces. Preserves the underlying error via
// %w so callers can errors.Is / errors.As against go-github's types.
func classifyGHError(err error, resp *github.Response, owner, repo string) error {
	if resp != nil {
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return fmt.Errorf("GitHub auth failed (401) fetching %s/%s: %w", owner, repo, err)
		case http.StatusForbidden:
			return fmt.Errorf("GitHub returned 403 fetching %s/%s (token may lack `repo` scope or be rate-limited): %w", owner, repo, err)
		case http.StatusNotFound:
			return fmt.Errorf("GitHub repo %s/%s not found (if private, token needs `repo` scope): %w", owner, repo, err)
		}
	}
	return fmt.Errorf("fetch %s/%s: %w", owner, repo, err)
}
