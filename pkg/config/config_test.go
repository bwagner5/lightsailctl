package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveThenLoad(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "lightsail.conf")
	orig := &Config{
		App: "foo", Env: "dev", Region: "us-east-2",
		Instance: "box-1", AgentPath: "/tmp/lightsailctl",
		Ignore: []string{".venv"},
	}
	if err := orig.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.App != "foo" || got.Env != "dev" || got.Region != "us-east-2" ||
		got.Instance != "box-1" || got.AgentPath != "/tmp/lightsailctl" ||
		len(got.Ignore) != 1 {
		t.Errorf("roundtrip lost data: %+v", got)
	}
	if got.Path != p {
		t.Errorf("Path = %q; want %q", got.Path, p)
	}
}

func TestFindWalksUp(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "deep", "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "lightsail.conf")
	if err := os.WriteFile(root, []byte("app: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Find(sub)
	if got != root {
		t.Errorf("Find(%s) = %q; want %q", sub, got, root)
	}
}

func TestFindAbsent(t *testing.T) {
	if got := Find(t.TempDir()); got != "" {
		t.Errorf("Find on empty tree = %q; want \"\"", got)
	}
}
