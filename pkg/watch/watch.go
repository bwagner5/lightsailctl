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
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

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

// Run starts the watch loop. Blocks until ctx is cancelled.
func Run(ctx context.Context, opts Options) error {
	if opts.Interval == 0 {
		opts.Interval = 15 * time.Second
	}
	if opts.KeepPrevious <= 0 {
		opts.KeepPrevious = 3
	}
	baseDir := filepath.Join(lightsail.BaseDir, opts.App, opts.Env)
	bucket, err := readBucket(baseDir)
	if err != nil {
		return err
	}
	log.Printf("watcher starting: app=%s env=%s bucket=%s interval=%s",
		opts.App, opts.Env, bucket, opts.Interval)

	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(opts.Region))
	if err != nil {
		return fmt.Errorf("load AWS config (IMDS): %w", err)
	}
	svc := s3.NewFromConfig(cfg)

	instance := instanceName(baseDir)
	currentDir := filepath.Join(baseDir, "current")
	stateFile := filepath.Join(baseDir, ".last-deploy")

	_ = os.MkdirAll(baseDir, 0o755)
	lastKey, _ := readLine(stateFile)

	// Initial status upload so `app status` shows something immediately.
	writeStatus(ctx, svc, bucket, opts.Region, instance, lastKey, currentDir)

	ticker := time.NewTicker(opts.Interval)
	defer ticker.Stop()
	var lastStatusBody string
	var lastStatusAt time.Time

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		latest, allKeys, lerr := findLatest(ctx, svc, bucket)
		if lerr != nil {
			log.Printf("list deploys: %v", lerr)
			continue
		}
		if latest != "" && latest != lastKey {
			log.Printf("new deploy: %s", latest)
			if derr := pullAndApply(ctx, svc, bucket, latest, baseDir, currentDir); derr != nil {
				log.Printf("apply %s: %v", latest, derr)
			} else {
				lastKey = latest
				_ = os.WriteFile(stateFile, []byte(latest), 0o644)
				prune(ctx, svc, bucket, allKeys, opts.KeepPrevious)
			}
		}

		// Status: publish on change or every minute.
		body := statusJSON(instance, bucket, opts.Region, lastKey, currentDir)
		if body != lastStatusBody || time.Since(lastStatusAt) > time.Minute {
			if err := put(ctx, svc, bucket, instance+lightsail.StatusSuffix, []byte(body), "application/json"); err == nil {
				lastStatusBody = body
				lastStatusAt = time.Now()
			}
		}
	}
}

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

func pullAndApply(ctx context.Context, svc *s3.Client, bucket, key, baseDir, currentDir string) error {
	out, err := svc.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
	})
	if err != nil {
		return err
	}
	defer func() { _ = out.Body.Close() }()

	staging := filepath.Join(baseDir, "staging")
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(baseDir, "deploy.tar.gz")
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, out.Body); err != nil {
		_ = f.Close()
		return err
	}
	_ = f.Close()
	defer func() { _ = os.Remove(tmp) }()

	if err := runCmd(ctx, "", "tar", "xzf", tmp, "-C", staging); err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	// Compose down on the old deployment, if any.
	if cf := findCompose(currentDir); cf != "" {
		_ = runCmd(ctx, currentDir, "docker", "compose", "-f", cf, "down")
	}
	_ = os.RemoveAll(currentDir)
	if err := os.Rename(staging, currentDir); err != nil {
		return fmt.Errorf("swap: %w", err)
	}
	cf := findCompose(currentDir)
	if cf == "" {
		return fmt.Errorf("no compose file in deployed asset")
	}
	return runCmd(ctx, currentDir, "docker", "compose", "-f", cf, "up", "--build", "-d")
}

func prune(ctx context.Context, svc *s3.Client, bucket string, all []string, keep int) {
	if len(all) <= keep {
		return
	}
	var ids []s3types.ObjectIdentifier
	for _, k := range all[keep:] {
		k := k
		ids = append(ids, s3types.ObjectIdentifier{Key: aws.String(k)})
	}
	_, _ = svc.DeleteObjects(ctx, &s3.DeleteObjectsInput{
		Bucket: aws.String(bucket), Delete: &s3types.Delete{Objects: ids},
	})
}

func writeStatus(ctx context.Context, svc *s3.Client, bucket, region, instance, lastKey, currentDir string) {
	body := statusJSON(instance, bucket, region, lastKey, currentDir)
	_ = put(ctx, svc, bucket, instance+lightsail.StatusSuffix, []byte(body), "application/json")
}

func statusJSON(instance, bucket, region, lastKey, currentDir string) string {
	st := lightsail.Status{
		Instance:  instance,
		Timestamp: time.Now().UTC(),
		Status:    "idle",
	}
	if lastKey != "" {
		st.LastDeploy = &lightsail.DeployInfo{
			Timestamp: time.Now().UTC(), // best-effort; actual asset ts embedded in key
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
		containers = append(containers, lightsail.ContainerStatus{
			Name: e.Name, Image: e.Image, Status: e.State, StartedAt: started,
		})
		if publicIP == "" {
			continue
		}
		for _, p := range e.Publishers {
			if p.PublishedPort > 0 && !seen[p.PublishedPort] {
				seen[p.PublishedPort] = true
				endpoints = append(endpoints, fmt.Sprintf("http://%s:%d", publicIP, p.PublishedPort))
			}
		}
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

func runCmd(ctx context.Context, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
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
