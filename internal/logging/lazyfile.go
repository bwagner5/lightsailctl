// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package logging

import (
	"os"
	"sync"
)

// lazyFile is an io.Writer that opens its underlying file on the first
// successful Write. Helps keep --help / parse-fail runs from dropping
// empty log files on disk.
type lazyFile struct {
	path string
	mu   sync.Mutex
	f    *os.File
	err  error // sticky: if open once fails, every Write becomes a no-op
}

func newLazyFile(path string) *lazyFile { return &lazyFile{path: path} }

// Write opens the file on first call (0600) and appends. Errors open
// and writes are swallowed: logging must never fail the program.
func (l *lazyFile) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return len(p), nil
	}
	if l.f == nil {
		// #nosec G304 -- path is resolved by Init under a 0700 dir.
		f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			l.err = err
			return len(p), nil
		}
		l.f = f
	}
	n, err := l.f.Write(p)
	if err != nil {
		// Stop trying; further writes become no-ops.
		l.err = err
		return n, nil
	}
	return n, nil
}

// Close flushes and closes the underlying file if one was ever opened.
func (l *lazyFile) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
