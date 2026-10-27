// Package claude is the agent.Backend for the Claude Code CLI (`claude -p`).
package claude

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jerryctt/hostrunner-mcp/internal/agent"
)

// Backend implements agent.Backend for claude.
type Backend struct{ s agent.Settings }

// New returns a claude backend using the given settings.
func New(s agent.Settings) *Backend { return &Backend{s: s} }

func (b *Backend) Name() string    { return "claude" }
func (b *Backend) Command() string { return b.s.Command }

// ReviewArgs: claude has no review subcommand, so reviews go through
// ChatArgs with agent.ReviewPrompt. Scope is still validated here so bad
// requests fail before a job is started.
func (b *Backend) ReviewArgs(scope, base, commit, _ string) ([]string, bool, error) {
	if _, err := agent.ScopeSentence(scope, base, commit); err != nil {
		return nil, false, err
	}
	return nil, false, nil
}

// readOnlyTools is the full set of built-in tools the peer may use at all.
const readOnlyTools = "Read,Grep,Glob,Bash"

// allowedRules auto-approves only reads and read-only git commands. Under
// --permission-mode dontAsk anything else is denied without prompting.
const allowedRules = "Read,Grep,Glob,Bash(git diff:*),Bash(git log:*),Bash(git status:*),Bash(git show:*)"

// deniedTools removes the editing tools from the peer entirely.
const deniedTools = "Edit,Write,NotebookEdit"

// ChatArgs runs one print-mode turn. --strict-mcp-config with no --mcp-config
// loads zero MCP servers, so the peer cannot call hostrunner recursively while
// the user's normal OAuth/subscription login keeps working (--bare would be
// stricter but disables OAuth and keychain auth, breaking logged-in CLIs).
// The message is passed immediately after -p so that no later list-valued
// flag can consume it as a value.
func (b *Backend) ChatArgs(message, sessionID string) []string {
	args := []string{
		"-p", message,
		"--strict-mcp-config",
		"--output-format", "json",
		"--permission-mode", "dontAsk",
		"--tools", readOnlyTools,
		"--allowedTools", allowedRules,
		"--disallowedTools", deniedTools,
	}
	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}
	return append(args, b.s.ExtraArgs...)
}

type result struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	IsError   bool   `json:"is_error"`
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
}

// ParseChat reads the single JSON object `claude -p --output-format json`
// prints. Anything before the first '{' is ignored.
func (b *Backend) ParseChat(stdout string) (reply, sessionID string, err error) {
	i := strings.IndexByte(stdout, '{')
	if i < 0 {
		return "", "", fmt.Errorf("no JSON object in claude output; tail: %s", agent.Tail(stdout, 2048))
	}
	var r result
	if err := json.NewDecoder(strings.NewReader(stdout[i:])).Decode(&r); err != nil {
		return "", "", fmt.Errorf("parse claude output: %w; tail: %s", err, agent.Tail(stdout, 2048))
	}
	if r.IsError {
		return "", r.SessionID, fmt.Errorf("claude reported an error (%s): %s", r.Subtype, r.Result)
	}
	return r.Result, r.SessionID, nil
}
