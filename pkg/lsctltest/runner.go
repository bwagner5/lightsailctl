// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package lsctltest

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/lightsailctl/pkg/lsctltest/testkit"
)

// Run executes all registered tests.
//
// Flow:
//  1. Print the plan (resolved config + test names) up front.
//  2. Execute each test with a fresh T so steps remain per-test.
//     Tests are responsible for their own setup and cleanup.
//  3. Print summary; return non-nil if any test failed.
func Run(ctx context.Context, env testkit.Env, rep testkit.Reporter) error {
	tests := testkit.All()
	if len(tests) == 0 {
		return fmt.Errorf("no tests registered")
	}

	rep.Plan(env, tests)

	var results []testkit.Result
	for _, rt := range tests {
		results = append(results, runOne(ctx, env, rep, rt))
	}
	rep.Summary(results)

	for _, r := range results {
		if !r.Passed {
			return fmt.Errorf("tests failed")
		}
	}
	return nil
}

// runOne executes a single registered test, catching a testkit.TestFailure
// panic used by Fatalf to unwind.
func runOne(ctx context.Context, env testkit.Env, rep testkit.Reporter, rt testkit.RegisteredTest) testkit.Result {
	rep.TestStart(rt.Name)
	t := testkit.NewT(ctx, env, rep)
	start := time.Now()

	func() {
		defer func() {
			if v := recover(); v != nil {
				if _, ok := v.(testkit.TestFailure); !ok {
					panic(v)
				}
			}
		}()
		rt.Run(t)
	}()

	r := testkit.Result{
		Name:    rt.Name,
		Steps:   t.Steps(),
		Passed:  !t.Failed(),
		FailMsg: t.FailMsg(),
		Elapsed: time.Since(start),
	}
	rep.TestDone(r)
	return r
}
