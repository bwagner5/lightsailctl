// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package build

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/lightsailctl/pkg/compose"
)

// Strategy is how an extracted source tree gets turned into a running
// container.
type Strategy int

const (
	// StrategyUnknown means the source tree has no recognizable build
	// signal — neither a compose file, a Dockerfile, nor any of the
	// language manifests Detect() looks for. Callers should refuse to
	// proceed.
	StrategyUnknown Strategy = iota
	// StrategyCompose means the user authored a docker-compose file.
	// We let `docker compose up --build` drive the build.
	StrategyCompose
	// StrategyDockerfile means a Dockerfile is present (and no compose
	// file). The agent runs `docker build` and synthesizes a compose
	// file pointing at the resulting image.
	StrategyDockerfile
	// StrategyBuildpack means we recognized a language manifest. The
	// agent runs `pack build` (Cloud Native Buildpacks) and
	// synthesizes a compose file pointing at the resulting image.
	StrategyBuildpack
)

// String returns the lowercase enum name. Used in logs, status, and
// the deploy review preamble.
func (s Strategy) String() string {
	switch s {
	case StrategyCompose:
		return "compose"
	case StrategyDockerfile:
		return "dockerfile"
	case StrategyBuildpack:
		return "buildpack"
	default:
		return "unknown"
	}
}

// buildpackSignals lists the filename / glob patterns Detect() treats
// as evidence that Cloud Native Buildpacks can build this tree. Order
// matches the precedence in §2 of the plan: Go → Node → Python → Java
// → .NET → Ruby → PHP → static.
//
// Each entry is paired with a short human-readable language label
// surfaced in the "reason" return value so users see "Go via go.mod"
// rather than just a filename.
var buildpackSignals = []struct {
	// match is called with the staging dir and returns the matched
	// path on success ("" when no match). Globs need this; plain
	// filenames could be a string field, but a single shape keeps the
	// table readable.
	match func(dir string) string
	lang  string
}{
	{matchFile("go.mod"), "Go"},
	{matchFile("package.json"), "Node.js"},
	{matchAny("requirements.txt", "pyproject.toml", "setup.py"), "Python"},
	{matchAny("pom.xml", "build.gradle", "build.gradle.kts"), "Java"},
	{matchGlob("*.csproj", "*.fsproj"), ".NET"},
	{matchFile("Gemfile"), "Ruby"},
	{matchFile("composer.json"), "PHP"},
	{matchFile("index.html"), "static site"},
}

// Detect inspects dir and returns the build strategy plus a short
// reason string describing the decision (e.g. "go.mod present" or
// "no compose file, Dockerfile, or recognized language manifest").
// Errors are reserved for genuine I/O failures; "no signal" is a
// successful return with StrategyUnknown.
func Detect(dir string) (Strategy, string, error) {
	if dir == "" {
		dir = "."
	}
	if fi, err := os.Stat(dir); err != nil {
		return StrategyUnknown, "", fmt.Errorf("stat %s: %w", dir, err)
	} else if !fi.IsDir() {
		return StrategyUnknown, "", fmt.Errorf("%s is not a directory", dir)
	}

	for _, name := range compose.DefaultPaths {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return StrategyCompose, name, nil
		}
	}

	if _, err := os.Stat(filepath.Join(dir, "Dockerfile")); err == nil {
		return StrategyDockerfile, "Dockerfile", nil
	}

	for _, sig := range buildpackSignals {
		if matched := sig.match(dir); matched != "" {
			return StrategyBuildpack, fmt.Sprintf("%s via %s", sig.lang, matched), nil
		}
	}

	return StrategyUnknown, "no compose file, Dockerfile, or recognized language manifest", nil
}

// matchFile returns a matcher that succeeds when the named file
// exists at dir's top level.
func matchFile(name string) func(dir string) string {
	return func(dir string) string {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return name
		}
		return ""
	}
}

// matchAny returns a matcher that succeeds at the first file in
// names found at dir's top level.
func matchAny(names ...string) func(dir string) string {
	return func(dir string) string {
		for _, n := range names {
			if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
				return n
			}
		}
		return ""
	}
}

// matchGlob returns a matcher that succeeds at the first glob pattern
// matching at least one file at dir's top level. Glob patterns may
// only reference the basename — Detect deliberately doesn't recurse,
// so projects with manifests buried in subdirs will currently look
// unknown. (A monorepo escape hatch is the user-authored compose
// path.)
func matchGlob(patterns ...string) func(dir string) string {
	return func(dir string) string {
		for _, p := range patterns {
			matches, err := filepath.Glob(filepath.Join(dir, p))
			if err == nil && len(matches) > 0 {
				return strings.TrimPrefix(matches[0], dir+string(os.PathSeparator))
			}
		}
		return ""
	}
}
