// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package compose

import (
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultPaths are the filenames we look for, in order, in the working dir.
var DefaultPaths = []string{
	"docker-compose.yml",
	"docker-compose.yaml",
	"compose.yml",
	"compose.yaml",
}

// Find returns the first existing compose file in the current directory, or
// "" if none is present.
func Find() string {
	for _, p := range DefaultPaths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// ParsePorts reads a compose file and returns the unique publicly-exposed
// host ports across all services.
func ParsePorts(path string) ([]int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var doc yaml.Node
	if err := yaml.NewDecoder(f).Decode(&doc); err != nil {
		return nil, err
	}

	root := documentRoot(&doc)
	services := mappingValue(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil, nil
	}

	seen := map[int]bool{}
	var ports []int
	for i := 0; i+1 < len(services.Content); i += 2 {
		service := services.Content[i+1]
		if service.Kind != yaml.MappingNode {
			continue
		}
		portList := mappingValue(service, "ports")
		if portList == nil || portList.Kind != yaml.SequenceNode {
			continue
		}
		for _, item := range portList.Content {
			p := portNode(item)
			if p <= 0 || seen[p] {
				continue
			}
			seen[p] = true
			ports = append(ports, p)
		}
	}
	return ports, nil
}

func documentRoot(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		return n.Content[0]
	}
	return n
}

func mappingValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func portNode(n *yaml.Node) int {
	if n == nil {
		return 0
	}
	switch n.Kind {
	case yaml.ScalarNode:
		return hostPort(n.Value)
	case yaml.MappingNode:
		if published := mappingValue(n, "published"); published != nil {
			return hostPort(published.Value)
		}
		return 0
	default:
		return 0
	}
}

// hostPort extracts the host-side port from a compose port string.
// Handles "8080", "8080:80", "8080:80/tcp", "0.0.0.0:8080:80", and ranges.
func hostPort(s string) int {
	s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), "\"'"))
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ":")
	var host string
	switch len(parts) {
	case 1:
		host = parts[0]
	case 2:
		host = parts[0]
	case 3:
		host = parts[1]
	default:
		return 0
	}
	if i := strings.Index(host, "-"); i >= 0 {
		host = host[:i]
	}
	p, err := strconv.Atoi(host)
	if err != nil || p <= 0 {
		return 0
	}
	return p
}
