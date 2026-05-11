// Package watch runs on the Lightsail instance. It polls the env bucket for
// new deploy tarballs, applies them via `docker compose`, and writes status
// back to the bucket for `lightsailctl app status` to consume.
//
// Credentials come exclusively from IMDS (SetResourceAccessForBucket grants
// the instance access to the bucket). No static keys on disk.
package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/bwagner5/triad/pkg/trace"

	"github.com/aws/lightsailctl/internal/logging"
	"github.com/aws/lightsailctl/pkg/build"
	"github.com/aws/lightsailctl/pkg/lightsail"
)

// Options controls Watch behaviour.
type Options struct {
	App          string
	Env          string
	Region       string
	Interval     time.Duration // between polls
	KeepPrevious int           // how many old deploy tarballs to keep
}

// DefaultOptions fills in sensible defaults.
func DefaultOptions(app, env, region string) Options {
	return Options{App: app, Env: env, Region: region, Interval: 15 * time.Second, KeepPrevious: 3}
}

// phaseState is the watcher's current deploy-phase report. The
// pollAndApply path mutates it directly; status writers read it under
// the same goroutine (single-writer, no mutex needed). Empty Phase
// means "watcher is idle, no deploy in flight".
type phaseState struct {
	Phase string
	Since time.Time
}

