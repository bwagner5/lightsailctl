// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

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

func TestParsePortsLongSyntax(t *testing.T) {
	body := `services:
  web:
    image: nginx
    ports:
      - target: 80
        published: "8080"
        protocol: tcp
      - target: 443
        published: 8443
      - target: 5000
        published: "5000-5002"
      - target: 6000
  worker:
    image: busybox
    ports:
      - name: metrics
        target: 9100
        published: "0.0.0.0:9100:9100"
`
	dir := t.TempDir()
	p := filepath.Join(dir, "compose.yml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ports, err := ParsePorts(p)
	if err != nil {
		t.Fatal(err)
	}
	sort.Ints(ports)
	want := []int{5000, 8080, 8443, 9100}
	if !reflect.DeepEqual(ports, want) {
		t.Errorf("ports = %v; want %v", ports, want)
	}
}

func TestParsePortsIgnoresNonServicePorts(t *testing.T) {
	body := `x-template:
  ports:
    - "9999:9999"
services:
  web:
    image: nginx
    environment:
      PORTS: "1234:1234"
`
	dir := t.TempDir()
	p := filepath.Join(dir, "compose.yml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ports, err := ParsePorts(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 0 {
		t.Errorf("ports = %v; want none", ports)
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
