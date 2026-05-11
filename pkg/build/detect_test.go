package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFiles creates each named file (relative to dir) with empty
// content. Test fixtures only ever care about presence, so we don't
// bother filling the bodies.
func writeFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		full := filepath.Join(dir, n)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", full, err)
		}
		if err := os.WriteFile(full, nil, 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
}

func TestDetect_Compose(t *testing.T) {
	for _, name := range []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, name)
			s, reason, err := Detect(dir)
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if s != StrategyCompose {
				t.Errorf("strategy = %v, want StrategyCompose", s)
			}
			if reason != name {
				t.Errorf("reason = %q, want %q", reason, name)
			}
		})
	}
}

func TestDetect_Dockerfile(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "Dockerfile")
	s, reason, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if s != StrategyDockerfile {
		t.Errorf("strategy = %v, want StrategyDockerfile", s)
	}
	if reason != "Dockerfile" {
		t.Errorf("reason = %q, want Dockerfile", reason)
	}
}

func TestDetect_Buildpack(t *testing.T) {
	cases := []struct {
		name      string
		files     []string
		wantLang  string
		wantMatch string
	}{
		{"go", []string{"go.mod"}, "Go", "go.mod"},
		{"node", []string{"package.json"}, "Node.js", "package.json"},
		{"python_requirements", []string{"requirements.txt"}, "Python", "requirements.txt"},
		{"python_pyproject", []string{"pyproject.toml"}, "Python", "pyproject.toml"},
		{"python_setup", []string{"setup.py"}, "Python", "setup.py"},
		{"java_maven", []string{"pom.xml"}, "Java", "pom.xml"},
		{"java_gradle", []string{"build.gradle"}, "Java", "build.gradle"},
		{"java_kts", []string{"build.gradle.kts"}, "Java", "build.gradle.kts"},
		{"dotnet_cs", []string{"app.csproj"}, ".NET", "app.csproj"},
		{"dotnet_fs", []string{"app.fsproj"}, ".NET", "app.fsproj"},
		{"ruby", []string{"Gemfile"}, "Ruby", "Gemfile"},
		{"php", []string{"composer.json"}, "PHP", "composer.json"},
		{"static", []string{"index.html"}, "static site", "index.html"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, tc.files...)
			s, reason, err := Detect(dir)
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if s != StrategyBuildpack {
				t.Errorf("strategy = %v, want StrategyBuildpack", s)
			}
			if !strings.Contains(reason, tc.wantLang) {
				t.Errorf("reason = %q, want it to contain %q", reason, tc.wantLang)
			}
			if !strings.Contains(reason, tc.wantMatch) {
				t.Errorf("reason = %q, want it to contain %q", reason, tc.wantMatch)
			}
		})
	}
}

func TestDetect_Unknown(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "README.md", "LICENSE")
	s, reason, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if s != StrategyUnknown {
		t.Errorf("strategy = %v, want StrategyUnknown", s)
	}
	if reason == "" {
		t.Errorf("reason is empty; expected an explanation")
	}
}

// Precedence: compose > Dockerfile > language signals. A repo with
// all three should pick compose; one with Dockerfile + language
// signal should pick Dockerfile.
func TestDetect_PrecedenceComposeOverDockerfile(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "docker-compose.yml", "Dockerfile", "go.mod")
	s, _, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if s != StrategyCompose {
		t.Errorf("strategy = %v, want StrategyCompose", s)
	}
}

func TestDetect_PrecedenceDockerfileOverBuildpack(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "Dockerfile", "go.mod")
	s, reason, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if s != StrategyDockerfile {
		t.Errorf("strategy = %v, want StrategyDockerfile", s)
	}
	if reason != "Dockerfile" {
		t.Errorf("reason = %q, want Dockerfile", reason)
	}
}

// Earlier buildpack signal wins over a later one (Go over Node when
// both are present).
func TestDetect_BuildpackPrecedence(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "go.mod", "package.json")
	s, reason, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if s != StrategyBuildpack {
		t.Errorf("strategy = %v, want StrategyBuildpack", s)
	}
	if !strings.Contains(reason, "Go") {
		t.Errorf("reason = %q, want Go to win over Node", reason)
	}
}

func TestDetect_NonexistentDir(t *testing.T) {
	_, _, err := Detect(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Errorf("expected error for missing dir")
	}
}

func TestDetect_FileNotDir(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "file")
	if err := os.WriteFile(f, nil, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, _, err := Detect(f)
	if err == nil {
		t.Errorf("expected error for non-directory path")
	}
}

func TestStrategy_String(t *testing.T) {
	cases := map[Strategy]string{
		StrategyUnknown:    "unknown",
		StrategyCompose:    "compose",
		StrategyDockerfile: "dockerfile",
		StrategyBuildpack:  "buildpack",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("Strategy(%d).String() = %q, want %q", s, got, want)
		}
	}
}