// Run starts the watch loop. Blocks until ctx is cancelled.
func Run(ctx context.Context, opts Options) error {
	if opts.Interval == 0 {
		opts.Interval = 15 * time.Second
	}
	if opts.KeepPrevious <= 0 {
		opts.KeepPrevious = 3
	}
	// The watcher runs under systemd; journald captures stderr. Stamp
	// ui=watch so every log line carries the surface for filtering.
	ctx = trace.WithUI(ctx, "watch")
	log := trace.FromContext(ctx)
	baseDir := filepath.Join(lightsail.BaseDir, opts.App, opts.Env)
	bucket, err := readBucket(baseDir)
	if err != nil {
		log.ErrorContext(ctx, "watcher read bucket failed",
			slog.String("app", opts.App), slog.String("env", opts.Env),
			slog.Any("err", err))
		return err
	}
	log.InfoContext(ctx, "watcher starting",
		slog.String("app", opts.App), slog.String("env", opts.Env),
		slog.String("bucket", bucket),
		slog.String("prefix", lightsail.DeployPrefix),
		slog.String("region", opts.Region),
		slog.Duration("interval", opts.Interval))

	// Route the AWS SDK's own logs through our slog pipeline so they
	// surface alongside watcher events in journalctl.
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(opts.Region),
		config.WithLogger(logging.AWSLogger(log)),
	)
	if err != nil {
		return fmt.Errorf("load AWS config (IMDS): %w", err)
	}
	svc := s3.NewFromConfig(cfg)

	instance := instanceName(baseDir)
	currentDir := filepath.Join(baseDir, "current")
	stateFile := filepath.Join(baseDir, ".last-deploy")

	_ = os.MkdirAll(baseDir, 0o755)
	lastKey, _ := readLine(stateFile)

	if lastKey != "" {
		log.InfoContext(ctx, "resuming from previous deployment",
			slog.String("last_key", lastKey))
	} else {
		log.InfoContext(ctx, "no previous deployment found, starting fresh")
	}

	// Phase tracker. The poll closure below mutates it as the deploy
	// progresses; the status writer reads it on every push. Both run
	// on the same goroutine, so no mutex is needed.
	phase := &phaseState{}

	// Initial status upload so `app status` shows something immediately.
	writeStatus(ctx, log, svc, bucket, opts.Region, instance, lastKey, currentDir, phase)

	ticker := time.NewTicker(opts.Interval)
	defer ticker.Stop()
	var lastStatusBody string
	var lastStatusAt time.Time

	log.InfoContext(ctx, "watcher ready, entering poll loop",
		slog.String("instance", instance),
		slog.String("last_deploy", lastKey))

	// lastPoll tracks the outcome of the most recent poll so we can log
	// transitions at Info level and repeats at Trace. Without this, an
	// empty bucket (the common "waiting for first deploy" case) would
	// be completely silent.
	var lastPoll pollOutcome

	// poll performs one bucket scan and, if a new asset is present,
	// applies it. It mutates lastKey on success and updates lastPoll.
	poll := func() {
		latest, allKeys, lerr := findLatest(ctx, svc, bucket)
		switch {
		case lerr != nil:
			if lastPoll != pollListFailed {
				log.WarnContext(ctx, "poll: failed to list deploy assets",
					slog.String("bucket", bucket),
					slog.String("prefix", lightsail.DeployPrefix),
					slog.Any("err", lerr))
				lastPoll = pollListFailed
			} else {
				trace.Trace(ctx, "poll: list still failing",
					slog.Any("err", lerr))
			}
			return
		case latest == "":
			if lastPoll != pollBucketEmpty {
				log.InfoContext(ctx, "poll: bucket has no deploy assets yet — waiting for first deploy",
					slog.String("bucket", bucket),
					slog.String("prefix", lightsail.DeployPrefix))
				lastPoll = pollBucketEmpty
			} else {
				trace.Trace(ctx, "poll: bucket still empty")
			}
			return
		case latest == lastKey:
			if lastPoll != pollNoChange {
				log.InfoContext(ctx, "poll: no new deploy assets (latest already applied)",
					slog.String("latest_key", latest),
					slog.Int("total_assets", len(allKeys)))
				lastPoll = pollNoChange
			} else {
				trace.Trace(ctx, "poll: no change",
					slog.String("latest_key", latest))
			}
			return
		}

		log.InfoContext(ctx, "new deployment asset found",
			slog.String("key", latest),
			slog.String("previous", lastKey),
			slog.Int("total_assets", len(allKeys)))
		start := time.Now()
		// Closure for pullAndApply to publish phase transitions
		// without copy-pasting all the state needed to write
		// status.json. setPhase + put together; failures logged
		// at warn so a transient S3 hiccup mid-deploy doesn't
		// abort the build path.
		pushPhase := func(name string) {
			setPhase(phase, name)
			body := statusJSON(instance, bucket, opts.Region, latest, currentDir, phase)
			if err := put(ctx, svc, bucket, instance+lightsail.StatusSuffix, []byte(body), "application/json"); err != nil {
				log.WarnContext(ctx, "phase status push failed",
					slog.String("phase", name),
					slog.Any("err", err))
			}
		}
		if derr := pullAndApply(ctx, log, svc, bucket, latest, baseDir, currentDir, pushPhase); derr != nil {
			log.ErrorContext(ctx, "deployment failed",
				slog.String("key", latest),
				slog.Duration("elapsed", time.Since(start)),
				slog.Any("err", derr))
			pushPhase("failed")
			lastPoll = pollDeployFailed
			return
		}
		log.InfoContext(ctx, "deployment succeeded",
			slog.String("key", latest),
			slog.Duration("elapsed", time.Since(start)))
		setPhase(phase, "")
		lastKey = latest
		_ = os.WriteFile(stateFile, []byte(latest), 0o644)
		prune(ctx, svc, bucket, allKeys, opts.KeepPrevious)
		lastPoll = pollDeployed
	}

	// pushStatusIfChanged uploads status to S3 on content change or
	// once a minute as a heartbeat. Runs after every poll so container
	// state changes (crashes, restarts) propagate even without a new
	// deploy. The phase pointer is passed through so a phase-only
	// transition (e.g. extracting → building) shows up on the next
	// push without waiting for any other state change.
	pushStatusIfChanged := func() {
		body := statusJSON(instance, bucket, opts.Region, lastKey, currentDir, phase)
		if body == lastStatusBody && time.Since(lastStatusAt) <= time.Minute {
			return
		}
		if err := put(ctx, svc, bucket, instance+lightsail.StatusSuffix, []byte(body), "application/json"); err == nil {
			lastStatusBody = body
			lastStatusAt = time.Now()
		} else {
			log.WarnContext(ctx, "failed to upload status",
				slog.Any("err", err))
		}
	}

	// Do an immediate poll so operators see feedback without waiting a
	// full tick interval.
	poll()
	pushStatusIfChanged()

	for {
		select {
		case <-ctx.Done():
			log.InfoContext(ctx, "watcher shutting down")
			return nil
		case <-ticker.C:
		}
		poll()
		pushStatusIfChanged()
	}
}

