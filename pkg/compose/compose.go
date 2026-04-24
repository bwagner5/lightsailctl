// Package compose parses a subset of docker-compose files — just enough to
// extract publicly-exposed host ports so we can pre-open the Lightsail
// firewall during deploy.
package compose

import (
	"bufio"
	"os"
	"strconv"
	"strings"
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

	seen := map[int]bool{}
	var ports []int
	inPorts := false

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		trimmed := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(trimmed, "ports:") {
			inPorts = true
			continue
		}
		if inPorts {
			if !strings.HasPrefix(trimmed, "-") {
				inPorts = false
				continue
			}
			p := hostPort(strings.TrimPrefix(trimmed, "-"))
			if p > 0 && !seen[p] {
				seen[p] = true
				ports = append(ports, p)
			}
		}
	}
	return ports, scanner.Err()
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
