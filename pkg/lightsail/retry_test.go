// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package lightsail

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetryableSucceedsEventually(t *testing.T) {
	attempts := 0
	err := Retryable(context.Background(), func(context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("not yet")
		}
		return nil
	})
	if err != nil || attempts != 3 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}
}

func TestRetryableStop(t *testing.T) {
	fatal := errors.New("permanent")
	attempts := 0
	err := Retryable(context.Background(), func(context.Context) error {
		attempts++
		return StopRetry(fatal)
	})
	if !errors.Is(err, fatal) {
		t.Errorf("want wraps fatal, got %v", err)
	}
	if attempts != 1 {
		t.Errorf("want 1 attempt, got %d", attempts)
	}
}

func TestRetryableCtxCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := Retryable(ctx, func(context.Context) error {
		return errors.New("fail")
	})
	if err == nil {
		t.Fatal("want cancellation error")
	}
}
