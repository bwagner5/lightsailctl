// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

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
	Region    string   `yaml:"region,omitempty"`
	Instance  string   `yaml:"instance,omitempty"`
	Instances []string `yaml:"instances,omitempty"`
	AgentPath string   `yaml:"agent-path,omitempty"`
	Ignore    []string `yaml:"ignore,omitempty"`

	// PendingInstance, when non-nil, holds a draft describing a new
	// Lightsail instance the user answered the create-new wizard for
	// but hasn't yet committed to creating (typically because they
	// aborted at the review step). On the next `lightsailctl deploy`
	// run the fields are pre-populated so the user doesn't have to
	// re-enter them; they're still asked to confirm. Cleared once
	// the instance is actually created.
	PendingInstance *PendingInstance `yaml:"pending-instance,omitempty"`

	// Path is the file the Config was loaded from. Empty when not loaded.
	Path string `yaml:"-"`
}

// PendingInstance mirrors the subset of instance.CreateFields the deploy
// wizard collects under the __ni/ namespace. Every field is omitempty
// so a partially-answered draft is representable (and won't produce
// stray YAML keys in the conf file).
type PendingInstance struct {
	Name          string `yaml:"name,omitempty"`
	Region        string `yaml:"region,omitempty"`
	BlueprintType string `yaml:"blueprint-type,omitempty"`
	Blueprint     string `yaml:"blueprint,omitempty"`
	Bundle        string `yaml:"bundle,omitempty"`
	IPAddressType string `yaml:"ip-address-type,omitempty"`
	UserData      string `yaml:"user-data,omitempty"`
	Monitoring    string `yaml:"monitoring,omitempty"`
}

// AllInstances returns the deduplicated union of Instance and Instances.
// Instance (the legacy single-target field) is always first if set.
func (c *Config) AllInstances() []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(s string) {
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	add(c.Instance)
	for _, inst := range c.Instances {
		add(inst)
	}
	return out
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
// When Instances is populated, Instance is kept in sync with the first
// entry for backward compatibility with older CLI versions.
func (c *Config) Save(path string) error {
	if path == "" {
		if c.Path == "" {
			return errors.New("save: no path set")
		}
		path = c.Path
	}
	// Keep Instance in sync with Instances[0] for backward compat.
	if len(c.Instances) > 0 && c.Instance != c.Instances[0] {
		c.Instance = c.Instances[0]
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
