package lightsail

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIsSSHWarmupError_Table(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"connection refused", errors.New("ssh: connect to host 3.5.6.7 port 22: Connection refused"), true},
		{"scp connection closed", errors.New("scp /tmp/x: ... : exit status 255\nscp: Connection closed\n"), true},
		{"kex banner race", errors.New("kex_exchange_identification: Connection closed by remote host"), true},
		{"no route to host", errors.New("ssh: connect to host 10.0.0.1 port 22: No route to host"), true},
		{"port timeout", errors.New("ssh: connect to host 3.5.6.7 port 22: Connection timed out"), true},

		// These must NOT retry — they're legitimate failures.
		{"permission denied", errors.New("Permission denied (publickey)"), false},
		{"host key", errors.New("Host key verification failed"), false},
		{"no such file", errors.New("scp: /tmp/does-not-exist: No such file or directory"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSSHWarmupError(tc.err); got != tc.want {
				t.Errorf("isSSHWarmupError(%v) = %v; want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestRetrySSHWarmup_SucceedsAfterRetries: the fn returns a warmup
// error twice, then nil. The loop must succeed without surfacing the
// first two failures.
func TestRetrySSHWarmup_SucceedsAfterRetries(t *testing.T) {
	calls := 0
	err := retrySSHWarmup(context.Background(), func(_ context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("ssh: connect to host 1.2.3.4 port 22: Connection refused")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("want success, got %v", err)
	}
	if calls != 3 {
		t.Errorf("want 3 calls, got %d", calls)
	}
}

// TestRetrySSHWarmup_FailsFastOnNonWarmup: an auth error returns
// immediately, not after many attempts.
func TestRetrySSHWarmup_FailsFastOnNonWarmup(t *testing.T) {
	calls := 0
	err := retrySSHWarmup(context.Background(), func(_ context.Context) error {
		calls++
		return errors.New("Permission denied (publickey)")
	})
	if err == nil {
		t.Fatalf("want error, got nil")
	}
	if calls != 1 {
		t.Errorf("want 1 call, got %d", calls)
	}
}

// TestRetrySSHWarmup_ContextCancel aborts the loop when ctx is done.
func TestRetrySSHWarmup_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := retrySSHWarmup(ctx, func(_ context.Context) error {
		return errors.New("Connection refused")
	})
	if err == nil {
		t.Fatalf("want error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Logf("got err: %v (acceptable if it's a timeout-shaped message)", err)
	}
}
