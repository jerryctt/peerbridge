// Package codex is the agent.Backend for the OpenAI Codex CLI.
package codex

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jerryctt/peerbridge/internal/agent"
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
// never writes files. Extra args and model options go before the optional
// positional prompt. Native review loads the user's config, which may carry
// a review_model override, so a requested model sets review_model as well.
func (b *Backend) ReviewArgs(scope, base, commit, prompt string, o agent.Options) ([]string, bool, error) {
	args, positional, err := ReviewArgs(scope, base, commit, prompt)
	if err != nil {
		return nil, true, err
	}
	args = append(args, b.s.ExtraArgs...)
	args = append(args, optionArgs(o, true)...)
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
// servers — and therefore without any route back into peerbridge. Config
// overrides such as `-c mcp_servers={}` are merged, not replacing, and
// `mcp_servers.<name>.enabled=false` errors when the entry is absent; both
// were verified ineffective on a real install. Model and other preferences
// can be re-supplied through extra_args (e.g. ["-m", "gpt-5"]).
//
// `codex exec resume` is a subcommand with its own option set: it accepts
// --json, --skip-git-repo-check, --ignore-user-config and -c overrides but
// NOT --sandbox, so the read-only policy is expressed as the equivalent
// config override there. (Verified against codex CLI help and live runs.)
func (b *Backend) ChatArgs(message, sessionID string, o agent.Options) []string {
	var args []string
	if sessionID == "" {
		args = []string{"exec", "--json", "--sandbox", "read-only", "--skip-git-repo-check", "--ignore-user-config"}
	} else {
		args = []string{"exec", "resume", "--json", "--skip-git-repo-check", "--ignore-user-config", "-c", `sandbox_mode="read-only"`}
	}
	args = append(args, b.s.ExtraArgs...)
	args = append(args, optionArgs(o, false)...)
	// "--" ends option parsing so caller-supplied text (a message that happens
	// to start with "--", an odd session id) is always a positional.
	args = append(args, "--")
	if sessionID != "" {
		args = append(args, sessionID)
	}
	return append(args, message)
}

// optionArgs renders model/effort as -c config overrides, which every codex
// subcommand we use (review, exec, exec resume) accepts. Values were checked
// by agent.ValidateOptions, so they cannot break out of the TOML string.
func optionArgs(o agent.Options, review bool) []string {
	var args []string
	if o.Model != "" {
		args = append(args, "-c", `model="`+o.Model+`"`)
		if review {
			args = append(args, "-c", `review_model="`+o.Model+`"`)
		}
	}
	if o.Effort != "" {
		args = append(args, "-c", `model_reasoning_effort="`+o.Effort+`"`)
	}
	return args
}

// CheckOptions rejects a model/effort when extra_args already sets one, in
// any spelling codex accepts: -m/--model (separate, "=" or attached -mX) and
// -c/--config overrides of model, review_model or model_reasoning_effort
// (separate, "=" or attached -cK=V).
func (b *Backend) CheckOptions(o agent.Options) error {
	ex := b.s.ExtraArgs
	conflict := func(what, arg string) error {
		return fmt.Errorf("agents.codex.extra_args already sets %s (%q); remove it there or omit that parameter", what, arg)
	}
	for i := 0; i < len(ex); i++ {
		a := ex[i]
		if o.Model != "" && (a == "-m" || a == "--model" || strings.HasPrefix(a, "--model=") ||
			(strings.HasPrefix(a, "-m") && !strings.HasPrefix(a, "--"))) {
			return conflict("the model", a)
		}
		var kv string
		switch {
		case a == "-c" || a == "--config":
			if i+1 < len(ex) {
				i++
				kv = ex[i]
			}
		case strings.HasPrefix(a, "--config="):
			kv = strings.TrimPrefix(a, "--config=")
		case strings.HasPrefix(a, "-c") && !strings.HasPrefix(a, "--"):
			kv = strings.TrimPrefix(a, "-c")
		default:
			continue
		}
		key, _, _ := strings.Cut(kv, "=")
		switch strings.TrimSpace(key) {
		case "model", "review_model":
			if o.Model != "" {
				return conflict("the model", kv)
			}
		case "model_reasoning_effort":
			if o.Effort != "" {
				return conflict("the reasoning effort", kv)
			}
		}
	}
	return nil
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
