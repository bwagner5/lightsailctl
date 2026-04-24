package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/lightsail"
)

// logsOp SSHes to the first target instance for app/env and remotely runs
// `lightsailctl app local logs -f` so the user sees a live compose log
// stream. This is a `Run` op so triad's TUI releases the terminal via
// tea.Exec for the duration (plan.md §6).
func logsOp(s *store, suggest func(context.Context) ([]registry.Choice, error)) registry.Operation {
	return registry.Operation{
		Name: "logs", Key: "l", Short: "tail docker compose logs on a target",
		Fields: []registry.Field{
			{Flag: "name", Short: "n", Help: "app name", Required: true, Suggest: suggest},
			{Flag: "env", Short: "e", Help: "environment", Default: "dev"},
		},
		Run: func(ctx context.Context, in registry.Input) error {
			c, err := s.ensure(ctx)
			if err != nil {
				return err
			}
			targets, err := c.FindTargetsForAppEnv(ctx, in.Get("name"), in.Get("env"))
			if err != nil {
				return err
			}
			if len(targets) == 0 {
				return fmt.Errorf("no target instance found for %s/%s", in.Get("name"), in.Get("env"))
			}
			t := targets[0]
			creds, err := c.GetInstanceSSH(ctx, t.Name)
			if err != nil {
				return err
			}
			defer creds.Remove()

			remote := fmt.Sprintf("sudo /usr/local/bin/lightsailctl app local logs --app %s --env %s -f",
				in.Get("name"), in.Get("env"))
			args := append(lightsail.SSHOpts(creds.KeyPath),
				"-t", creds.Username+"@"+creds.Host, remote)
			cmd := exec.CommandContext(ctx, "ssh", args...)
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		},
	}
}