// pollOutcome tracks the result of the most recent poll. Used to log
// transitions at Info level while demoting repeats to Trace, so a
// steady-state "empty bucket" watcher doesn't flood the log.
type pollOutcome int

const (
	pollStartup pollOutcome = iota
	pollBucketEmpty
	pollNoChange
	pollListFailed
	pollDeployed
	pollDeployFailed
)

// readBucket returns the bucket name written to /opt/lightsail/<app>/<env>/.bucket
// by `app local install`.
func readBucket(baseDir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(baseDir, ".bucket"))
	if err != nil {
		return "", fmt.Errorf("read .bucket: %w (run `app local install` first)", err)
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", fmt.Errorf(".bucket is empty")
	}
	return s, nil
}

func readLine(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func findLatest(ctx context.Context, svc *s3.Client, bucket string) (latest string, all []string, err error) {
	prefix := lightsail.DeployPrefix
	out, err := svc.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket), Prefix: aws.String(prefix),
	})
	if err != nil || len(out.Contents) == 0 {
		return "", nil, err
	}
	keys := make([]string, 0, len(out.Contents))
	for _, o := range out.Contents {
		keys = append(keys, aws.ToString(o.Key))
	}
	// Timestamp-prefixed names sort chronologically.
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	return keys[0], keys, nil
}

// setPhase records a phase transition. Empty string clears the
// phase (watcher idle). Caller is responsible for pushing the
// status afterwards if it wants the change to reach `app status`
// before the next minute heartbeat — pullAndApply does so via the
// pushPhase closure.
func setPhase(p *phaseState, phase string) {
	if p == nil {
		return
	}
	if p.Phase == phase {
		return
	}
	p.Phase = phase
	p.Since = time.Now().UTC()
}

// pushPhase is the callback type pullAndApply uses to publish
// phase transitions during a deploy. The closure passed in by Run
// captures region/instance/lastKey/currentDir/svc/bucket so this
// signature stays small.
type pushPhaseFunc func(name string)

// pullAndApply orchestrates a single deploy. It's split into three
// named phases so the agent never takes the live stack down before
// the new one is ready:
//
//  1. downloadAndExtract — pulls the tarball from S3 into a fresh
//     staging dir. If this fails the existing `current/` is
//     untouched. Phase: downloading → extracting.
//  2. buildAndStage — strategy-specific. For compose, this is just
//     detection (compose's own build runs inline during `up
//     --build` in step 3). For Dockerfile/buildpack (Tasks 6/7) it
//     runs `docker build` / `pack build` and synthesizes a compose
//     file inside staging. Failures here also leave `current/`
//     intact. Phase: detecting → building.
//  3. swap — only now does the previous stack go down. Renames
//     staging → current and runs `compose up`. The brief
//     port-bind window between down and up is unavoidable without
//     a reverse proxy. Phase: starting.
//
// The strategy returned by buildAndStage is consumed by swap so the
// two callers don't run Detect twice on the same tree.
func pullAndApply(ctx context.Context, log *slog.Logger, svc *s3.Client, bucket, key, baseDir, currentDir string, pushPhase pushPhaseFunc) error {
	staging := filepath.Join(baseDir, "staging")
	if err := downloadAndExtract(ctx, log, svc, bucket, key, baseDir, staging, pushPhase); err != nil {
		return err
	}
	strategy, err := buildAndStage(ctx, log, staging, pushPhase)
	if err != nil {
		return err
	}
	return swap(ctx, log, staging, currentDir, strategy, pushPhase)
}

