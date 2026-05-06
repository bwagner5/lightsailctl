package lightsail

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Status is the shape of <instance>_status.json in an env bucket. Produced by
// the on-instance watcher, consumed by `app status`.
type Status struct {
	Instance   string            `json:"instance"`
	Timestamp  time.Time         `json:"timestamp"`
	Status     string            `json:"status"` // healthy | degraded | down | idle
	LastDeploy *DeployInfo       `json:"last_deploy,omitempty"`
	Containers []ContainerStatus `json:"containers,omitempty"`
	Endpoints  []string          `json:"endpoints,omitempty"`
}

// DeployInfo describes the last-deployed asset.
type DeployInfo struct {
	Timestamp time.Time `json:"timestamp"`
	ObjectURL string    `json:"object_url"`
}

// ContainerStatus describes one running container inside the compose stack.
type ContainerStatus struct {
	Name string `json:"name"`
	// Service is the docker compose service name (e.g. "web" in a
	// services.web: block). Name is the concrete container (e.g.
	// "current-web-1"); Service is what the user wrote in compose.yml.
	Service   string    `json:"service,omitempty"`
	Image     string    `json:"image"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"started_at"`
	// Endpoints are the published http URLs that resolve to this
	// container's exposed ports. Status.Endpoints is the union across
	// all containers; this field lets consumers attribute an endpoint
	// back to the service that serves it.
	Endpoints []string `json:"endpoints,omitempty"`
}

// ReadBucketStatuses lists every *_status.json object in a bucket and parses
// them. Uses a fresh short-lived access key; returned errors are non-fatal
// (best-effort — an empty slice is normal when no watchers have started yet).
func (c *Client) ReadBucketStatuses(ctx context.Context, bucket string) ([]Status, error) {
	s3cli, cleanup, err := c.S3ClientFor(ctx, bucket)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	out, err := s3cli.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	if err != nil {
		return nil, err
	}
	var statuses []Status
	for _, obj := range out.Contents {
		key := aws.ToString(obj.Key)
		if !strings.HasSuffix(key, StatusSuffix) {
			continue
		}
		getOut, err := s3cli.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(bucket), Key: aws.String(key),
		})
		if err != nil {
			continue
		}
		data, rerr := io.ReadAll(getOut.Body)
		_ = getOut.Body.Close()
		if rerr != nil {
			continue
		}
		var st Status
		if json.Unmarshal(data, &st) == nil {
			statuses = append(statuses, st)
		}
	}
	return statuses, nil
}
