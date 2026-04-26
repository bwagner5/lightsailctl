// Package config reads and writes lightsail.conf — the per-project file that
// tells `lightsailctl deploy` which Lightsail Application it's targeting.
//
// Schema:
//
//	app:        cosmic-comet         # application name
//	env:        dev                  # environment within the app
//	region:     us-east-2            # AWS region
//	instance:   my-lightsail-box     # target Lightsail instance
//	agent-path: /path/to/lightsailctl# linux/amd64 binary to scp on first deploy
//	ignore:                          # extra paths excluded from the tarball
//	  - node_modules
//	  - .venv
//
// `ignore` is additive: built-in excludes (.git, .lightsail, node_modules,
// .DS_Store) are always applied. agent-path is only consulted when the
// app doesn't yet exist and needs to be created.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Filename is the canonical name; Find walks up from cwd looking for it.
const Filename = "lightsail.conf"

// Config is the on-disk schema.
type Config struct {
	App       string   `yaml:"app"`
	Env       string   `yaml:"env"`
	Region    string   `yaml:"region"`
	Instance  string   `yaml:"instance,omitempty"`
	AgentPath string   `yaml:"agent-path,omitempty"`
	Ignore    []string `yaml:"ignore,omitempty"`

	// Path is the file the Config was loaded from. Empty when not loaded.
	Path string `yaml:"-"`
}

// Find walks up from start looking for lightsail.conf. Returns the path or
// "" if not found.
func Find(start string) string {
	dir, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	for {
		p := filepath.Join(dir, Filename)
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// Load reads and parses a Config. Returns (nil, os.ErrNotExist) when missing.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	c.Path = path
	return &c, nil
}

// LoadFromCwd walks up from the current directory and loads the nearest
// lightsail.conf. Returns (nil, nil) if none found (not an error — caller
// decides whether absence triggers the first-run wizard).
func LoadFromCwd() (*Config, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	p := Find(cwd)
	if p == "" {
		return nil, nil
	}
	return Load(p)
}

// Save writes the Config as YAML. Creates parent directories if needed.
func (c *Config) Save(path string) error {
	if path == "" {
		if c.Path == "" {
			return errors.New("save: no path set")
		}
		path = c.Path
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
