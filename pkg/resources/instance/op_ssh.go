// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package instance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/lightsail"
)

func sshOp(s *Store, suggest func(context.Context) ([]registry.Choice, error)) registry.Operation {
	return registry.Operation{
		Name: "ssh", Aliases: []string{"connect"}, Key: "x",
		Short:   "SSH to an instance",
		Enabled: isRunning,
		Fields: []registry.Field{
			{Flag: "name", Short: "n", Help: "instance name", Required: true, Suggest: suggest},
		},
		Run: func(ctx context.Context, in registry.Input) error {
			c, err := s.ensure(ctx)
			if err != nil {
				return err
			}
			name := in.Get("name")
			// Resolve region so we can get SSH creds.
			inst, err := c.GetInstance(ctx, name)
			if err != nil {
				return fmt.Errorf("get instance: %w", err)
			}
			rc := c.WithRegion(inst.Region)
			creds, err := rc.GetInstanceSSH(ctx, name)
			if err != nil {
				return err
			}
			defer creds.Remove()
			return runSSH(ctx, creds)
		},
	}
}

// runSSH execs `ssh` with the user's terminal wired through, so the
// user sees prompts and drives input normally. Cancellation of ctx
// (e.g. the user pressing Ctrl+C at the top-level, or SIGTERM via
// `kill PID`) sends SIGTERM to the ssh child so it can restore the
// local terminal from raw mode before exiting; cmd.WaitDelay caps
// the cleanup window before the runtime falls back to SIGKILL.
//
// Plain exec.CommandContext defaults to SIGKILL on cancel, which
// leaves the terminal in raw mode and forces the user to `reset`.
// Using SIGTERM + WaitDelay is the standard Go recipe for external
// programs that own terminal state.
func runSSH(ctx context.Context, creds *lightsail.SSHCredentials) error {
	args := append(lightsail.SSHOpts(creds.KeyPath), creds.Username+"@"+creds.Host)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Cancel = func() error {
		// Send SIGTERM so ssh can restore the terminal state it
		// mutated (raw mode, alt charset) before exiting. If it
		// ignores us, cmd.WaitDelay escalates to SIGKILL.
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 3 * time.Second
	err := cmd.Run()
	// Exit 130 = user pressed Ctrl+C to end the session; not an error.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 130 {
		return nil
	}
	return err
}
