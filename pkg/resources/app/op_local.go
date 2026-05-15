// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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
	root.AddCommand(localInstallCmd(), localUpCmd(), localDownCmd(), localWatchCmd(), localLogsCmd(), localLsCmd(), localStatusCmd())
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
	var app, env, service string
	var follow bool
	var lines int
	c := &cobra.Command{
		Use:   "logs",
		Short: "tail docker compose logs",
		RunE: func(cmd *cobra.Command, args []string) error {
			return localLogs(app, env, follow, lines, service)
		},
	}
	c.Flags().StringVar(&app, "app", "", "application name")
	c.Flags().StringVar(&env, "env", "", "environment")
	c.Flags().BoolVarP(&follow, "follow", "f", true, "follow log output")
	c.Flags().IntVar(&lines, "lines", 200, "number of lines to show from the end")
	c.Flags().StringVarP(&service, "service", "s", "", "show logs for a specific service only")
	_ = c.MarkFlagRequired("app")
	_ = c.MarkFlagRequired("env")
	return c
}

// localInstall writes the systemd unit file + the .bucket/.instance markers.
// The binary itself is placed by the scp-from-client step in `app create`.
// Idempotent.
//
// Runs as root (invoked via `sudo` from the client), but the watcher
// itself runs as the `lightsailctl` system user. We create the user
// here rather than relying solely on dockerize-remote.sh because:
//
//   - cloud-init runs user-data asynchronously, and SSH typically
//     becomes available before user-data finishes. A `localInstall`
//     SSH'd in during bootstrap would otherwise race the useradd in
//     the script.
//   - users supply their own user-data on `app create`, so we can't
//     assume dockerize-remote.sh ran at all.
//
// The user creation + chown are idempotent so re-running on an
// already-bootstrapped instance is a no-op.
func localInstall(app, env, bucket, region, instance string) error {
	if err := ensureServiceUser(); err != nil {
		return fmt.Errorf("ensure %s user: %w", serviceUser, err)
	}
	base := filepath.Join(lightsail.BaseDir, app, env)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(base, ".bucket"), []byte(bucket), 0o644); err != nil {
		return err
	}
	if region != "" {
		_ = os.WriteFile(filepath.Join(base, ".region"), []byte(region), 0o644)
	}
	if instance != "" {
		_ = os.WriteFile(filepath.Join(base, ".instance"), []byte(instance), 0o644)
	}
	if err := chownTree(filepath.Join(lightsail.BaseDir, app), serviceUser, serviceUser); err != nil {
		return fmt.Errorf("chown %s: %w", base, err)
	}
	unit := fmt.Sprintf(lightsail.UnitNameFmt, app, env)
	// The --log-dest=stderr argument routes structured records into
	// journald (via StandardOutput/Error=journal) so operators can run
	// `journalctl -u lightsailctl-<app>-<env>` to review watcher
	// behavior. No on-disk log file is kept on the instance.
	//
	// User=/Group= drop privileges to the lightsailctl service user
	// created by dockerize-remote.sh. systemd auto-derives HOME from
	// the user's passwd entry, which is what `pack` needs.
	// SupplementaryGroups=docker grants daemon access without
	// touching primary-group semantics.
	unitBody := fmt.Sprintf(`[Unit]
Description=Lightsail deployment watcher for %s/%s
After=network.target docker.service
Wants=docker.service

[Service]
Type=simple
User=%s
Group=%s
SupplementaryGroups=docker
StandardOutput=journal
StandardError=journal
ExecStart=/usr/local/bin/lightsailctl --log-dest=stderr app local watch --app %s --env %s --region %s
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
`, app, env, serviceUser, serviceUser, app, env, region)
	return os.WriteFile(fmt.Sprintf("/etc/systemd/system/%s.service", unit), []byte(unitBody), 0o644)
}

// serviceUser is the unprivileged system user the watcher runs as.
// Matched in the systemd unit's User=/Group= directives. The user is
// created on demand by ensureServiceUser; dockerize-remote.sh creates
// it ahead of time as a courtesy so the first deploy doesn't pay the
// useradd cost.
const serviceUser = "lightsailctl"

