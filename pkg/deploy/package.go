// Package deploy provides the saga steps for shipping a source directory to
// a Lightsail Application's env bucket.
//
// It is split from pkg/app because it needs to be importable by a future
// promote/rollback op without pulling in the whole app resource model.
package deploy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Built-in excludes — applied in addition to the user's lightsail.conf ignore list.
var builtinExcludes = map[string]bool{
	".git":         true,
	".lightsail":   true,
	"node_modules": true,
	".DS_Store":    true,
}

// AssetName builds the S3 key for a deploy tarball: deploy/<unix>-<sha>.tar.gz.
// Uses the short git SHA of the current HEAD if available, else "nocommit".
func AssetName() string {
	return fmt.Sprintf("deploy/%d-%s.tar.gz", time.Now().Unix(), gitSHA())
}

// Package creates a gzip'd tarball of srcDir, excluding builtinExcludes plus
// extraIgnore. Returns the temp-file path and its size; caller must os.Remove.
func Package(srcDir string, extraIgnore []string) (path string, size int64, err error) {
	excl := map[string]bool{}
	for k, v := range builtinExcludes {
		excl[k] = v
	}
	for _, x := range extraIgnore {
		excl[x] = true
	}

	tmp, err := os.CreateTemp("", "lightsail-deploy-*.tar.gz")
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = tmp.Close() }()

	if err := tarTo(srcDir, excl, tmp); err != nil {
		_ = os.Remove(tmp.Name())
		return "", 0, err
	}
	fi, err := tmp.Stat()
	if err != nil {
		return tmp.Name(), 0, err
	}
	return tmp.Name(), fi.Size(), nil
}

// PackageTo writes the archive to w (for tests).
func PackageTo(srcDir string, extraIgnore []string, w io.Writer) error {
	excl := map[string]bool{}
	for k, v := range builtinExcludes {
		excl[k] = v
	}
	for _, x := range extraIgnore {
		excl[x] = true
	}
	return tarTo(srcDir, excl, w)
}

func tarTo(srcDir string, excl map[string]bool, w io.Writer) error {
	gz := gzip.NewWriter(w)
	defer func() { _ = gz.Close() }()
	tw := tar.NewWriter(gz)
	defer func() { _ = tw.Close() }()

	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		base := filepath.Base(path)
		if excl[base] {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = rel
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		_, err = io.Copy(tw, f)
		return err
	})
}

// gitSHA returns the short HEAD SHA, or "nocommit" outside a repo.
func gitSHA() string {
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		return "nocommit"
	}
	return strings.TrimSpace(buf.String())
}