// downloadAndExtract is phase 1: pull + unpack into a fresh
// staging dir. The previous deploy's `current/` is unaffected.
func downloadAndExtract(ctx context.Context, log *slog.Logger, svc *s3.Client, bucket, key, baseDir, staging string, pushPhase pushPhaseFunc) error {
	pushPhase("downloading")
	log.InfoContext(ctx, "downloading deploy asset",
		slog.String("bucket", bucket), slog.String("key", key))
	dlStart := time.Now()
	out, err := svc.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("s3 download: %w", err)
	}
	defer func() { _ = out.Body.Close() }()

	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}
	tmp := filepath.Join(baseDir, "deploy.tar.gz")
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	n, err := io.Copy(f, out.Body)
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("write tarball: %w", err)
	}
	_ = f.Close()
	defer func() { _ = os.Remove(tmp) }()
	log.InfoContext(ctx, "download complete",
		slog.Int64("bytes", n),
		slog.Duration("elapsed", time.Since(dlStart)))

	pushPhase("extracting")
	log.InfoContext(ctx, "extracting tarball", slog.String("dest", staging))
	if err := runCmd(ctx, log, "", "tar", "xzf", tmp, "-C", staging); err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	if entries, rerr := os.ReadDir(staging); rerr == nil {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		log.InfoContext(ctx, "extracted files",
			slog.Int("count", len(names)),
			slog.String("files", strings.Join(names, ", ")))
	}
	return nil
}

// buildAndStage is phase 2: strategy detection + (for non-compose
// strategies) the actual `docker build` / `pack build`. For compose
// it's a near-no-op since `compose up --build` does the work
// inline. Until Tasks 6/7 land, non-compose strategies fail fast
// here so the live stack is preserved.
func buildAndStage(ctx context.Context, log *slog.Logger, staging string, pushPhase pushPhaseFunc) (build.Strategy, error) {
	pushPhase("detecting")
	strategy, reason, derr := build.Detect(staging)
	if derr != nil {
		return build.StrategyUnknown, fmt.Errorf("detect strategy: %w", derr)
	}
	log.InfoContext(ctx, "detected build strategy",
		slog.String("strategy", strategy.String()),
		slog.String("reason", reason))
	switch strategy {
	case build.StrategyCompose:
		// compose's own up --build runs the build inline during swap.
		return strategy, nil
	default:
		return strategy, fmt.Errorf(
			"build strategy %q (%s) is recognized but not yet implemented in the agent — "+
				"this watcher only supports compose deploys for now",
			strategy.String(), reason)
	}
}

// swap is phase 3: stop the old stack, atomically replace
// `current/` with the new staging tree, and start the new stack.
// This is the only phase that takes the live service down, and it
// only runs after build succeeds.
func swap(ctx context.Context, log *slog.Logger, staging, currentDir string, strategy build.Strategy, pushPhase pushPhaseFunc) error {
	pushPhase("starting")

	// Stop the old deployment AFTER staging is ready and the new
	// build has succeeded. Failures earlier in the pipeline now
	// leave the old stack running.
	if cf := findCompose(currentDir); cf != "" {
		log.InfoContext(ctx, "stopping previous deployment",
			slog.String("compose_file", cf))
		if err := runCmd(ctx, log, currentDir, "docker", "compose", "-f", cf, "down"); err != nil {
			log.WarnContext(ctx, "compose down failed (continuing)",
				slog.Any("err", err))
		}
	} else {
		log.InfoContext(ctx, "no previous deployment to stop")
	}

	_ = os.RemoveAll(currentDir)
	if err := os.Rename(staging, currentDir); err != nil {
		return fmt.Errorf("swap staging to current: %w", err)
	}
	log.InfoContext(ctx, "swapped staging to current",
		slog.String("path", currentDir))

	cf := findCompose(currentDir)
	if cf == "" {
		return fmt.Errorf("no compose file found in deployed asset (looked for docker-compose.yml, compose.yml, etc.)")
	}
	// `up --build` is right for compose (build inline); for
	// Dockerfile/buildpack the image is already built and tagged
	// so plain `up -d` is enough. Strategy distinguishes them.
	upArgs := []string{"compose", "-f", cf, "up", "-d"}
	if strategy == build.StrategyCompose {
		upArgs = []string{"compose", "-f", cf, "up", "--build", "-d"}
	}
	log.InfoContext(ctx, "starting new deployment",
		slog.String("compose_file", cf),
		slog.String("working_dir", currentDir))
	if err := runCmd(ctx, log, currentDir, "docker", upArgs...); err != nil {
		return fmt.Errorf("compose up: %w", err)
	}
	log.InfoContext(ctx, "compose up completed, checking container health")

	containers, endpoints := containerInfo(currentDir)
	for _, c := range containers {
		log.InfoContext(ctx, "container status",
			slog.String("name", c.Name),
			slog.String("image", c.Image),
			slog.String("status", c.Status))
	}
	if len(endpoints) > 0 {
		log.InfoContext(ctx, "endpoints available",
			slog.String("endpoints", strings.Join(endpoints, ", ")))
	}
	if len(containers) == 0 {
		log.WarnContext(ctx, "no containers running after compose up")
	}
	return nil
}

