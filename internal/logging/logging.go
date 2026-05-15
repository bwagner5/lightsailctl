// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// Package logging owns lightsailctl's logging configuration.
//
// Triad is a library: it exposes vocabulary (LevelVar, LevelTrace,
// context helpers) but does no file handling. This package resolves
// paths, builds the handler chain, installs redaction, and runs
// retention. lightsailctl's main wires it in PersistentPreRunE.
//
// The handler chain is:
//
//	slog.Logger
//	  └── dedupHandler                  // collapses repeated top-level keys (last-wins)
//	        └── redactingHandler        // top-level key regex
//	              └── slog.NewTextHandler // text records to sink
//
// sink is one of: lazy file (file mode), os.Stderr (stderr mode), or
// io.Discard (none mode). The level comes from trace.LevelVar; flipping
// it at runtime takes effect immediately.
//
// The dedup layer is what makes trace.WithUI's "later call wins"
// contract actually hold. slog.Logger.With only appends attrs — it
// does not replace by key — so stamping "ui" at three layers
// (logging.Init → triad PersistentPreRunE → watch/tui Run) would
// otherwise emit ui=cli ui=interactive ui=watch on every line.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bwagner5/triad/pkg/trace"
)

// Dest selects where log records are written.
type Dest string

const (
	DestFile   Dest = "file"
	DestStderr Dest = "stderr"
	DestNone   Dest = "none"
)

// Options configures Init. Zero values are fine; each field documents
// its default.
type Options struct {
	// Dest is the sink type. Default (empty) is DestFile.
	Dest Dest
	// Path overrides the auto-resolved default when Dest == DestFile.
	// Empty means "auto-resolve under $HOME/.lightsailctl/logs/".
	// User-supplied paths are NOT retention-managed — the user owns
	// the filesystem lifecycle when they pick the path.
	Path string
	// UI is the initial ui attribute baked into the root logger. The
	// four UIs are "cli", "interactive", "tui", "watch"; entry points
	// refine this with trace.WithUI in their Run funcs.
	UI string
	// Attrs are additional base attributes (e.g. cli_version, pid).
	Attrs []slog.Attr
	// Stderr is the writer used for silent-downgrade warnings
	// ("log dir unwritable, falling back to $TMPDIR"). No warning is
	// emitted when Stderr is nil or UI == "tui" (TUI alt-screen would
	// be corrupted).
	Stderr io.Writer
	// Clock is injectable for retention tests. Defaults to time.Now.
	Clock func() time.Time
}

// currentPathMu guards currentPath, which Init stamps with the
// resolved file path (if any). main.go reads this when echoing
// "trace log: <path>" under --debug.
var (
	currentPathMu sync.RWMutex
	currentPath   string
)

// CurrentPath returns the resolved log-file path, or "" if the current
// sink isn't a file. main echoes this under --debug for bug-report
// ergonomics.
func CurrentPath() string {
	currentPathMu.RLock()
	defer currentPathMu.RUnlock()
	return currentPath
}

func setCurrentPath(p string) {
	currentPathMu.Lock()
	currentPath = p
	currentPathMu.Unlock()
}

// Init resolves the sink, builds the handler chain, installs
// redaction, wires trace.LevelVar as the Leveler, and calls
// slog.SetDefault. It returns the root logger, a shutdown func that
// flushes and closes any underlying file, and an error.
//
// Init is called once per process from PersistentPreRunE. Call
// shutdown from a deferred wrapper so panics still flush the file.
func Init(opts Options) (*slog.Logger, func() error, error) {
	if opts.Dest == "" {
		opts.Dest = DestFile
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}

	sink, shutdown, resolvedPath, err := resolveSink(opts)
	if err != nil {
		return nil, nil, err
	}
	setCurrentPath(resolvedPath)

	// The level comes from trace.LevelVar so --debug can flip it
	// atomically at runtime.
	text := slog.NewTextHandler(sink, &slog.HandlerOptions{Level: trace.LevelVar})
	handler := newDedupHandler(newRedactingHandler(text))

	logger := slog.New(handler)
	if opts.UI != "" {
		logger = logger.With(slog.String("ui", opts.UI))
	}
	if len(opts.Attrs) > 0 {
		args := make([]any, 0, len(opts.Attrs))
		for _, a := range opts.Attrs {
			args = append(args, a)
		}
		logger = logger.With(args...)
	}
	slog.SetDefault(logger)
	return logger, shutdown, nil
}

// resolveSink picks a writer based on opts.Dest. For files it sets up
// a lazy-open writer so --help / parse-fail paths don't leave empty
// files, runs retention on the logs dir, and warns to stderr on
// fallback. Returns (sink, shutdown, resolvedPath, err).
func resolveSink(opts Options) (io.Writer, func() error, string, error) {
	switch opts.Dest {
	case DestStderr:
		return os.Stderr, func() error { return nil }, "", nil
	case DestNone:
		return io.Discard, func() error { return nil }, "", nil
	case DestFile:
		// fall through
	default:
		return nil, nil, "", fmt.Errorf("unknown --log-dest: %q", opts.Dest)
	}

	// File destination: user-supplied path or auto-resolved.
	retention := true
	path := opts.Path
	if path == "" {
		p, dir, err := autoResolvePath()
		if err != nil {
			// fall back to $TMPDIR.
			tmpDir := filepath.Join(os.TempDir(), "lightsailctl-logs")
			if mkerr := os.MkdirAll(tmpDir, 0o700); mkerr != nil {
				warn(opts, "logging: unwritable log dir, discarding")
				return io.Discard, func() error { return nil }, "", nil
			}
			warn(opts, "logging: $HOME unwritable, using %s", tmpDir)
			path = filepath.Join(tmpDir, autoFileName())
			retention = false
		} else {
			path = p
			_ = dir // retained for symmetry; used by pruning below
		}
	} else {
		// User-supplied: respect retention=false.
		retention = false
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			warn(opts, "logging: mkdir %s: %v (discarding)", filepath.Dir(path), err)
			return io.Discard, func() error { return nil }, "", nil
		}
	}

	if retention {
		// Run retention on the auto-resolved directory. Errors are
		// best-effort and do not fail startup.
		_ = pruneLogs(filepath.Dir(path), opts.Clock, 14*24*time.Hour, 100)
	}

	lw := newLazyFile(path)
	shutdown := func() error { return lw.Close() }
	return lw, shutdown, path, nil
}

// warn writes to opts.Stderr unless the caller is a TUI (where stderr
// corrupts the alt-screen) or Stderr is nil.
func warn(opts Options, format string, args ...any) {
	if opts.Stderr == nil || opts.UI == "tui" {
		return
	}
	_, _ = fmt.Fprintf(opts.Stderr, format+"\n", args...)
}

// autoResolvePath returns the default log path and its containing
// directory: $HOME/.lightsailctl/logs/<UTC-ts>-<pid>.log. The directory
// is created 0700.
func autoResolvePath() (path, dir string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	dir = filepath.Join(home, ".lightsailctl", "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	return filepath.Join(dir, autoFileName()), dir, nil
}

// autoFileName returns <UTC-ts>-<pid>.log in the format the plan
// specifies.
func autoFileName() string {
	ts := time.Now().UTC().Format("2006-01-02T15-04-05Z")
	return fmt.Sprintf("%s-%d.log", ts, os.Getpid())
}
