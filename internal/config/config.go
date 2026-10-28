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
//  2. the PEERBRIDGE_CONFIG environment variable, if set (HOSTRUNNER_CONFIG,
//     the pre-rename name, is still honoured)
//  3. the first existing file among DefaultPaths(); if none exists, the
//     first candidate (so the error message names the conventional place).
//
// Returns "" only if no explicit path / env is given and no home dir is known.
func ResolvePath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if env := os.Getenv("PEERBRIDGE_CONFIG"); env != "" {
		return env
	}
	if env := os.Getenv("HOSTRUNNER_CONFIG"); env != "" { // legacy name (project was hostrunner-mcp)
		return env
	}
	paths := DefaultPaths()
	if len(paths) == 0 {
		return ""
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return paths[0]
}

// DefaultPaths lists the conventional config locations for this OS:
// ~/.config/peerbridge/config.yaml everywhere (the documented default), plus
// the platform's user config dir when it differs — %APPDATA%\peerbridge on
// Windows, ~/Library/Application Support/peerbridge on macOS. The same
// locations under the pre-rename name "hostrunner" come last, so an existing
// installation keeps working until the user moves its config.
func DefaultPaths() []string {
	var paths []string
	seen := map[string]bool{}
	var dirs []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, ".config"))
	}
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		dirs = append(dirs, dir)
	}
	for _, name := range []string{"peerbridge", "hostrunner"} {
		for _, dir := range dirs {
			p := filepath.Join(dir, name, "config.yaml")
			if !seen[p] {
				seen[p] = true
				paths = append(paths, p)
			}
		}
	}
	return paths
}

type Config struct {
	AllowedRoots   []string      `yaml:"allowed_roots"`
	Timeout        time.Duration `yaml:"timeout"`
	MaxOutputBytes int           `yaml:"max_output_bytes"`
	CodexExtraArgs []string      `yaml:"codex_extra_args"`
	StreamOutput   *bool         `yaml:"stream_output"`
	// StatusPoll is how long job_status blocks waiting for a job before
	// answering "running". Longer means fewer polls (and fewer permission
	// prompts in the GUI) but it must stay under the MCP client's per-call
	// timeout: Codex tool_timeout_sec (120 when installed by our scripts, 60
	// by default), Claude Desktop ~180s. Default 50s.
	StatusPoll time.Duration `yaml:"status_poll"`
	// Agents holds per-peer CLI settings keyed by agent name ("codex",
	// "claude"). Missing entries/fields fall back to DefaultAgents.
	Agents map[string]AgentConfig `yaml:"agents"`
	// AllowedCommands is ignored: it configured run_command, which was
	// removed in v0.5.0. Still parsed so main can warn about stale configs.
	AllowedCommands []string `yaml:"allowed_commands"`
}

// AgentConfig configures one peer CLI (see internal/agent.Settings).
type AgentConfig struct {
	Command   string   `yaml:"command"`
	ExtraArgs []string `yaml:"extra_args"`
}

// DefaultAgents returns the built-in agent settings.
func DefaultAgents() map[string]AgentConfig {
	return map[string]AgentConfig{
		"codex":  {Command: "codex"},
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
	if c.StatusPoll == 0 {
		c.StatusPoll = 50 * time.Second
	}
	if c.StatusPoll < 5*time.Second || c.StatusPoll > 170*time.Second {
		return nil, fmt.Errorf("status_poll must be between 5s and 170s (got %s); it has to stay below the MCP client's per-call timeout", c.StatusPoll)
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
