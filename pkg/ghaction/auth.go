package ghaction

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// AuthMode is how the user wants us to obtain a GitHub token.
type AuthMode string

const (
	// AuthAuto tries, in order: --github-token flag value, $GITHUB_TOKEN
	// env var, then `gh auth token`. If all three produce nothing, the
	// interactive wizard falls through to prompting; -y mode fails.
	AuthAuto AuthMode = "auto"
	// AuthGH shells out to the `gh` CLI (`gh auth token`).
	AuthGH AuthMode = "gh"
	// AuthToken uses an explicitly provided PAT (flag or env).
	AuthToken AuthMode = "token"
	// AuthDevice opens a GitHub OAuth device-flow session.
	AuthDevice AuthMode = "device"
	// AuthNone explicitly skips GitHub entirely. Meant for offline
	// dry-run flows where the caller has already supplied repo metadata
	// via flags (owner/repo/repository_id).
	AuthNone AuthMode = "none"
)

// AuthResult is what ResolveToken returns on success. User is informational
// (e.g. "alice"), logged to the user on the "Using ..." line.
type AuthResult struct {
	Token    string
	Source   AuthMode // which branch actually produced the token
	UserHint string   // best-effort GitHub login (empty if unknown)
}

// ResolveToken picks a GitHub token according to AuthMode and the
// inputs available. It never logs the token itself. The returned
// AuthResult.Source is the branch we actually took, which callers log
// for transparency.
//
// Inputs:
//   - mode:     what the user asked for (auto/gh/token/device/none)
//   - tokenFlag: value of --github-token, "" if unset
//   - envToken: value of $GITHUB_TOKEN, "" if unset
//   - promptPAT / promptDevice: optional callbacks the wizard can
//     provide to gather a PAT interactively / run device flow. In
//     non-interactive (-y) mode the caller passes nil and we fail
//     cleanly when the non-interactive paths run out of options.
func ResolveToken(ctx context.Context, mode AuthMode, tokenFlag, envToken string,
	promptPAT func(ctx context.Context) (string, error),
	promptDevice func(ctx context.Context) (AuthResult, error),
) (AuthResult, error) {
	switch mode {
	case AuthNone:
		return AuthResult{Source: AuthNone}, nil

	case AuthToken:
		tok := firstNonEmpty(tokenFlag, envToken)
		if tok == "" {
			return AuthResult{}, fmt.Errorf("--github-auth=token requires --github-token or $GITHUB_TOKEN")
		}
		return AuthResult{Token: tok, Source: AuthToken}, nil

	case AuthGH:
		tok, user, err := ghAuthToken(ctx)
		if err != nil {
			return AuthResult{}, fmt.Errorf("gh auth token: %w", err)
		}
		return AuthResult{Token: tok, Source: AuthGH, UserHint: user}, nil

	case AuthDevice:
		if promptDevice == nil {
			return AuthResult{}, fmt.Errorf("--github-auth=device requires an interactive terminal")
		}
		return promptDevice(ctx)

	case AuthAuto, "":
		// Priority 1: --github-token flag
		if tokenFlag != "" {
			return AuthResult{Token: tokenFlag, Source: AuthToken}, nil
		}
		// Priority 2: $GITHUB_TOKEN
		if envToken != "" {
			return AuthResult{Token: envToken, Source: AuthToken}, nil
		}
		// Priority 3: gh CLI
		if tok, user, err := ghAuthToken(ctx); err == nil && tok != "" {
			return AuthResult{Token: tok, Source: AuthGH, UserHint: user}, nil
		}
		// Priority 4: interactive prompt (PAT paste)
		if promptPAT != nil {
			if tok, err := promptPAT(ctx); err == nil && tok != "" {
				return AuthResult{Token: tok, Source: AuthToken}, nil
			}
		}
		return AuthResult{}, fmt.Errorf("no GitHub token available: set --github-token, $GITHUB_TOKEN, or run `gh auth login`")
	}
	return AuthResult{}, fmt.Errorf("unknown --github-auth=%q", mode)
}

// ghAuthToken shells out to `gh auth token` and, best-effort,
// `gh api user --jq .login`. Returns ("", "", err) if gh isn't on PATH
// or isn't logged in.
func ghAuthToken(ctx context.Context) (token, user string, err error) {
	if _, lerr := exec.LookPath("gh"); lerr != nil {
		return "", "", fmt.Errorf("gh CLI not on PATH")
	}
	tok, err := runCmd(ctx, "gh", "auth", "token")
	if err != nil {
		return "", "", fmt.Errorf("gh auth token failed: %w", err)
	}
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return "", "", fmt.Errorf("gh auth token returned empty output")
	}
	// User hint is best-effort; don't fail the token resolution on it.
	user, _ = runCmd(ctx, "gh", "api", "user", "--jq", ".login")
	return tok, strings.TrimSpace(user), nil
}

