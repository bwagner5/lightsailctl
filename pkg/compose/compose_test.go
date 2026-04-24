package compose

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestParsePorts(t *testing.T) {
	body := `services:
  web:
    image: nginx
    ports:
      - "8080:80"
      - 9000
      - "0.0.0.0:3306:3306"
      - "5000-5002:5000-5002"
    environment:
      FOO: bar
  db:
    image: postgres
    ports:
      - "5432:5432/tcp"
`
	dir := t.TempDir()
	p := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ports, err := ParsePorts(p)
	if err != nil {
		t.Fatal(err)
	}
	sort.Ints(ports)
	want := []int{3306, 5000, 5432, 8080, 9000}
	if !reflect.DeepEqual(ports, want) {
		t.Errorf("ports = %v; want %v", ports, want)
	}
}

func TestFind(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(pwd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if got := Find(); got != "compose.yml" {
		t.Errorf("Find = %q; want compose.yml", got)
	}
}
