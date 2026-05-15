// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package lightsail

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Retryable runs fn up to 5 times with exponential back-off (200ms, 400ms,
// 800ms, 1.6s, 3.2s). Use it to paper over Lightsail's eventual consistency
// for bucket/access-key reads after creates. fn should return nil on success,
// a retryable error to retry, or StopRetry(err) to bail immediately.
func Retryable(ctx context.Context, fn func(context.Context) error) error {
	return retryFor(ctx, 5, 200*time.Millisecond, fn)
}

// RetryableLong is Retryable with a longer ceiling for operations on
// freshly-created buckets (access keys, S3 writes) where server-side
// propagation can take minutes. Caps at ~5 minutes total wall clock.
func RetryableLong(ctx context.Context, fn func(context.Context) error) error {
	return retryFor(ctx, 30, 2*time.Second, fn)
}

func retryFor(ctx context.Context, attempts int, initial time.Duration, fn func(context.Context) error) error {
	backoff := initial
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			if backoff < 15*time.Second {
				backoff *= 2
				if backoff > 15*time.Second {
					backoff = 15 * time.Second
				}
			}
		}
		err := fn(ctx)
		if err == nil {
			return nil
		}
		var s *stopErr
		if errors.As(err, &s) {
			return s.inner
		}
		lastErr = err
	}
	return fmt.Errorf("after retries: %w", lastErr)
}

// StopRetry wraps err so Retryable will bail out instead of retrying.
func StopRetry(err error) error {
	if err == nil {
		return nil
	}
	return &stopErr{inner: err}
}

type stopErr struct{ inner error }

func (s *stopErr) Error() string { return "stop: " + s.inner.Error() }
func (s *stopErr) Unwrap() error { return s.inner }
