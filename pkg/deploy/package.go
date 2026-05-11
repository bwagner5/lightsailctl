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

	"github.com/aws/lightsailctl/pkg/build"
)

// Built-in excludes — applied in addition to the user's lightsail.conf ignore list.
var builtinExcludes = map[string]bool{
	".git":         true,
	".lightsail":   true,
	"node_modules": true,
	".DS_Store":    true,
}

// strategyExcludes lists per-strategy add-ons applied on top of
// builtinExcludes. Compose users keep the existing behavior (no
// add-ons) so authoring `compose.yml` is the explicit opt-out.
//
// For Dockerfile / buildpack the user typically can't make the
// upload smaller themselves — buildpacks have no .dockerignore
// equivalent, and the tarball is uploaded BEFORE docker reads
// .dockerignore. Pruning common build-artifact dirs at upload time
// is the only way to keep the round-trip fast on a 100 MB
// Maven/Gradle/Webpack tree.
var strategyExcludes = map[build.Strategy][]string{
	build.StrategyDockerfile: {"target", "dist", "build", "__pycache__", ".next", ".nuxt", "vendor"},
	build.StrategyBuildpack:  {"target", "dist", "build", "__pycache__", ".next", ".nuxt", "vendor"},
}

// AssetName builds the S3 key for a deploy tarball: deploy/<unix>-<sha>.tar.gz.
// Uses the short git SHA of the current HEAD if available, else "nocommit".
func AssetName() string {
	return fmt.Sprintf("deploy/%d-%s.tar.gz", time.Now().Unix(), gitSHA())
}

// Package creates a gzip'd tarball of srcDir, excluding builtinExcludes plus
// any strategy add-ons plus the user's extraIgnore list. Returns the temp-file
// path and its size; caller must os.Remove.
func Package(srcDir string, strategy build.Strategy, extraIgnore []string) (path string, size int64, err error) {
	excl := buildExcludes(strategy, extraIgnore)

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
func PackageTo(srcDir string, strategy build.Strategy, extraIgnore []string, w io.Writer) error {
	return tarTo(srcDir, buildExcludes(strategy, extraIgnore), w)
}

// buildExcludes assembles the final exclude set: builtinExcludes ∪
// strategyExcludes[strategy] ∪ extraIgnore.
func buildExcludes(strategy build.Strategy, extraIgnore []string) map[string]bool {
	excl := map[string]bool{}
	for k, v := range builtinExcludes {
		excl[k] = v
	}
	for _, x := range strategyExcludes[strategy] {
		excl[x] = true
	}
	for _, x := range extraIgnore {
		excl[x] = true
	}
	return excl
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