func prune(ctx context.Context, svc *s3.Client, bucket string, all []string, keep int) {
	if len(all) <= keep {
		return
	}
	log := trace.FromContext(ctx)
	var ids []s3types.ObjectIdentifier
	for _, k := range all[keep:] {
		k := k
		ids = append(ids, s3types.ObjectIdentifier{Key: aws.String(k)})
	}
	log.InfoContext(ctx, "pruning old deploy assets",
		slog.Int("removing", len(ids)),
		slog.Int("keeping", keep))
	_, _ = svc.DeleteObjects(ctx, &s3.DeleteObjectsInput{
		Bucket: aws.String(bucket), Delete: &s3types.Delete{Objects: ids},
	})
}

func writeStatus(ctx context.Context, log *slog.Logger, svc *s3.Client, bucket, region, instance, lastKey, currentDir string, phase *phaseState) {
	body := statusJSON(instance, bucket, region, lastKey, currentDir, phase)
	if err := put(ctx, svc, bucket, instance+lightsail.StatusSuffix, []byte(body), "application/json"); err != nil {
		log.WarnContext(ctx, "initial status upload failed",
			slog.Any("err", err))
	} else {
		log.InfoContext(ctx, "initial status uploaded")
	}
}

// deployTimeFromKey extracts the unix timestamp from a deploy asset key
// (deploy/<unix>-<sha>.tar.gz). Falls back to time.Now() if unparseable.
func deployTimeFromKey(key string) time.Time {
	// Strip "deploy/" prefix.
	name := strings.TrimPrefix(key, "deploy/")
	// Split on "-" to get the unix timestamp before the sha.
	if i := strings.Index(name, "-"); i > 0 {
		if ts, err := strconv.ParseInt(name[:i], 10, 64); err == nil {
			return time.Unix(ts, 0).UTC()
		}
	}
	return time.Now().UTC()
}

func statusJSON(instance, bucket, region, lastKey, currentDir string, phase *phaseState) string {
	st := lightsail.Status{
		Instance:  instance,
		Timestamp: time.Now().UTC(),
		Status:    "idle",
	}
	if phase != nil && phase.Phase != "" {
		st.Phase = phase.Phase
		since := phase.Since
		st.PhaseSince = &since
	}
	if lastKey != "" {
		st.LastDeploy = &lightsail.DeployInfo{
			Timestamp: deployTimeFromKey(lastKey),
			ObjectURL: fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", bucket, region, lastKey),
		}
	}
	st.Containers, st.Endpoints = containerInfo(currentDir)
	running, total := 0, len(st.Containers)
	for _, c := range st.Containers {
		if c.Status == "running" {
			running++
		}
	}
	switch {
	case total == 0:
		st.Status = "idle"
	case running == total:
		st.Status = "healthy"
	case running == 0:
		st.Status = "down"
	default:
		st.Status = "degraded"
	}
	b, _ := json.Marshal(st)
	return string(b)
}

type composePS struct {
	Name       string `json:"Name"`
	Service    string `json:"Service"`
	Image      string `json:"Image"`
	State      string `json:"State"`
	CreatedAt  string `json:"CreatedAt"`
	Publishers []struct {
		PublishedPort int `json:"PublishedPort"`
	} `json:"Publishers"`
}

