package lightsail

import (
	"context"
	"fmt"
	"os"
	"os/exec"

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
func (s *SSHCredentials) SSHRun(ctx context.Context, cmdStr string) ([]byte, error) {
	args := append(SSHOpts(s.KeyPath), s.Username+"@"+s.Host, cmdStr)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	return cmd.CombinedOutput()
}

// SCPTo copies localPath to remotePath on the instance (via scp). Creates
// remote parent dirs via a preceding ssh mkdir -p when mkdirParent is true.
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
	cmd := exec.CommandContext(ctx, "scp", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("scp %s: %s: %w", localPath, out, err)
	}
	return nil
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
