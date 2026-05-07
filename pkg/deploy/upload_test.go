package deploy

import (
	"testing"
	"time"

	"github.com/aws/lightsailctl/pkg/lightsail"
)

// TestAllHealthy_MatrixOfStates exercises every shape the watcher can
// produce around a deploy. Each case documents the signal allHealthy
// must respond to so future changes don't regress the "treat idle as
// healthy on timeout" bug.
func TestAllHealthy_MatrixOfStates(t *testing.T) {
	asset := "deploy/1700000000-abc123.tar.gz"
	url := "https://ls--123--app--dev.s3.us-east-1.amazonaws.com/" + asset
	oldURL := "https://ls--123--app--dev.s3.us-east-1.amazonaws.com/deploy/1600000000-zzz.tar.gz"
	since := time.Unix(1700000000, 0)
	fresh := since.Add(1 * time.Second)
	stale := since.Add(-1 * time.Second)

	healthyReport := lightsail.Status{
		Instance:   "box-a",
		Timestamp:  fresh,
		Status:     "healthy",
		LastDeploy: &lightsail.DeployInfo{Timestamp: fresh, ObjectURL: url},
		Containers: []lightsail.ContainerStatus{{Name: "web", Status: "running"}},
	}

	cases := []struct {
		name     string
		statuses []lightsail.Status
		want     bool
	}{
		{
			name:     "empty slice (no watcher reports yet)",
			statuses: nil,
			want:     false,
		},
		{
			name:     "happy path: one healthy report for our asset",
			statuses: []lightsail.Status{healthyReport},
			want:     true,
		},
		{
			name: "idle rollup with zero containers (bootstrapping)",
			statuses: []lightsail.Status{{
				Instance:   "box-a",
				Timestamp:  fresh,
				Status:     "idle",
				LastDeploy: &lightsail.DeployInfo{Timestamp: fresh, ObjectURL: url},
			}},
			want: false,
		},
		{
			name: "degraded rollup (1/2 running)",
			statuses: []lightsail.Status{{
				Instance:   "box-a",
				Timestamp:  fresh,
				Status:     "degraded",
				LastDeploy: &lightsail.DeployInfo{Timestamp: fresh, ObjectURL: url},
				Containers: []lightsail.ContainerStatus{
					{Name: "web", Status: "running"},
					{Name: "db", Status: "exited"},
				},
			}},
			want: false,
		},
		{
			name: "stale timestamp (pre-deploy)",
			statuses: []lightsail.Status{{
				Instance:   "box-a",
				Timestamp:  stale,
				Status:     "healthy",
				LastDeploy: &lightsail.DeployInfo{Timestamp: fresh, ObjectURL: url},
				Containers: []lightsail.ContainerStatus{{Name: "web", Status: "running"}},
			}},
			want: false,
		},
		{
			name: "fresh timestamp but LastDeploy points at previous asset",
			statuses: []lightsail.Status{{
				Instance:   "box-a",
				Timestamp:  fresh,
				Status:     "healthy",
				LastDeploy: &lightsail.DeployInfo{Timestamp: fresh, ObjectURL: oldURL},
				Containers: []lightsail.ContainerStatus{{Name: "web", Status: "running"}},
			}},
			want: false,
		},
		{
			name: "fresh timestamp but LastDeploy nil (watcher hasn't applied anything yet)",
			statuses: []lightsail.Status{{
				Instance:   "box-a",
				Timestamp:  fresh,
				Status:     "healthy",
				Containers: []lightsail.ContainerStatus{{Name: "web", Status: "running"}},
			}},
			want: false,
		},
		{
			name: "rollup says healthy but a container is still 'created'",
			statuses: []lightsail.Status{{
				Instance:   "box-a",
				Timestamp:  fresh,
				Status:     "healthy",
				LastDeploy: &lightsail.DeployInfo{Timestamp: fresh, ObjectURL: url},
				Containers: []lightsail.ContainerStatus{
					{Name: "web", Status: "running"},
					{Name: "db", Status: "created"},
				},
			}},
			want: false,
		},
		{
			name: "multi-instance: all healthy",
			statuses: []lightsail.Status{
				healthyReport,
				{
					Instance:   "box-b",
					Timestamp:  fresh,
					Status:     "healthy",
					LastDeploy: &lightsail.DeployInfo{Timestamp: fresh, ObjectURL: url},
					Containers: []lightsail.ContainerStatus{{Name: "web", Status: "running"}},
				},
			},
			want: true,
		},
		{
			name: "multi-instance: one still idle",
			statuses: []lightsail.Status{
				healthyReport,
				{
					Instance:   "box-b",
					Timestamp:  fresh,
					Status:     "idle",
					LastDeploy: &lightsail.DeployInfo{Timestamp: fresh, ObjectURL: url},
				},
			},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := allHealthy(tc.statuses, asset, since)
			if got != tc.want {
				t.Errorf("allHealthy = %v; want %v", got, tc.want)
			}
		})
	}
}
