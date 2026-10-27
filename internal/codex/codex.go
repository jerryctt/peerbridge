// Package codex is the agent.Backend for the OpenAI Codex CLI.
package codex

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jerryctt/hostrunner-mcp/internal/agent"
)

// ReviewArgs builds the `codex review` CLI arguments for the given mode.
//
// In the codex CLI the scope flags are mutually exclusive with the positional
// [PROMPT] argument ("error: the argument '--uncommitted' cannot be used with
// '[PROMPT]'", same for --base and --commit). So:
//
//   - prompt == "": the scope is passed as a flag.
//     mode "" or "uncommitted" -> ["review", "--uncommitted"]
//     mode "base"              -> ["review", "--base", base]   (base must be non-empty)
//     mode "commit"            -> ["review", "--commit", sha]  (commit must be non-empty)
//   - prompt != "": no scope flag is emitted; the scope is folded into the
//     prompt text and returned as `positional`, to be appended as the last
//     CLI argument.
func ReviewArgs(mode, base, commit, prompt string) (args []string, positional string, err error) {
	scope, err := agent.ScopeSentence(mode, base, commit)
	if err != nil {
		return nil, "", err
	}
	if prompt != "" {
		return []string{"review"}, scope + " " + prompt, nil
	}
	switch mode {
	case "", "uncommitted":
		return []string{"review", "--uncommitted"}, "", nil
	case "base":
		return []string{"review", "--base", base}, "", nil
	default: // "commit" — ScopeSentence already rejected anything else
		return []string{"review", "--commit", commit}, "", nil
	}
}

// Backend implements agent.Backend for codex.
type Backend struct{ s agent.Settings }

// New returns a codex backend using the given settings.
func New(s agent.Settings) *Backend { return &Backend{s: s} }

func (b *Backend) Name() string    { return "codex" }
func (b *Backend) Command() string { return b.s.Command }

// ReviewArgs is native for codex: `codex review` computes the diff itself and
// never writes files. Extra args go before the optional positional prompt.
func (b *Backend) ReviewArgs(scope, base, commit, prompt string) ([]string, bool, error) {
	args, positional, err := ReviewArgs(scope, base, commit, prompt)
	if err != nil {
		return nil, true, err
	}
	args = append(args, b.s.ExtraArgs...)
	if positional != "" {
		args = append(args, positional)
	}
	return args, true, nil
}

// ChatArgs runs one non-interactive turn in a read-only sandbox, emitting
// JSONL so ParseChat can recover the thread id.
//
// --ignore-user-config skips ~/.codex/config.toml (auth is still read from
// CODEX_HOME), which is the only way to start the peer without the user's MCP
// servers — and therefore without any route back into hostrunner. Config
// overrides such as `-c mcp_servers={}` are merged, not replacing, and
// `mcp_servers.<name>.enabled=false` errors when the entry is absent; both
// were verified ineffective on a real install. Model and other preferences
// can be re-supplied through extra_args (e.g. ["-m", "gpt-5"]).
//
// `codex exec resume` is a subcommand with its own option set: it accepts
// --json, --skip-git-repo-check, --ignore-user-config and -c overrides but
// NOT --sandbox, so the read-only policy is expressed as the equivalent
// config override there. (Verified against codex CLI help and live runs.)
func (b *Backend) ChatArgs(message, sessionID string) []string {
	var args []string
	if sessionID == "" {
		args = []string{"exec", "--json", "--sandbox", "read-only", "--skip-git-repo-check", "--ignore-user-config"}
	} else {
		args = []string{"exec", "resume", "--json", "--skip-git-repo-check", "--ignore-user-config", "-c", `sandbox_mode="read-only"`}
	}
	args = append(args, b.s.ExtraArgs...)
	if sessionID != "" {
		args = append(args, sessionID)
	}
	return append(args, message)
}

type jsonlEvent struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Item     struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// ParseChat reads `codex exec --json` output: thread.started carries the
// session id, and every item.completed/agent_message carries reply text
// (joined with blank lines when there are several).
func (b *Backend) ParseChat(stdout string) (reply, sessionID string, err error) {
	var parts []string
	var failure string
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var ev jsonlEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "thread.started":
			sessionID = ev.ThreadID
		case "item.completed":
			if ev.Item.Type == "agent_message" && ev.Item.Text != "" {
				parts = append(parts, ev.Item.Text)
			}
		case "turn.failed", "error":
			if ev.Error.Message != "" {
				failure = ev.Error.Message
			}
		}
	}
	if len(parts) == 0 {
		if failure != "" {
			return "", sessionID, fmt.Errorf("codex turn failed: %s", failure)
		}
		return "", sessionID, fmt.Errorf("no agent_message in codex output; tail: %s", agent.Tail(stdout, 2048))
	}
	return strings.Join(parts, "\n\n"), sessionID, nil
}
