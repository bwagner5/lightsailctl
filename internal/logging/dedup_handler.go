// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package logging

import (
	"context"
	"log/slog"
)

// dedupHandler collapses repeated top-level attribute keys (last-wins)
// before forwarding to the inner handler.
//
// slog.Logger.With simply appends attributes; it does NOT deduplicate
// by key. Our logging chain stamps "ui" (and sometimes "cmd") at three
// layers — the binary's logging.Init, triad's PersistentPreRunE, and
// UI entry points like watch.Run — so without deduplication a single
// log line can carry ui=cli ui=interactive ui=watch. This handler
// makes the "later WithUI wins" contract documented in
// trace.WithUI/lightsailctl.main actually hold.
//
// Only TOP-LEVEL keys are deduplicated. Once WithGroup opens a
// namespace, dedup stops: keys inside a group are scoped to that group
// and collisions there are the caller's concern.
type dedupHandler struct {
	inner slog.Handler
	// attrs holds the accumulated base attrs in insertion order,
	// already deduplicated by key (last occurrence kept, in its
	// last-seen position). Nil until the first WithAttrs call.
	attrs []slog.Attr
	// grouped is set once WithGroup has been called. In that state
	// we stop tracking attrs locally and forward every call to inner
	// unchanged — we cannot safely dedup across group boundaries.
	grouped bool
}

func newDedupHandler(inner slog.Handler) *dedupHandler {
	return &dedupHandler{inner: inner}
}

// Enabled delegates to the inner handler so level threshold checks
// flow through without modification.
func (h *dedupHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

// WithAttrs merges attrs into h.attrs with last-wins dedup. Once
// grouped, we forward to inner unchanged.
func (h *dedupHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	if h.grouped {
		return &dedupHandler{inner: h.inner.WithAttrs(attrs), grouped: true}
	}
	return &dedupHandler{inner: h.inner, attrs: mergeLastWins(h.attrs, attrs)}
}

// WithGroup flushes the accumulated attrs to inner (so they appear at
// the top level, above the group) and then opens the group. Subsequent
// WithAttrs/Handle calls pass through unchanged.
func (h *dedupHandler) WithGroup(name string) slog.Handler {
	inner := h.inner
	if len(h.attrs) > 0 {
		inner = inner.WithAttrs(h.attrs)
	}
	return &dedupHandler{inner: inner.WithGroup(name), grouped: true}
}

// Handle merges base attrs with record attrs (last-wins) and emits
// the combined set to inner. Once grouped, passes through unchanged.
func (h *dedupHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.grouped {
		return h.inner.Handle(ctx, r)
	}
	if len(h.attrs) == 0 && r.NumAttrs() == 0 {
		return h.inner.Handle(ctx, r)
	}
	rec := make([]slog.Attr, 0, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		rec = append(rec, a)
		return true
	})
	merged := mergeLastWins(h.attrs, rec)
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	out.AddAttrs(merged...)
	return h.inner.Handle(ctx, out)
}

// mergeLastWins returns base ++ extra with duplicate keys collapsed to
// the last occurrence, preserving the position of that last
// occurrence. Empty keys are kept as-is (not deduped) — slog allows
// them and treating them as distinct is safer than collapsing unrelated
// attrs that happen to have no name.
func mergeLastWins(base, extra []slog.Attr) []slog.Attr {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	all := make([]slog.Attr, 0, len(base)+len(extra))
	all = append(all, base...)
	all = append(all, extra...)

	// Walk backward so the LAST occurrence of each key is the one we
	// keep. Empty keys short-circuit the dedup check.
	seen := make(map[string]struct{}, len(all))
	kept := make([]slog.Attr, 0, len(all))
	for i := len(all) - 1; i >= 0; i-- {
		k := all[i].Key
		if k != "" {
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
		}
		kept = append(kept, all[i])
	}
	// Reverse kept back to original order.
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	return kept
}
