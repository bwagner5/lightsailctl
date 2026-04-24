package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/aws/lightsailctl/pkg/lightsail"
	"github.com/aws/lightsailctl/pkg/watch"
)

// LocalCommand builds the `lightsailctl app local` subtree. These commands
// run ON the Lightsail instance and are invoked remotely via SSH by
// client-side operations (create, delete). They are hand-rolled cobra rather
// than triad operations because triad doesn't nest operation groups today,
// and these commands are machine-facing (no interactive UX needed).
func LocalCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "local",
		Short: "[instance] commands invoked over SSH by the client",
	}
	root.AddCommand(localInstallCmd(), localUpCmd(), localDownCmd(), localWatchCmd(), localLogsCmd())
	return root
}

func localInstallCmd() *cobra.Command {
	var app, env, bucket, region, instance string
	c := &cobra.Command{
		Use:   "install",
		Short: "install the deployment watcher (systemd unit + markers)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return localInstall(app, env, bucket, region, instance)
		},
	}
	c.Flags().StringVar(&app, "app", "", "application name")
	c.Flags().StringVar(&env, "env", "", "environment")
	c.Flags().StringVar(&bucket, "bucket", "", "env bucket name")
	c.Flags().StringVar(&region, "region", "", "AWS region")
	c.Flags().StringVar(&instance, "instance", "", "instance name (override for logging)")
	_ = c.MarkFlagRequired("app")
	_ = c.MarkFlagRequired("env")
	_ = c.MarkFlagRequired("bucket")
	_ = c.MarkFlagRequired("region")
	return c
}

func localUpCmd() *cobra.Command {
	var app, env string
	c := &cobra.Command{
		Use:   "up",
		Short: "start (or restart) the watcher",
		RunE: func(cmd *cobra.Command, args []string) error {
			unit := fmt.Sprintf(lightsail.UnitNameFmt, app, env)
			if err := systemctl("daemon-reload"); err != nil {
				return err
			}
			if err := systemctl("enable", unit); err != nil {
				return err
			}
			return systemctl("restart", unit)
		},
	}
	c.Flags().StringVar(&app, "app", "", "application name")
	c.Flags().StringVar(&env, "env", "", "environment")
	_ = c.MarkFlagRequired("app")
	_ = c.MarkFlagRequired("env")
	return c
}

func localDownCmd() *cobra.Command {
	var app, env string
	c := &cobra.Command{
		Use:   "down",
		Short: "stop the watcher, tear down compose, remove state",
		RunE: func(cmd *cobra.Command, args []string) error {
			return localDown(app, env)
		},
	}
	c.Flags().StringVar(&app, "app", "", "application name")
	c.Flags().StringVar(&env, "env", "", "environment")
	_ = c.MarkFlagRequired("app")
	_ = c.MarkFlagRequired("env")
	return c
}

func localWatchCmd() *cobra.Command {
	var app, env, region string
	c := &cobra.Command{
		Use:   "watch",
		Short: "run the watch loop (called by systemd)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return watch.Run(cmd.Context(), watch.DefaultOptions(app, env, region))
		},
	}
	c.Flags().StringVar(&app, "app", "", "application name")
	c.Flags().StringVar(&env, "env", "", "environment")
	c.Flags().StringVar(&region, "region", "", "AWS region")
	_ = c.MarkFlagRequired("app")
	_ = c.MarkFlagRequired("env")
	_ = c.MarkFlagRequired("region")
	return c
}

func localLogsCmd() *cobra.Command {
	var app, env string
	var follow bool
	c := &cobra.Command{
		Use:   "logs",
		Short: "tail docker compose logs",
		RunE: func(cmd *cobra.Command, args []string) error {
			return localLogs(app, env, follow)
		},
	}
	c.Flags().StringVar(&app, "app", "", "application name")
	c.Flags().StringVar(&env, "env", "", "environment")
	c.Flags().BoolVarP(&follow, "follow", "f", true, "follow")
	_ = c.MarkFlagRequired("app")
	_ = c.MarkFlagRequired("env")
	return c
}

// localInstall writes the systemd unit file + the .bucket/.instance markers.
// The binary itself is placed by the scp-from-client step in `app create`.
// Idempotent.
func localInstall(app, env, bucket, region, instance string) error {
	base := filepath.Join(lightsail.BaseDir, app, env)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(base, ".bucket"), []byte(bucket), 0o644); err != nil {
		return err
	}
	if instance != "" {
		_ = os.WriteFile(filepath.Join(base, ".instance"), []byte(instance), 0o644)
	}
	unit := fmt.Sprintf(lightsail.UnitNameFmt, app, env)
	unitBody := fmt.Sprintf(`[Unit]
Description=Lightsail deployment watcher for %s/%s
After=network.target docker.service
Wants=docker.service

[Service]
Type=simple
ExecStart=/usr/local/bin/lightsailctl app local watch --app %s --env %s --region %s
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
`, app, env, app, env, region)
	return os.WriteFile(fmt.Sprintf("/etc/systemd/system/%s.service", unit), []byte(unitBody), 0o644)
}

func localDown(app, env string) error {
	unit := fmt.Sprintf(lightsail.UnitNameFmt, app, env)
	_ = systemctl("stop", unit)
	_ = systemctl("disable", unit)
	_ = os.Remove(fmt.Sprintf("/etc/systemd/system/%s.service", unit))
	_ = systemctl("daemon-reload")

	base := filepath.Join(lightsail.BaseDir, app, env)
	current := filepath.Join(base, "current")
	if cf := findCompose(current); cf != "" {
		c := exec.Command("docker", "compose", "-f", cf, "down")
		c.Dir = current
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		_ = c.Run()
	}
	if err := os.RemoveAll(base); err != nil {
		return err
	}
	parent := filepath.Join(lightsail.BaseDir, app)
	if entries, err := os.ReadDir(parent); err == nil && len(entries) == 0 {
		_ = os.Remove(parent)
	}
	return nil
}

func localLogs(app, env string, follow bool) error {
	current := filepath.Join(lightsail.BaseDir, app, env, "current")
	cf := findCompose(current)
	if cf == "" {
		return fmt.Errorf("no compose file at %s (is the app deployed?)", current)
	}
	args := []string{"compose", "-f", cf, "logs", "--tail", "200"}
	if follow {
		args = append(args, "-f")
	}
	cmd := exec.Command("docker", args...)
	cmd.Dir = current
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func systemctl(args ...string) error {
	cmd := exec.Command("systemctl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func findCompose(dir string) string {
	for _, n := range []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"} {
		p := filepath.Join(dir, n)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