func runCmd(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// ── Device flow ────────────────────────────────────────────────────────
//
// GitHub's OAuth device flow is a two-call dance: (1) request a device +
// user code, show the user_code + verification_uri and ask them to open
// it; (2) poll token endpoint until they authorize or we time out.
//
// We speak the API directly rather than pull in github.com/cli/oauth,
// which would nearly double the binary's dependency surface for ~80 LOC
// of JSON. Both endpoints are documented and stable.
//
//   POST https://github.com/login/device/code
//   POST https://github.com/login/oauth/access_token
//
// Scope "repo" is the smallest useful set per plan.md ("Required
// OAuth scope"). No write scopes — we only read repo metadata.

// GitHubCLIClientID is the OAuth client id of the public "gh" GitHub CLI
// app. It's a stable public identifier, used here only for the device
// flow — the same id GitHub's own gh CLI advertises. Users see this
// client name on the consent screen.
const GitHubCLIClientID = "178c6fc778ccc68e1d6a"

// DeviceCode is the intermediate response from the device-code endpoint.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	Interval        int    `json:"interval"`
	ExpiresIn       int    `json:"expires_in"`
}

// RequestDeviceCode performs the first step of the device flow.
func RequestDeviceCode(ctx context.Context, clientID, scope string) (DeviceCode, error) {
	form := url.Values{
		"client_id": {clientID},
		"scope":     {scope},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://github.com/login/device/code", strings.NewReader(form.Encode()))
	if err != nil {
		return DeviceCode{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return DeviceCode{}, err
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return DeviceCode{}, fmt.Errorf("device code: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var dc DeviceCode
	if err := json.NewDecoder(resp.Body).Decode(&dc); err != nil {
		return DeviceCode{}, fmt.Errorf("decode device code: %w", err)
	}
	if dc.Interval == 0 {
		dc.Interval = 5
	}
	return dc, nil
}

// PollForToken runs the device-flow polling loop. Writes status
// messages to status (prompts the user with the user_code and URL
// on first call). Returns the access token once the user authorizes,
// or an error on expiry / denial.
func PollForToken(ctx context.Context, clientID string, dc DeviceCode, status io.Writer) (string, error) {
	if status != nil {
		fmt.Fprintf(status, "To authorize, open %s and enter code: %s\n",
			dc.VerificationURI, dc.UserCode)
	}
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	if dc.ExpiresIn == 0 {
		deadline = time.Now().Add(15 * time.Minute)
	}
	interval := time.Duration(dc.Interval) * time.Second
	for {
		if time.Now().After(deadline) {
			return "", fmt.Errorf("device flow expired before user authorized")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(interval):
		}
		tok, retry, err := exchangeDeviceCode(ctx, clientID, dc.DeviceCode)
		if tok != "" {
			return tok, nil
		}
		if err != nil && !retry {
			return "", err
		}
	}
}

// exchangeDeviceCode performs a single token-exchange attempt.
// Returns (token, retry, err):
//   - token != "" on success
//   - retry==true + err=="authorization_pending" keeps polling
//   - retry==true + err=="slow_down" means back off (not propagated to caller as error)
//   - retry==false + err on terminal failure (expired / denied)
func exchangeDeviceCode(ctx context.Context, clientID, deviceCode string) (string, bool, error) {
	form := url.Values{
		"client_id":   {clientID},
		"device_code": {deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://github.com/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", true, err
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	var r struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if derr := json.NewDecoder(resp.Body).Decode(&r); derr != nil {
		return "", true, derr
	}
	if r.AccessToken != "" {
		return r.AccessToken, false, nil
	}
	switch r.Error {
	case "authorization_pending", "slow_down":
		return "", true, nil
	case "expired_token", "access_denied":
		return "", false, fmt.Errorf("%s", r.Error)
	default:
		return "", false, fmt.Errorf("device token exchange: %s", r.Error)
	}
}

// ReadPATFromTTY reads a single line from r (typically os.Stdin),
// trimming whitespace. Provided here for wizards that want a fallback
// path without pulling in a TTY-masking dep. Callers that need
// masking should implement a SensitiveInput-aware prompt instead.
func ReadPATFromTTY(r io.Reader) (string, error) {
	br := bufio.NewReader(r)
	line, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// tokenLooksLikePAT is a tiny sanity check so we don't POST obviously
// wrong values at GitHub. Matches classic and fine-grained PAT prefixes.
// Returns true for anything we can't recognize, so unknown future
// shapes don't falsely reject.
func tokenLooksLikePAT(tok string) bool {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return false
	}
	known := []string{"ghp_", "github_pat_", "gho_", "ghu_", "ghs_", "ghr_"}
	for _, p := range known {
		if strings.HasPrefix(tok, p) {
			return true
		}
	}
	// 40-char hex (legacy) also looks PAT-ish.
	if len(tok) == 40 {
		for _, r := range tok {
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
			if !isHex {
				return true // not legacy hex but still allow
			}
		}
		return true
	}
	return true
}

// Compile-time usage of helpers that might otherwise be dead-code pruned
// by linters; keep them callable from client.go / callers.
var _ = ReadPATFromTTY
var _ = tokenLooksLikePAT
