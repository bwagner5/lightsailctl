// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package logging

import (
	"context"
	"log/slog"
	"regexp"
)

// sensitiveKeyPattern matches attribute keys whose values must be
// redacted. This is defense-in-depth: the primary guard is the
// sensitive-surfaces list in the logging plan (§12). If engineers
// route a sensitive value through a key name that doesn't match here,
// the redactor won't save them.
//
// The handler only inspects TOP-LEVEL keys. slog.Group is NOT recursed
// into: engineers must keep sensitive values at the top level with a
// matching key name, or use redact.* helpers to stringify first.
var sensitiveKeyPattern = regexp.MustCompile(
	`(?i)(password|passwd|secret|token|authorization|x-amz-signature|` +
		`private[_-]?key|access[_-]?key(_id)?|client[_-]?secret|` +
		`bearer|oauth|session[_-]?token|api[_-]?key|user[_-]?data)`)

// redactingHandler wraps a slog.Handler. Every top-level Attr it
// receives is checked against sensitiveKeyPattern; matches get their
// value replaced with "<redacted len=N>" before being forwarded.
type redactingHandler struct {
	inner slog.Handler
}

func newRedactingHandler(inner slog.Handler) *redactingHandler {
	return &redactingHandler{inner: inner}
}

// Enabled delegates to the inner handler so level threshold checks
// flow through without modification.
func (h *redactingHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

// Handle redacts top-level attrs on r then forwards to the inner.
func (h *redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	// Build a new record so we don't mutate the caller's copy.
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, out)
}

// WithAttrs redacts the supplied attrs, then forwards to the inner.
// Base attrs (from logger.With) flow through this path.
func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		redacted = append(redacted, redactAttr(a))
	}
	return &redactingHandler{inner: h.inner.WithAttrs(redacted)}
}

// WithGroup is opaque: the group name becomes a prefix on the inner
// handler but the group's contained attrs are not recursed into (the
// plan documents this explicitly).
func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{inner: h.inner.WithGroup(name)}
}

// redactAttr returns a (potentially replaced) Attr. Non-matching keys
// are returned unchanged.
func redactAttr(a slog.Attr) slog.Attr {
	if sensitiveKeyPattern.MatchString(a.Key) {
		v := a.Value.Resolve()
		// Preserve the length hint for correlation without leaking
		// the value. Non-string values fall back to a generic marker.
		if v.Kind() == slog.KindString {
			s := v.String()
			return slog.String(a.Key, redactedMarker(len(s)))
		}
		return slog.String(a.Key, "<redacted>")
	}
	return a
}

// redactedMarker returns "<redacted len=N>" without pulling in fmt
// (keeps the hot path allocation-cheap).
func redactedMarker(n int) string {
	var digits [20]byte
	i := len(digits)
	if n == 0 {
		i--
		digits[i] = '0'
	} else {
		for n > 0 {
			i--
			digits[i] = byte('0' + n%10)
			n /= 10
		}
	}
	return "<redacted len=" + string(digits[i:]) + ">"
}
