package instance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/lightsail"
)

func sshOp(s *store, suggest func(context.Context) ([]registry.Choice, error)) registry.Operation {
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

func runSSH(ctx context.Context, creds *lightsail.SSHCredentials) error {
	args := append(lightsail.SSHOpts(creds.KeyPath), creds.Username+"@"+creds.Host)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	// Exit 130 = user pressed Ctrl+C to end the session; not an error.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 130 {
		return nil
	}
	return err
}
