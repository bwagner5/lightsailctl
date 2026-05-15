// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package lightsail

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lightsail"
	lstypes "github.com/aws/aws-sdk-go-v2/service/lightsail/types"
)

// SSHCredentials is a temp SSH identity for one-shot use against a Lightsail
// instance. KeyPath points at a file mode-600 on disk; the caller must
// os.Remove it (and KeyPath+"-cert.pub" if present) when done.
type SSHCredentials struct {
	Username string
	Host     string
	KeyPath  string
}

// GetInstanceSSH fetches temporary SSH credentials for an instance and writes
// them to a tmp file. Callers defer SSHCredentials.Remove.
func (c *Client) GetInstanceSSH(ctx context.Context, instance string) (*SSHCredentials, error) {
	out, err := c.ls.GetInstanceAccessDetails(ctx, &lightsail.GetInstanceAccessDetailsInput{
		InstanceName: aws.String(instance),
		Protocol:     lstypes.InstanceAccessProtocolSsh,
	})
	if err != nil {
		return nil, fmt.Errorf("get instance access: %w", err)
	}
	d := out.AccessDetails
	if d == nil || d.PrivateKey == nil {
		return nil, fmt.Errorf("no SSH details returned for %s", instance)
	}

	f, err := os.CreateTemp("", "lightsailctl-ssh-*")
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, err
	}
	if _, err := f.WriteString(aws.ToString(d.PrivateKey)); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if cert := aws.ToString(d.CertKey); cert != "" {
		if err := os.WriteFile(f.Name()+"-cert.pub", []byte(cert), 0o600); err != nil {
			return nil, err
		}
	}
	return &SSHCredentials{
		Username: aws.ToString(d.Username),
		Host:     aws.ToString(d.IpAddress),
		KeyPath:  f.Name(),
	}, nil
}

// Remove wipes the on-disk files backing these credentials. Safe to call
// multiple times.
func (s *SSHCredentials) Remove() {
	if s == nil || s.KeyPath == "" {
		return
	}
	_ = os.Remove(s.KeyPath)
	_ = os.Remove(s.KeyPath + "-cert.pub")
	s.KeyPath = ""
}

// SSHOpts returns the standard ssh/scp options for a given key path: no host
// key prompts, short timeouts, keepalives. Pass-through friendly to
// append(SSHOpts(keyPath), target, remoteCmd).
func SSHOpts(keyPath string) []string {
	return []string{
		"-i", keyPath,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
	}
}

// SSHRun executes a shell command on the remote instance and returns the
// combined stdout+stderr. Intended for small, non-interactive commands.
//
// Transparent retry: a freshly-launched Lightsail instance can take 30-90s
// before sshd accepts connections. We silently retry connection-refused /
// connection-reset errors for up to ~2 minutes so the first post-create
// ssh call doesn't surface a transient network error to the user.
func (s *SSHCredentials) SSHRun(ctx context.Context, cmdStr string) ([]byte, error) {
	args := append(SSHOpts(s.KeyPath), s.Username+"@"+s.Host, cmdStr)
	var out []byte
	err := retrySSHWarmup(ctx, func(ctx context.Context) error {
		cmd := exec.CommandContext(ctx, "ssh", args...)
		var rerr error
		out, rerr = cmd.CombinedOutput()
		return rerr
	})
	return out, err
}

// SCPTo copies localPath to remotePath on the instance (via scp). Creates
// remote parent dirs via a preceding ssh mkdir -p when mkdirParent is true.
//
// See SSHRun for the retry rationale; scp's error shape on connection
// refused is identical.
func (s *SSHCredentials) SCPTo(ctx context.Context, localPath, remotePath string, mkdirParent bool) error {
	if mkdirParent {
		parent := remoteDir(remotePath)
		if parent != "" && parent != "/" {
			if out, err := s.SSHRun(ctx, fmt.Sprintf("sudo mkdir -p %s && sudo chmod 755 %s", parent, parent)); err != nil {
				return fmt.Errorf("mkdir %s: %s: %w", parent, out, err)
			}
		}
	}
	args := append(SSHOpts(s.KeyPath), localPath, s.Username+"@"+s.Host+":"+remotePath)
	return retrySSHWarmup(ctx, func(ctx context.Context) error {
		cmd := exec.CommandContext(ctx, "scp", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			// Preserve stderr for caller's error message on the final
			// failing attempt; retrySSHWarmup inspects err.Error() to
			// decide retry, so wrapping with %s is safe.
			return fmt.Errorf("scp %s: %s: %w", localPath, out, err)
		}
		return nil
	})
}

// remoteDir is path.Dir without the path import dance.
func remoteDir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return ""
}

// retrySSHWarmup silently retries fn while it returns an error that looks
// like "sshd isn't ready yet." A newly-launched Lightsail instance can
// go 30-90s between state=running and sshd listening on :22, and even
// longer when the AMI has a cloud-init script. We cap at ~2 minutes total.
//
// Non-warmup errors (auth failure, permission denied, file not found)
// are returned on the first call. Context cancellation propagates.
//
// The retry is intentionally invisible to the user: the UI shows a
// single spinner on the scp step, not a flood of failed attempts.
func retrySSHWarmup(ctx context.Context, fn func(context.Context) error) error {
	const (
		maxElapsed = 2 * time.Minute
		initial    = 2 * time.Second
		maxWait    = 10 * time.Second
	)
	deadline := time.Now().Add(maxElapsed)
	backoff := initial
	var lastErr error
	for {
		err := fn(ctx)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isSSHWarmupError(err) {
			// Not a warmup-shaped error — fail fast so the user gets
			// a real message (e.g. auth failure, missing file).
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("ssh warmup timed out after %s: %w", maxElapsed, lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < maxWait {
			backoff *= 2
			if backoff > maxWait {
				backoff = maxWait
			}
		}
	}
}

// isSSHWarmupError reports whether err looks like sshd-not-ready-yet.
// Matched against the textual form since ssh/scp exit with status 255
// and the distinguishing info is only in stderr.
func isSSHWarmupError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// "Connection refused" — sshd isn't listening yet.
	// "Connection reset"   — sshd accepted then kicked us mid-handshake.
	// "Connection closed"  — scp/ssh tail line after either of the above.
	// "No route to host"   — transient networking before ENI is wired.
	// "kex_exchange_identification" — sshd just came up, banner racing.
	// "port 22: Connection timed out" — sshd/ENI not yet reachable.
	for _, needle := range []string{
		"connection refused",
		"connection reset",
		"connection closed",
		"no route to host",
		"kex_exchange_identification",
		"connection timed out",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}
