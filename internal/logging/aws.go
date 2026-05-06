// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package logging

import (
	"context"
	"fmt"
	"log/slog"

	smithylogging "github.com/aws/smithy-go/logging"
)

// AWSLogger adapts a *slog.Logger into the smithy-go logging.Logger
// the AWS SDK v2 expects. Every SDK message is emitted at slog DEBUG
// regardless of the smithy classification: smithy's logger is
// format-string only (not structured KV), so a finer split between
// smithy.Warn and smithy.Info buys nothing in a structured pipeline.
//
// Under the default INFO threshold, SDK logs are suppressed. Under
// --debug (which flips trace.LevelVar to LevelTrace) DEBUG is visible.
func AWSLogger(l *slog.Logger) smithylogging.Logger {
	if l == nil {
		l = slog.Default()
	}
	return awsLogger{l: l}
}

type awsLogger struct{ l *slog.Logger }

// Logf is the smithy-go logger entry point. The classification is
// carried as a source attr for filtering; the level is always DEBUG.
func (a awsLogger) Logf(class smithylogging.Classification, format string, v ...any) {
	a.l.LogAttrs(context.Background(), slog.LevelDebug, "aws sdk",
		slog.String("source", "aws-sdk"),
		slog.String("class", string(class)),
		slog.String("msg", fmt.Sprintf(format, v...)),
	)
}