func containerInfo(currentDir string) ([]lightsail.ContainerStatus, []string) {
	cf := findCompose(currentDir)
	if cf == "" {
		return nil, nil
	}
	cmd := exec.Command("docker", "compose", "-f", cf, "ps", "--format", "json")
	cmd.Dir = currentDir
	out, err := cmd.Output()
	if err != nil {
		return nil, nil
	}
	publicIP := imdsPublicIP()
	var containers []lightsail.ContainerStatus
	var endpoints []string
	seen := map[int]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var e composePS
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		var started time.Time
		if t, err := time.Parse("2006-01-02 15:04:05 -0700 MST", e.CreatedAt); err == nil {
			started = t.UTC()
		}

		// Per-container endpoints: walk Publishers, dedupe by port,
		// format http URL. Keep a union in endpoints (top-level) for
		// callers that just want a flat list.
		var cEndpoints []string
		cSeen := map[int]bool{}
		for _, p := range e.Publishers {
			if p.PublishedPort <= 0 || cSeen[p.PublishedPort] {
				continue
			}
			cSeen[p.PublishedPort] = true
			if publicIP != "" {
				url := fmt.Sprintf("http://%s:%d", publicIP, p.PublishedPort)
				cEndpoints = append(cEndpoints, url)
				if !seen[p.PublishedPort] {
					seen[p.PublishedPort] = true
					endpoints = append(endpoints, url)
				}
			}
		}

		containers = append(containers, lightsail.ContainerStatus{
			Name:      e.Name,
			Service:   e.Service,
			Image:     e.Image,
			Status:    e.State,
			StartedAt: started,
			Endpoints: cEndpoints,
		})
	}
	return containers, endpoints
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

func runCmd(ctx context.Context, log *slog.Logger, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	err := cmd.Run()
	output := strings.TrimSpace(combined.String())
	if output != "" {
		// Log last 50 lines max to avoid flooding.
		lines := strings.Split(output, "\n")
		if len(lines) > 50 {
			lines = lines[len(lines)-50:]
		}
		level := slog.LevelInfo
		if err != nil {
			level = slog.LevelError
		}
		log.Log(ctx, level, "exec: "+name,
			slog.String("args", strings.Join(args, " ")),
			slog.String("output", strings.Join(lines, "\n")))
	}
	if err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func put(ctx context.Context, svc *s3.Client, bucket, key string, body []byte, ct string) error {
	_, err := svc.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
		Body: bytes.NewReader(body), ContentType: aws.String(ct),
	})
	return err
}

// instanceName reads the .instance file written by `app local install`, or
// falls back to IMDS tag lookup, or hostname.
func instanceName(baseDir string) string {
	if b, err := os.ReadFile(filepath.Join(baseDir, ".instance")); err == nil {
		if n := strings.TrimSpace(string(b)); n != "" {
			return n
		}
	}
	if n := imdsInstanceName(); n != "" {
		return n
	}
	h, _ := os.Hostname()
	return h
}

// imdsInstanceName fetches the instance name via IMDSv2. Empty on failure.
func imdsInstanceName() string {
	return imdsGet("/latest/meta-data/tags/instance/Name")
}

func imdsPublicIP() string {
	return imdsGet("/latest/meta-data/public-ipv4")
}

func imdsGet(path string) string {
	client := &http.Client{Timeout: 2 * time.Second}
	tok, _ := http.NewRequest("PUT", "http://169.254.169.254/latest/api/token", nil)
	tok.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "30")
	tr, err := client.Do(tok)
	if err != nil {
		return ""
	}
	defer func() { _ = tr.Body.Close() }()
	tb, _ := io.ReadAll(tr.Body)
	req, _ := http.NewRequest("GET", "http://169.254.169.254"+path, nil)
	req.Header.Set("X-aws-ec2-metadata-token", string(tb))
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return strings.TrimSpace(string(data))
}

// LocalStatus generates a fresh status snapshot for an app/env without
// uploading to S3. Used by `app local status` to print current state.
func LocalStatus(instance, bucket, lastKey, currentDir string) lightsail.Status {
	st := lightsail.Status{
		Instance:  instance,
		Timestamp: time.Now().UTC(),
		Status:    "idle",
	}
	if lastKey != "" {
		st.LastDeploy = &lightsail.DeployInfo{
			Timestamp: deployTimeFromKey(lastKey),
		}
		if bucket != "" {
			st.LastDeploy.ObjectURL = fmt.Sprintf("s3://%s/%s", bucket, lastKey)
		}
	}
	st.Containers, st.Endpoints = containerInfo(currentDir)
	running, total := 0, len(st.Containers)
	for _, c := range st.Containers {
		if c.Status == "running" {
			running++
		}
	}
	switch {
	case total == 0:
		st.Status = "idle"
	case running == total:
		st.Status = "healthy"
	case running == 0:
		st.Status = "down"
	default:
		st.Status = "degraded"
	}
	return st
}
