package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ResolvePath decides which config file to load, in priority order:
//  1. an explicit path (e.g. from the -config flag), if non-empty
//  2. the HOSTRUNNER_CONFIG environment variable, if set
//  3. the default ~/.config/hostrunner/config.yaml
//
// Returns "" only if no explicit path / env is given and the home dir is unknown.
func ResolvePath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if env := os.Getenv("HOSTRUNNER_CONFIG"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "hostrunner", "config.yaml")
}

type Config struct {
	AllowedRoots    []string      `yaml:"allowed_roots"`
	AllowedCommands []string      `yaml:"allowed_commands"`
	Timeout         time.Duration `yaml:"timeout"`
	MaxOutputBytes  int           `yaml:"max_output_bytes"`
	CodexExtraArgs  []string      `yaml:"codex_extra_args"`
	StreamOutput    *bool         `yaml:"stream_output"`
	// Agents holds per-peer CLI settings keyed by agent name ("codex",
	// "claude"). Missing entries/fields fall back to DefaultAgents.
	Agents map[string]AgentConfig `yaml:"agents"`
}

// AgentConfig configures one peer CLI (see internal/agent.Settings).
type AgentConfig struct {
	Command       string   `yaml:"command"`
	ExtraArgs     []string `yaml:"extra_args"`
	MCPServerName string   `yaml:"mcp_server_name"`
}

// DefaultAgents returns the built-in agent settings.
func DefaultAgents() map[string]AgentConfig {
	return map[string]AgentConfig{
		"codex":  {Command: "codex", MCPServerName: "hostrunner"},
		"claude": {Command: "claude"},
	}
}

// Agent returns the effective settings for name, falling back to defaults
// field by field so a partial `agents:` entry still works. The legacy
// top-level codex_extra_args are appended to agents.codex.extra_args.
func (c *Config) Agent(name string) AgentConfig {
	def := DefaultAgents()[name]
	a, ok := c.Agents[name]
	if !ok {
		a = def
	}
	if a.Command == "" {
		a.Command = def.Command
	}
	if a.MCPServerName == "" {
		a.MCPServerName = def.MCPServerName
	}
	if name == "codex" && len(c.CodexExtraArgs) > 0 {
		a.ExtraArgs = append(append([]string{}, a.ExtraArgs...), c.CodexExtraArgs...)
	}
	return a
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		if strings.Contains(err.Error(), "time.Duration") {
			return nil, fmt.Errorf("parse config: %w (durations need a unit, e.g. `timeout: 600s`)", err)
		}
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if c.Timeout == 0 {
		c.Timeout = 180 * time.Second
	}
	if c.MaxOutputBytes == 0 {
		c.MaxOutputBytes = 200000
	}
	if c.StreamOutput == nil {
		v := true
		c.StreamOutput = &v
	}
	if c.Agents == nil {
		c.Agents = map[string]AgentConfig{}
	}
	if len(c.AllowedRoots) == 0 {
		return nil, fmt.Errorf("allowed_roots must not be empty")
	}
	for i, r := range c.AllowedRoots {
		abs, err := filepath.Abs(r)
		if err != nil {
			return nil, fmt.Errorf("allowed_root %q: %w", r, err)
		}
		c.AllowedRoots[i] = abs
	}
	return &c, nil
}

func (c *Config) CommandAllowed(name string) bool {
	for _, a := range c.AllowedCommands {
		if a == name {
			return true
		}
	}
	return false
}

func (c *Config) StreamEnabled() bool {
	return c.StreamOutput != nil && *c.StreamOutput
}

func (c *Config) ResolveAllowedDir(dir string) (string, error) {
	if strings.HasPrefix(dir, "/sessions/") {
		return "", fmt.Errorf("got a sandbox path %q; pass the host path (e.g. /Users/...) instead", dir)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve dir: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve dir: %w", err)
	}
	for _, root := range c.AllowedRoots {
		rootResolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		if resolved == rootResolved || strings.HasPrefix(resolved, rootResolved+string(filepath.Separator)) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("path %q is not inside any allowed_root", dir)
}
