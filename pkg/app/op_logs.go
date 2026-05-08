package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/lightsail"
)

// logsOp SSHes to a target instance for app/env and remotely runs
// `lightsailctl app local logs` so the user sees a live compose log
// stream. This is a `Run` op so triad's TUI releases the terminal via
// tea.Exec for the duration (plan.md §6).
//
// Wizard inputs are kept minimal — env and instance only. The remote
// command relies on the defaults of `lightsailctl app local logs`
// (follow=true, lines=200) so the user doesn't have to specify them.
// Service filtering is intentionally not exposed yet; add it back once
// we have a good way to discover services for the Suggest list.
func logsOp(s *store, suggest func(context.Context) ([]registry.Choice, error)) registry.Operation {
	return registry.Operation{
		Name: "logs", Key: "l", Short: "tail docker compose logs on a target",
		Fields: []registry.Field{
			{Flag: "name", Short: "n", Label: "App name", Help: "app name",
				Required: true, Suggest: suggest},
			{Flag: "env", Short: "e", Label: "Environment", Help: "environment",
				Required: true, Default: "dev", Suggest: envSuggest(s)},
			{Flag: "instance", Short: "i", Label: "Instance",
				Help: "target instance", Required: true,
				Suggest: instanceSuggest(s)},
		},
		Run: func(ctx context.Context, in registry.Input) error {
			c, err := s.ensure(ctx)
			if err != nil {
				return err
			}
			appName := in.Get("name")
			env := in.Get("env")
			instance := in.Get("instance")

			targets, err := c.FindTargetsForAppEnv(ctx, appName, env)
			if err != nil {
				return err
			}
			if len(targets) == 0 {
				return fmt.Errorf("no target instance found for %s/%s", appName, env)
			}
			// Pick the requested target (validates it's actually tagged
			// for this app/env). Fall back to the first target when no
			// instance was specified — keeps the non-interactive/CLI
			// path usable without a picker.
			var t lightsail.Instance
			if instance == "" {
				t = targets[0]
			} else {
				found := false
				for _, x := range targets {
					if x.Name == instance {
						t = x
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("instance %q is not a target for %s/%s",
						instance, appName, env)
				}
			}

			// GetInstanceAccessDetails is a regional Lightsail API. When
			// the store is in global mode (no --region) c.ls is nil, so
			// we must region-pin the client to the target's region first
			// — otherwise the call panics with a nil-pointer deref. Same
			// pattern as op_delete / op_remove_target.
			rc := c.WithRegion(t.Region)
			creds, err := rc.GetInstanceSSH(ctx, t.Name)
			if err != nil {
				return err
			}
			defer creds.Remove()

			remote := fmt.Sprintf("sudo /usr/local/bin/lightsailctl app local logs --app %s --env %s",
				appName, env)
			args := append(lightsail.SSHOpts(creds.KeyPath),
				"-t", creds.Username+"@"+creds.Host, remote)
			cmd := exec.CommandContext(ctx, "ssh", args...)
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			// Send SIGTERM on ctx cancel so ssh can restore the
			// local terminal before exiting. Same rationale as
			// instance ssh; see pkg/instance/op_ssh.go for the
			// full context.
			cmd.Cancel = func() error {
				if cmd.Process == nil {
					return nil
				}
				return cmd.Process.Signal(syscall.SIGTERM)
			}
			cmd.WaitDelay = 3 * time.Second
			return cmd.Run()
		},
	}
}
