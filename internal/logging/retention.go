// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package logging

import (
	"os"
	"path/filepath"
	"sort"
	"time"
)

// pruneLogs deletes files in dir older than maxAge, then deletes the
// oldest files when the count exceeds maxCount. Errors are returned
// but Init treats them as best-effort.
func pruneLogs(dir string, now func() time.Time, maxAge time.Duration, maxCount int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type fileInfo struct {
		path    string
		modTime time.Time
	}
	files := make([]fileInfo, 0, len(entries))
	cutoff := now().Add(-maxAge)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// Only manage .log files in this directory; leave other
		// files alone so unrelated content in the dir survives.
		if filepath.Ext(e.Name()) != ".log" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if info.ModTime().Before(cutoff) {
			// Age-based delete; tolerate races with concurrent
			// invocations (ENOENT is benign).
			_ = os.Remove(path)
			continue
		}
		files = append(files, fileInfo{path: path, modTime: info.ModTime()})
	}
	// Count-based trim: drop the oldest until we're under maxCount.
	if len(files) > maxCount {
		sort.Slice(files, func(i, j int) bool {
			return files[i].modTime.Before(files[j].modTime)
		})
		overflow := len(files) - maxCount
		for i := 0; i < overflow; i++ {
			_ = os.Remove(files[i].path)
		}
	}
	return nil
}