// ensureServiceUser creates the lightsailctl system user if it
// doesn't already exist, and ensures it's in the docker group so the
// watcher can drive `docker build` / `pack build` against the local
// daemon. Idempotent: re-runs on an existing user are no-ops aside
// from the gpasswd call (which is also idempotent).
//
// Runs as root (localInstall is invoked under sudo from the client).
// Errors propagate so the caller can refuse to write the unit file
// against a missing user — the watcher would fail to start otherwise.
func ensureServiceUser() error {
	if _, err := exec.LookPath("useradd"); err != nil {
		return fmt.Errorf("useradd not found in PATH: %w", err)
	}
	if _, err := user.Lookup(serviceUser); err == nil {
		// Already exists; just make sure it's in the docker group.
		// Failure here is non-fatal: the bootstrap script has the
		// canonical group config, and an admin may have intentionally
		// pruned membership. Log via the returned error from chown.
		_ = exec.Command("usermod", "-aG", "docker", serviceUser).Run()
		return nil
	}
	args := []string{"--system", "--create-home", "--shell", "/bin/bash", serviceUser}
	if out, err := exec.Command("useradd", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("useradd %s: %s: %w", serviceUser, strings.TrimSpace(string(out)), err)
	}
	if out, err := exec.Command("usermod", "-aG", "docker", serviceUser).CombinedOutput(); err != nil {
		return fmt.Errorf("usermod -aG docker %s: %s: %w", serviceUser, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// chownTree recursively chowns dir to user:group. Best-effort on
// individual entries: a missing dir is fine (nothing to do); a
// permission error on an unrelated file shouldn't abort install.
func chownTree(dir, user, group string) error {
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	cmd := exec.Command("chown", "-R", user+":"+group, dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("chown -R %s:%s %s: %s: %w", user, group, dir, strings.TrimSpace(string(out)), err)
	}
	return nil
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

	// Best-effort: delete the status file from the bucket so the TUI
	// doesn't keep reporting the instance as healthy after shutdown.
	deleteBucketStatus(base)

	if err := os.RemoveAll(base); err != nil {
		return err
	}
	parent := filepath.Join(lightsail.BaseDir, app)
	if entries, err := os.ReadDir(parent); err == nil && len(entries) == 0 {
		_ = os.Remove(parent)
	}
	return nil
}

// deleteBucketStatus removes the <instance>_status.json object from the env
// bucket. Runs best-effort using IMDS credentials; failures are silently
// ignored since the local teardown must still proceed.
func deleteBucketStatus(baseDir string) {
	bucket, _ := os.ReadFile(filepath.Join(baseDir, ".bucket"))
	instance, _ := os.ReadFile(filepath.Join(baseDir, ".instance"))
	region, _ := os.ReadFile(filepath.Join(baseDir, ".region"))
	b := strings.TrimSpace(string(bucket))
	inst := strings.TrimSpace(string(instance))
	reg := strings.TrimSpace(string(region))
	if b == "" || inst == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	opts := []func(*config.LoadOptions) error{}
	if reg != "" {
		opts = append(opts, config.WithRegion(reg))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not delete bucket status: %v\n", err)
		return
	}
	svc := s3.NewFromConfig(cfg)
	key := inst + lightsail.StatusSuffix
	if _, err := svc.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(b),
		Key:    aws.String(key),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not delete bucket status %s/%s: %v\n", b, key, err)
	}
}

func localLogs(app, env string, follow bool, lines int, service string) error {
	current := filepath.Join(lightsail.BaseDir, app, env, "current")
	cf := findCompose(current)
	if cf == "" {
		return fmt.Errorf("no compose file at %s (is the app deployed?)", current)
	}
	args := []string{"compose", "-f", cf, "logs", "--tail", fmt.Sprintf("%d", lines)}
	if follow {
		args = append(args, "-f")
	}
	if service != "" {
		args = append(args, service)
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

// --- ls command ---

func localLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "list all apps/envs on this instance",
		RunE: func(cmd *cobra.Command, args []string) error {
			return localLs()
		},
	}
}

func localLs() error {
	entries, err := os.ReadDir(lightsail.BaseDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No apps found.")
			return nil
		}
		return err
	}

	type row struct {
		app, env, watcher, status, lastDeploy, containers string
	}
	var rows []row

	for _, appEntry := range entries {
		if !appEntry.IsDir() {
			continue
		}
		appName := appEntry.Name()
		envEntries, err := os.ReadDir(filepath.Join(lightsail.BaseDir, appName))
		if err != nil {
			continue
		}
		for _, envEntry := range envEntries {
			if !envEntry.IsDir() {
				continue
			}
			envName := envEntry.Name()
			baseDir := filepath.Join(lightsail.BaseDir, appName, envName)
			currentDir := filepath.Join(baseDir, "current")
			unit := fmt.Sprintf(lightsail.UnitNameFmt, appName, envName)

			// Read markers best-effort; localLs is a read-only status view.
			lastKey := ""
			if b, rerr := os.ReadFile(filepath.Join(baseDir, ".last-deploy")); rerr == nil {
				lastKey = strings.TrimSpace(string(b))
			}
			bucket := ""
			if b, rerr := os.ReadFile(filepath.Join(baseDir, ".bucket")); rerr == nil {
				bucket = strings.TrimSpace(string(b))
			}
			instance := ""
			if b, rerr := os.ReadFile(filepath.Join(baseDir, ".instance")); rerr == nil {
				instance = strings.TrimSpace(string(b))
			}
			st := watch.LocalStatus(instance, bucket, lastKey, currentDir)

			rows = append(rows, row{
				app:        appName,
				env:        envName,
				watcher:    systemctlState(unit),
				status:     st.Status,
				lastDeploy: formatLastDeploy(st.LastDeploy),
				containers: composeContainerSummary(currentDir),
			})
		}
	}

	if len(rows) == 0 {
		fmt.Println("No apps found.")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	// Column meanings:
	//   WATCHER     systemd ActiveState of the lightsail-watch-<app>-<env> unit.
	//   STATUS      application-level status computed from docker ps output.
	//               idle = no containers; healthy/degraded/down = deploy active.
	//   LAST DEPLOY timestamp of the last applied deploy tarball, or "never".
	//   CONTAINERS  docker compose ps summary, or "-" when nothing is deployed.
	_, _ = fmt.Fprintf(tw, "APP\tENV\tWATCHER\tSTATUS\tLAST DEPLOY\tCONTAINERS\n")
	for _, r := range rows {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.app, r.env, r.watcher, r.status, r.lastDeploy, r.containers)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	// Footer: give operators the commands they'll almost always want
	// next. The unit name is mechanical, but printing it saves a tab-
	// complete dance.
	_, _ = fmt.Fprintln(os.Stdout)
	_, _ = fmt.Fprintln(os.Stdout, "Tail watcher logs:  journalctl -u lightsail-watch-<app>-<env> -f")
	_, _ = fmt.Fprintln(os.Stdout, "App logs:           lightsailctl app local logs --app <app> --env <env>")
	return nil
}

// formatLastDeploy returns a short, human-readable last-deploy column
// value. Empty DeployInfo → "never" so the absence of any deploy is
// obvious at a glance.
func formatLastDeploy(d *lightsail.DeployInfo) string {
	if d == nil || d.Timestamp.IsZero() {
		return "never"
	}
	age := time.Since(d.Timestamp)
	switch {
	case age < time.Minute:
		return "just now"
	case age < time.Hour:
		return fmt.Sprintf("%dm ago", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(age.Hours()))
	default:
		return d.Timestamp.UTC().Format("2006-01-02 15:04")
	}
}

// systemctlState returns the ActiveState of a systemd unit (active, inactive, failed, etc).
func systemctlState(unit string) string {
	out, err := exec.Command("systemctl", "show", "--property=ActiveState", "--value", unit+".service").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// composeContainerSummary returns a short summary of running containers like "web(running),db(running)".
func composeContainerSummary(currentDir string) string {
	cf := findCompose(currentDir)
	if cf == "" {
		return "-"
	}
	cmd := exec.Command("docker", "compose", "-f", cf, "ps", "--format", "json")
	cmd.Dir = currentDir
	out, err := cmd.Output()
	if err != nil {
		return "-"
	}
	type cps struct {
		Service string `json:"Service"`
		State   string `json:"State"`
	}
	var parts []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var c cps
		if json.Unmarshal([]byte(line), &c) != nil {
			continue
		}
		parts = append(parts, c.Service+"("+c.State+")")
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ",")
}

// --- status command ---

func localStatusCmd() *cobra.Command {
	var app, env string
	c := &cobra.Command{
		Use:   "status",
		Short: "print the current status for an app/env",
		RunE: func(cmd *cobra.Command, args []string) error {
			return localStatus(app, env)
		},
	}
	c.Flags().StringVar(&app, "app", "", "application name")
	c.Flags().StringVar(&env, "env", "", "environment")
	_ = c.MarkFlagRequired("app")
	_ = c.MarkFlagRequired("env")
	return c
}

func localStatus(app, env string) error {
	baseDir := filepath.Join(lightsail.BaseDir, app, env)
	if _, err := os.Stat(baseDir); err != nil {
		return fmt.Errorf("app %s/%s not found on this instance", app, env)
	}
	currentDir := filepath.Join(baseDir, "current")

	// Read instance name from .instance marker or fall back to hostname.
	instance := ""
	if b, err := os.ReadFile(filepath.Join(baseDir, ".instance")); err == nil {
		instance = strings.TrimSpace(string(b))
	}
	if instance == "" {
		instance, _ = os.Hostname()
	}

	// Read bucket and region from markers (best-effort for the status shape).
	bucket := ""
	if b, err := os.ReadFile(filepath.Join(baseDir, ".bucket")); err == nil {
		bucket = strings.TrimSpace(string(b))
	}

	// Read last deploy key.
	lastKey := ""
	if b, err := os.ReadFile(filepath.Join(baseDir, ".last-deploy")); err == nil {
		lastKey = strings.TrimSpace(string(b))
	}

	// Regenerate status using the same logic as the watcher.
	st := watch.LocalStatus(instance, bucket, lastKey, currentDir)
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}
