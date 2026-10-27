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

// readOnlyTools is the full set of built-in tools the peer may use. There is
// deliberately no Bash: `claude` has no OS sandbox, and even "read-only" git
// invocations can write (`git diff --output=FILE`), so the peer gets file
// reading only. Review diffs are computed by hostrunner and embedded in the
// prompt instead (see server.runReview).
const readOnlyTools = "Read,Grep,Glob"

// deniedTools is belt-and-braces on top of --tools: even if a tool were
// re-added by some setting, editing and shell stay denied.
const deniedTools = "Edit,Write,NotebookEdit,Bash"

// ChatArgs runs one print-mode turn.
//
//   - --strict-mcp-config with no --mcp-config loads zero MCP servers, so the
//     peer cannot call hostrunner recursively; unlike --bare it leaves the
//     user's OAuth/subscription login intact.
//   - --setting-sources "" loads no user/project/local settings, so inherited
//     permission allow-rules and hooks cannot widen what the peer may do
//     (--allowedTools is additive to settings; this makes it the whole list).
//   - --permission-mode dontAsk denies anything not allowlisted without
//     prompting, so the non-interactive run can never hang.
//
// The message goes last, after a "--" terminator, so it is always the prompt
// positional even when it starts with "--" (and no list-valued flag can
// consume it as a value).
func (b *Backend) ChatArgs(message, sessionID string) []string {
	args := []string{
		"-p",
		"--strict-mcp-config",
		"--setting-sources", "",
		"--output-format", "json",
		"--permission-mode", "dontAsk",
		"--tools", readOnlyTools,
		"--allowedTools", readOnlyTools,
		"--disallowedTools", deniedTools,
	}
	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}
	args = append(args, b.s.ExtraArgs...)
	return append(args, "--", message)
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
