# Peer Agents (Claude ⇄ Codex) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make hostrunner-mcp installable in both Claude (Desktop/Code) and Codex (CLI/macOS app), exposing generic `review_start` / `chat_start` / `job_status` tools that run the *other* vendor's CLI read-only on the host, with multi-turn session continuation, while keeping `codex_review_start` / `codex_review_status` working as aliases.

**Architecture:** A new `internal/agent` package defines a `Backend` interface (argv builders + output parser) implemented by `internal/codex` (existing, extended) and a new `internal/claude`. `internal/server` becomes backend-agnostic: a `Service` resolves the agent (explicit param or inferred from the MCP client name), validates, and runs every job through the existing background job store, now keyed by (worktree root, agent, kind). A new `scripts/install-codex.sh` registers the same binary in `~/.codex/config.toml` and installs the shared skills into `~/.agents/skills/`.

**Tech Stack:** Go 1.25 (`go.mod` already says `go 1.25.5`), `github.com/mark3labs/mcp-go v0.55.1`, `gopkg.in/yaml.v3`, `github.com/rs/zerolog`, POSIX `sh` for scripts. Spec: `docs/superpowers/specs/2026-10-27-peer-agents-design.md`.

## Global Constraints

- Module path `github.com/jerryctt/hostrunner-mcp`; binary `hostrunner` from `cmd/hostrunner`.
- **No shell, ever.** All CLIs run as argv arrays through `internal/exec`.
- **Host paths only**, inside `allowed_roots`; `/sessions/...` rejected (unchanged).
- **Peers are read-only.** Claude peer: `-p --bare --output-format json --permission-mode dontAsk --tools Read,Grep,Glob,Bash --allowedTools <read-only git rules> --disallowedTools Edit,Write,NotebookEdit`. Codex peer chat: `exec --json --sandbox read-only --skip-git-repo-check -c mcp_servers.<name>.enabled=false`. Codex review: `codex review` exactly as today.
- No file renames/moves: `internal/codex` stays where it is (changes are applied to the user's machine by copying files).
- Backward compatible: `codex_review_start`, `codex_review_status`, `run_command`, legacy `codex_extra_args`, existing `config.yaml` files all keep working.
- `job_status` long-poll stays 50s (must remain under Codex `tool_timeout_sec = 120` set by the installer and Claude Desktop's ~180s).
- Version becomes `0.3.0` (plugin.json, server version string, README).
- Commits: run `go vet ./... && go test ./...` before each commit; commit messages end with the attribution trailer used in this session.
- Working copy for implementation is the cloud clone at `/home/claude/hostrunner-mcp`; the final task syncs changed files to `/Users/jerryctt/code/personal/hostrunner-mcp` and commits there.

---

### Task 1: `internal/agent` — interface, scope helpers, review prompt, peer inference, registry

**Files:**
- Create: `internal/agent/agent.go`
- Create: `internal/agent/agent_test.go`

**Interfaces:**
- Produces:
  ```go
  type Kind string // KindReview = "review", KindChat = "chat"
  type Runner interface { Run(ctx context.Context, req exec.Request) (exec.Result, error) }
  type Settings struct { Command string; ExtraArgs []string; MCPServerName string }
  type Backend interface {
      Name() string
      Command() string
      ReviewArgs(scope, base, commit, prompt string) (args []string, native bool, err error)
      ChatArgs(message, sessionID string) []string
      ParseChat(stdout string) (reply, sessionID string, err error)
  }
  func ScopeSentence(scope, base, commit string) (string, error)
  func ReviewPrompt(scope, base, commit, userPrompt string) (string, error)
  func Default(clientName string) (string, bool)
  type Registry map[string]Backend
  func (r Registry) Get(name string) (Backend, error)
  func (r Registry) Names() []string
  func Label(name string) string   // "codex" -> "Codex"
  func Tail(s string, n int) string
  ```

- [ ] **Step 1: Write the failing tests**

```go
package agent

import (
	"strings"
	"testing"
)

func TestScopeSentence(t *testing.T) {
	cases := []struct {
		scope, base, commit, want string
		wantErr                   bool
	}{
		{"", "", "", "uncommitted changes (staged, unstaged, and untracked)", false},
		{"uncommitted", "", "", "uncommitted changes (staged, unstaged, and untracked)", false},
		{"base", "main", "", "against base branch main", false},
		{"commit", "", "abc123", "introduced by commit abc123", false},
		{"base", "", "", "", true},
		{"commit", "", "", "", true},
		{"weird", "", "", "", true},
	}
	for _, c := range cases {
		got, err := ScopeSentence(c.scope, c.base, c.commit)
		if (err != nil) != c.wantErr {
			t.Errorf("scope=%q: err=%v wantErr=%v", c.scope, err, c.wantErr)
			continue
		}
		if !c.wantErr && !strings.Contains(got, c.want) {
			t.Errorf("scope=%q: %q should contain %q", c.scope, got, c.want)
		}
	}
}

func TestReviewPrompt(t *testing.T) {
	p, err := ReviewPrompt("base", "main", "", "focus on concurrency")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"base branch main", "focus on concurrency", "Do not modify", "git diff", "no issues"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt should contain %q:\n%s", want, p)
		}
	}
	if _, err := ReviewPrompt("commit", "", "", ""); err == nil {
		t.Error("commit without sha should error")
	}
	// Without a user prompt the text must not end with a dangling space.
	p, _ = ReviewPrompt("", "", "", "")
	if strings.HasSuffix(p, " ") {
		t.Errorf("prompt has trailing space: %q", p)
	}
}

func TestDefault(t *testing.T) {
	cases := []struct {
		client, want string
		ok           bool
	}{
		{"claude-ai", "codex", true},
		{"Claude Desktop", "codex", true},
		{"claude-code", "codex", true},
		{"codex-cli", "claude", true},
		{"Codex", "claude", true},
		{"mcp-inspector", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := Default(c.client)
		if got != c.want || ok != c.ok {
			t.Errorf("Default(%q) = %q,%v want %q,%v", c.client, got, ok, c.want, c.ok)
		}
	}
}

type stub struct{ name string }

func (s stub) Name() string                                             { return s.name }
func (s stub) Command() string                                          { return s.name }
func (s stub) ReviewArgs(_, _, _, _ string) ([]string, bool, error)     { return nil, false, nil }
func (s stub) ChatArgs(_, _ string) []string                            { return nil }
func (s stub) ParseChat(_ string) (string, string, error)               { return "", "", nil }

func TestRegistry(t *testing.T) {
	r := Registry{"codex": stub{"codex"}, "claude": stub{"claude"}}
	if names := strings.Join(r.Names(), ","); names != "claude,codex" {
		t.Errorf("Names = %q, want sorted claude,codex", names)
	}
	if _, err := r.Get("codex"); err != nil {
		t.Errorf("Get codex: %v", err)
	}
	if _, err := r.Get("gemini"); err == nil || !strings.Contains(err.Error(), "claude, codex") {
		t.Errorf("Get gemini should list valid agents, got %v", err)
	}
}

func TestLabelAndTail(t *testing.T) {
	if Label("codex") != "Codex" || Label("") != "" {
		t.Error("Label wrong")
	}
	if Tail("abcdef", 3) != "def" || Tail("ab", 3) != "ab" {
		t.Error("Tail wrong")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/`
Expected: FAIL — `no Go files` / undefined symbols.

- [ ] **Step 3: Implement `internal/agent/agent.go`**

```go
// Package agent defines the host-CLI "peer agent" abstraction shared by the
// codex and claude backends, plus helpers the server uses to pick a backend
// and build review prompts.
package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jerryctt/hostrunner-mcp/internal/exec"
)

// Kind is the type of background job a backend runs.
type Kind string

const (
	KindReview Kind = "review"
	KindChat   Kind = "chat"
)

// Runner executes a host command. Satisfied by exec.Runner and by test fakes.
type Runner interface {
	Run(ctx context.Context, req exec.Request) (exec.Result, error)
}

// Settings is the per-agent configuration a backend is built from.
type Settings struct {
	// Command is the executable name or absolute path (e.g. "codex",
	// "/opt/homebrew/bin/claude"). macOS GUI apps don't inherit the shell
	// PATH, so users may need an absolute path here.
	Command string
	// ExtraArgs are appended to every invocation (model overrides etc.).
	ExtraArgs []string
	// MCPServerName is the [mcp_servers.<name>] entry a codex peer should
	// disable so it cannot call back into hostrunner (recursion guard).
	// Only meaningful for the codex backend.
	MCPServerName string
}

// Backend builds argv for, and parses output of, one vendor's CLI.
// Implementations must never emit flags that allow the CLI to write files.
type Backend interface {
	Name() string
	Command() string
	// ReviewArgs returns argv (excluding the command) for a review of the
	// given scope. native=false means the CLI has no review subcommand; the
	// caller must then run ChatArgs(ReviewPrompt(...), "") instead. err
	// reports invalid scope/base/commit combinations in both cases.
	ReviewArgs(scope, base, commit, prompt string) (args []string, native bool, err error)
	// ChatArgs returns argv for one conversational turn. sessionID == ""
	// starts a new session; otherwise the turn continues that session.
	ChatArgs(message, sessionID string) []string
	// ParseChat extracts the assistant's reply and the session id from the
	// CLI's stdout.
	ParseChat(stdout string) (reply, sessionID string, err error)
}

// ScopeSentence describes the review scope in prose and validates the
// scope/base/commit combination. The wording is shared by both backends.
func ScopeSentence(scope, base, commit string) (string, error) {
	switch scope {
	case "", "uncommitted":
		return "Review the uncommitted changes (staged, unstaged, and untracked).", nil
	case "base":
		if base == "" {
			return "", fmt.Errorf("mode=base requires a base ref")
		}
		return fmt.Sprintf("Review the changes against base branch %s.", base), nil
	case "commit":
		if commit == "" {
			return "", fmt.Errorf("mode=commit requires a commit sha")
		}
		return fmt.Sprintf("Review the changes introduced by commit %s.", commit), nil
	default:
		return "", fmt.Errorf("unknown mode %q", scope)
	}
}

const reviewInstructions = "You are a read-only code reviewer working in the current repository. " +
	"Use `git status`, `git diff`, `git log` and `git show` to see the changes, and read surrounding files as needed. " +
	"Do not modify anything. " +
	"Report concrete bugs, regressions, security problems and risky assumptions, each with file path and line reference, ordered by severity, and explain briefly why each matters. " +
	"Skip style nits unless asked. If you find no issues, say so explicitly."

// ReviewPrompt composes the prompt used when a backend has no native review
// command (claude). It validates scope like ScopeSentence.
func ReviewPrompt(scope, base, commit, userPrompt string) (string, error) {
	s, err := ScopeSentence(scope, base, commit)
	if err != nil {
		return "", err
	}
	p := s + " " + reviewInstructions
	if strings.TrimSpace(userPrompt) != "" {
		p += " Additional instructions from the requester: " + strings.TrimSpace(userPrompt)
	}
	return p, nil
}

// Default infers which peer to talk to from the MCP client's reported name:
// a Claude client talks to codex and a Codex client talks to claude.
func Default(clientName string) (string, bool) {
	n := strings.ToLower(clientName)
	switch {
	case strings.Contains(n, "claude"):
		return "codex", true
	case strings.Contains(n, "codex"):
		return "claude", true
	}
	return "", false
}

// Registry maps agent names to backends.
type Registry map[string]Backend

// Names returns the registered agent names, sorted.
func (r Registry) Names() []string {
	names := make([]string, 0, len(r))
	for n := range r {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Get returns the backend for name or an error listing the valid names.
func (r Registry) Get(name string) (Backend, error) {
	if b, ok := r[name]; ok {
		return b, nil
	}
	return nil, fmt.Errorf("unknown agent %q; valid agents: %s", name, strings.Join(r.Names(), ", "))
}

// Label capitalises an agent name for result headers ("codex" -> "Codex").
func Label(name string) string {
	if name == "" {
		return ""
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

// Tail returns the last n bytes of s, for diagnostics.
func Tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/agent/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/agent
git commit -m "feat(agent): backend interface, scope/review prompt helpers, peer inference"
```

---

### Task 2: Config — `agents:` map with defaults and legacy `codex_extra_args` merge

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

**Interfaces:**
- Produces:
  ```go
  type AgentConfig struct { Command string `yaml:"command"`; ExtraArgs []string `yaml:"extra_args"`; MCPServerName string `yaml:"mcp_server_name"` }
  // Config gains: Agents map[string]AgentConfig `yaml:"agents"`
  func (c *Config) Agent(name string) AgentConfig   // always returns a filled struct for codex/claude
  func DefaultAgents() map[string]AgentConfig
  ```
- Defaults: codex → `{Command: "codex", MCPServerName: "hostrunner"}`, claude → `{Command: "claude"}`.

- [ ] **Step 1: Add failing tests to `config_test.go`**

```go
func TestAgentsDefaults(t *testing.T) {
	root := t.TempDir()
	p := writeTemp(t, "allowed_roots:\n  - "+root+"\n")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Agent("codex"); got.Command != "codex" || got.MCPServerName != "hostrunner" {
		t.Errorf("codex defaults = %+v", got)
	}
	if got := c.Agent("claude"); got.Command != "claude" {
		t.Errorf("claude defaults = %+v", got)
	}
}

func TestAgentsOverrideAndLegacyMerge(t *testing.T) {
	root := t.TempDir()
	p := writeTemp(t, "allowed_roots:\n  - "+root+"\n"+
		"codex_extra_args: [\"-c\", \"model=o3\"]\n"+
		"agents:\n"+
		"  codex:\n    command: /opt/homebrew/bin/codex\n    mcp_server_name: hr\n"+
		"  claude:\n    command: /usr/local/bin/claude\n    extra_args: [\"--model\", \"opus\"]\n")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	cx := c.Agent("codex")
	if cx.Command != "/opt/homebrew/bin/codex" || cx.MCPServerName != "hr" {
		t.Errorf("codex override = %+v", cx)
	}
	if strings.Join(cx.ExtraArgs, " ") != "-c model=o3" {
		t.Errorf("legacy codex_extra_args not merged: %v", cx.ExtraArgs)
	}
	cl := c.Agent("claude")
	if cl.Command != "/usr/local/bin/claude" || strings.Join(cl.ExtraArgs, " ") != "--model opus" {
		t.Errorf("claude override = %+v", cl)
	}
}

func TestAgentsLegacyDoesNotDuplicate(t *testing.T) {
	root := t.TempDir()
	p := writeTemp(t, "allowed_roots:\n  - "+root+"\n"+
		"codex_extra_args: [\"-c\", \"a=1\"]\n"+
		"agents:\n  codex:\n    extra_args: [\"-c\", \"b=2\"]\n")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.Agent("codex").ExtraArgs, " "); got != "-c b=2 -c a=1" {
		t.Errorf("merge order = %q, want agents first then legacy", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/config/`
Expected: FAIL — `c.Agent undefined`.

- [ ] **Step 3: Implement**

Add to `config.go` (after the `Config` struct, and modify `Load`):

```go
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
// field by field so a partial `agents:` entry still works.
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
```

Add the field to `Config`:

```go
	Agents          map[string]AgentConfig `yaml:"agents"`
```

In `Load`, after the `StreamOutput` default block, add:

```go
	if c.Agents == nil {
		c.Agents = map[string]AgentConfig{}
	}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/config/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -m "feat(config): per-agent settings with defaults and legacy codex_extra_args merge"
```

---

### Task 3: Codex backend — implement `agent.Backend`, add chat + JSONL parsing, drop `Review()`

**Files:**
- Modify: `internal/codex/codex.go`
- Modify: `internal/codex/codex_test.go`

**Interfaces:**
- Consumes: `agent.Settings`, `agent.Backend`, `agent.ScopeSentence`, `agent.Tail`.
- Produces:
  ```go
  type Backend struct{ ... }
  func New(s agent.Settings) *Backend
  // keeps: func ReviewArgs(mode, base, commit, prompt string) (args []string, positional string, err error)
  // removes: Runner, ReviewParams, ReviewResult, Review()
  ```
- Chat argv (new session): `exec --json --sandbox read-only --skip-git-repo-check -c mcp_servers.<name>.enabled=false [extra...] <message>`
- Chat argv (resume): `exec resume --json --sandbox read-only --skip-git-repo-check -c mcp_servers.<name>.enabled=false [extra...] <sessionID> <message>`

- [ ] **Step 1: Replace the `Review*` tests in `codex_test.go` with backend tests**

Delete `TestReviewHappyPath`, `TestReviewBaseMode`, `TestReviewExtraArgs`, `TestReviewWithPrompt` and the `fakeRunner` type (no longer needed). Keep `TestReviewArgs` and `TestReviewArgsPromptSuppressesScopeFlags`. Append:

```go
func newBackend() *Backend {
	return New(agent.Settings{Command: "codex", MCPServerName: "hostrunner"})
}

func TestBackendReviewArgsNativeWithExtraArgs(t *testing.T) {
	b := New(agent.Settings{Command: "codex", ExtraArgs: []string{"-c", "model=o3"}})
	args, native, err := b.ReviewArgs("uncommitted", "", "", "")
	if err != nil || !native {
		t.Fatalf("err=%v native=%v", err, native)
	}
	if got := strings.Join(args, " "); got != "review --uncommitted -c model=o3" {
		t.Errorf("args = %q", got)
	}
	// With a prompt the positional goes last, after extra args.
	args, _, _ = b.ReviewArgs("base", "main", "", "focus on auth")
	if args[0] != "review" || args[len(args)-1] == "model=o3" || !strings.Contains(args[len(args)-1], "focus on auth") {
		t.Errorf("prompt should be the final positional: %v", args)
	}
	for _, a := range args {
		if a == "--write" || a == "--full-auto" || a == "--dangerously-bypass-approvals-and-sandbox" {
			t.Errorf("read-only check: disallowed arg %q", a)
		}
	}
	if _, _, err := b.ReviewArgs("commit", "", "", ""); err == nil {
		t.Error("commit without sha should error")
	}
}

func TestBackendChatArgs(t *testing.T) {
	b := newBackend()
	got := strings.Join(b.ChatArgs("hello there", ""), " ")
	want := "exec --json --sandbox read-only --skip-git-repo-check -c mcp_servers.hostrunner.enabled=false hello there"
	if got != want {
		t.Errorf("new session:\n got %q\nwant %q", got, want)
	}
	got = strings.Join(b.ChatArgs("more", "thread-123"), " ")
	want = "exec resume --json --sandbox read-only --skip-git-repo-check -c mcp_servers.hostrunner.enabled=false thread-123 more"
	if got != want {
		t.Errorf("resume:\n got %q\nwant %q", got, want)
	}
	// No MCP server name -> no -c override.
	b2 := New(agent.Settings{Command: "codex", ExtraArgs: []string{"-m", "o3"}})
	got = strings.Join(b2.ChatArgs("x", ""), " ")
	if strings.Contains(got, "mcp_servers") || !strings.HasSuffix(got, "-m o3 x") {
		t.Errorf("args = %q", got)
	}
}

const codexJSONL = `{"type":"thread.started","thread_id":"0199a-thread"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_1","type":"reasoning","text":"thinking"}}
{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"First part."}}
{"type":"item.completed","item":{"id":"item_3","type":"agent_message","text":"Second part."}}
{"type":"turn.completed","usage":{"input_tokens":1}}
`

func TestBackendParseChat(t *testing.T) {
	reply, sid, err := newBackend().ParseChat(codexJSONL)
	if err != nil {
		t.Fatal(err)
	}
	if sid != "0199a-thread" {
		t.Errorf("session = %q", sid)
	}
	if reply != "First part.\n\nSecond part." {
		t.Errorf("reply = %q", reply)
	}
}

func TestBackendParseChatErrors(t *testing.T) {
	b := newBackend()
	// No agent message at all.
	_, sid, err := b.ParseChat(`{"type":"thread.started","thread_id":"t1"}` + "\n" + `{"type":"turn.failed","error":{"message":"boom"}}` + "\n")
	if err == nil || !strings.Contains(err.Error(), "boom") || sid != "t1" {
		t.Errorf("want error mentioning boom and session t1, got err=%v sid=%q", err, sid)
	}
	// Garbage / non-JSON lines are skipped, not fatal.
	_, _, err = b.ParseChat("warning: something\n" + codexJSONL)
	if err != nil {
		t.Errorf("non-JSON prefix should be ignored: %v", err)
	}
	if _, _, err = b.ParseChat(""); err == nil {
		t.Error("empty output should error")
	}
}
```

Add `"github.com/jerryctt/hostrunner-mcp/internal/agent"` to the test imports; remove `context` and `exec` imports if now unused.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/codex/`
Expected: FAIL — `undefined: New`, `undefined: Backend`.

- [ ] **Step 3: Rewrite `internal/codex/codex.go`**

```go
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
// JSONL so ParseChat can recover the thread id. The hostrunner MCP server
// entry is disabled inside the peer so it cannot call back into us.
func (b *Backend) ChatArgs(message, sessionID string) []string {
	args := []string{"exec"}
	if sessionID != "" {
		args = append(args, "resume")
	}
	args = append(args, "--json", "--sandbox", "read-only", "--skip-git-repo-check")
	if b.s.MCPServerName != "" {
		args = append(args, "-c", fmt.Sprintf("mcp_servers.%s.enabled=false", b.s.MCPServerName))
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
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/codex/`
Expected: `ok`. (`./internal/server` will not compile yet — that is expected until Task 5/6.)

- [ ] **Step 5: Commit**

```bash
git add internal/codex
git commit -m "feat(codex): implement agent.Backend with read-only exec chat and JSONL parsing"
```

---

### Task 4: Claude backend

**Files:**
- Create: `internal/claude/claude.go`
- Create: `internal/claude/claude_test.go`

**Interfaces:**
- Produces: `type Backend`, `func New(s agent.Settings) *Backend` implementing `agent.Backend` with `Name() == "claude"`, `ReviewArgs → native=false`.
- Chat argv: `-p <message> --bare --output-format json --permission-mode dontAsk --tools Read,Grep,Glob,Bash --allowedTools Read,Grep,Glob,Bash(git diff:*),Bash(git log:*),Bash(git status:*),Bash(git show:*) --disallowedTools Edit,Write,NotebookEdit [--resume <id>] [extra...]`. The message is placed right after `-p` so later list-valued flags can never swallow it.

- [ ] **Step 1: Write failing tests**

```go
package claude

import (
	"strings"
	"testing"

	"github.com/jerryctt/hostrunner-mcp/internal/agent"
)

func TestReviewArgsIsNotNative(t *testing.T) {
	b := New(agent.Settings{Command: "claude"})
	args, native, err := b.ReviewArgs("uncommitted", "", "", "")
	if err != nil || native || args != nil {
		t.Errorf("want nil,false,nil got %v,%v,%v", args, native, err)
	}
	if _, _, err := b.ReviewArgs("base", "", "", ""); err == nil {
		t.Error("base without ref should error")
	}
}

func TestChatArgs(t *testing.T) {
	b := New(agent.Settings{Command: "claude", ExtraArgs: []string{"--model", "opus"}})
	args := b.ChatArgs("critique this", "")
	if args[0] != "-p" || args[1] != "critique this" {
		t.Errorf("message must directly follow -p: %v", args)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--bare", "--output-format json", "--permission-mode dontAsk",
		"--tools Read,Grep,Glob,Bash",
		"--allowedTools Read,Grep,Glob,Bash(git diff:*),Bash(git log:*),Bash(git status:*),Bash(git show:*)",
		"--disallowedTools Edit,Write,NotebookEdit",
		"--model opus",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %q", want, joined)
		}
	}
	if strings.Contains(joined, "--resume") || strings.Contains(joined, "dangerously") || strings.Contains(joined, "acceptEdits") {
		t.Errorf("unexpected flag in %q", joined)
	}
	resumed := strings.Join(b.ChatArgs("again", "sess-1"), " ")
	if !strings.Contains(resumed, "--resume sess-1") {
		t.Errorf("resume missing: %q", resumed)
	}
}

func TestParseChat(t *testing.T) {
	b := New(agent.Settings{Command: "claude"})
	out := `{"type":"result","subtype":"success","is_error":false,"duration_ms":5,"result":"Looks fine.\nNo issues.","session_id":"abc-123","total_cost_usd":0.01}`
	reply, sid, err := b.ParseChat(out)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "Looks fine.\nNo issues." || sid != "abc-123" {
		t.Errorf("reply=%q sid=%q", reply, sid)
	}
	// Leading non-JSON noise is tolerated.
	if _, sid, err := b.ParseChat("note: x\n" + out); err != nil || sid != "abc-123" {
		t.Errorf("prefix noise: err=%v sid=%q", err, sid)
	}
	// is_error surfaces as an error carrying the message and session id.
	_, sid, err = b.ParseChat(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"rate limited","session_id":"s9"}`)
	if err == nil || !strings.Contains(err.Error(), "rate limited") || sid != "s9" {
		t.Errorf("is_error: err=%v sid=%q", err, sid)
	}
	if _, _, err := b.ParseChat("not json at all"); err == nil {
		t.Error("garbage should error")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/claude/`
Expected: FAIL — package has no non-test files.

- [ ] **Step 3: Implement `internal/claude/claude.go`**

```go
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

// ChatArgs runs one print-mode turn. --bare skips the user's MCP servers,
// plugins, hooks and skills, which both speeds startup and prevents the peer
// from calling hostrunner recursively. The message is passed immediately
// after -p so that no later flag can consume it as a value.
func (b *Backend) ChatArgs(message, sessionID string) []string {
	args := []string{
		"-p", message,
		"--bare",
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
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/claude/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/claude
git commit -m "feat(claude): read-only claude -p backend with JSON parsing and --resume"
```

---

### Task 5: Job store — generic jobs keyed by (worktree root, agent, kind)

**Files:**
- Modify: `internal/server/jobs.go`
- Modify: `internal/server/server_test.go` (only the job-store tests; handler tests are rewritten in Task 6 — expect the package to stay red until Task 6 is done)

**Interfaces:**
- Produces:
  ```go
  type job struct { ID, Folder, Root, Agent string; Kind agent.Kind; Mode string; Started time.Time; /* unexported: done, output, isErr, ended */ }
  func (s *jobStore) add(folder, agentName string, kind agent.Kind, mode string) (*job, error)
  ```
  `get`, `gcLocked`, `shutdown`, `worktreeRoot`, `newJobID`, `keepFinished` unchanged.

- [ ] **Step 1: Update job-store tests in `server_test.go`**

Replace `TestJobStoreExpiresFinishedJobsOnGet` and add a duplicate-key test:

```go
func TestJobStoreExpiresFinishedJobsOnGet(t *testing.T) {
	jobs := newJobStore()
	j, err := jobs.add("/repo", "codex", agent.KindReview, "uncommitted")
	if err != nil {
		t.Fatal(err)
	}
	j.finish("done", false)
	j.ended = time.Now().Add(-keepFinished - time.Minute)

	if _, ok := jobs.get(j.ID); ok {
		t.Errorf("job older than keepFinished should be expired on get")
	}
}

func TestJobStoreDuplicateKeyIsRootAgentKind(t *testing.T) {
	jobs := newJobStore()
	if _, err := jobs.add("/repo", "codex", agent.KindReview, "uncommitted"); err != nil {
		t.Fatal(err)
	}
	// Same root+agent+kind -> refused.
	if _, err := jobs.add("/repo", "codex", agent.KindReview, "base"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("duplicate codex review should be refused, got %v", err)
	}
	// Different agent or kind on the same root -> allowed.
	if _, err := jobs.add("/repo", "claude", agent.KindReview, "uncommitted"); err != nil {
		t.Errorf("claude review alongside codex review should be allowed: %v", err)
	}
	if _, err := jobs.add("/repo", "codex", agent.KindChat, ""); err != nil {
		t.Errorf("codex chat alongside codex review should be allowed: %v", err)
	}
}
```

Add `"github.com/jerryctt/hostrunner-mcp/internal/agent"` to the test imports.

- [ ] **Step 2: Modify `jobs.go`**

Rename `reviewJob` → `job`, add fields and the new `add` signature:

```go
// job is a background peer-agent invocation (review or chat). The
// output/isErr/ended fields are written exactly once by finish() before done
// is closed; readers must only access them after observing <-done.
type job struct {
	ID      string
	Folder  string
	Root    string // worktree root; part of the duplicate-detection key
	Agent   string
	Kind    agent.Kind
	Mode    string // review scope label, or "" for chat
	Started time.Time

	done   chan struct{}
	output string
	isErr  bool
	ended  time.Time
}
```

```go
// add registers a new job. It refuses to start a second job with the same
// (worktree root, agent, kind) while one is running: each job spawns a
// long-running CLI process on the host, so retries or impatient clients must
// poll the existing job instead of stacking processes. Different agents or
// kinds on the same repo (a codex review plus a claude chat) may run together.
func (s *jobStore) add(folder, agentName string, kind agent.Kind, mode string) (*job, error) {
	root := worktreeRoot(folder)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked()
	for _, j := range s.jobs {
		if j.Root == root && j.Agent == agentName && j.Kind == kind && !j.finished() {
			return nil, fmt.Errorf(
				"a %s %s of %s is already running (job %s, started %s ago); poll job_status with that job_id instead of starting another",
				agentName, kind, j.Folder, j.ID, time.Since(j.Started).Round(time.Second),
			)
		}
	}
	j := &job{
		ID:      newJobID(),
		Folder:  folder,
		Root:    root,
		Agent:   agentName,
		Kind:    kind,
		Mode:    mode,
		Started: time.Now(),
		done:    make(chan struct{}),
	}
	s.jobs[j.ID] = j
	return j, nil
}
```

Update every `*reviewJob` / `map[string]*reviewJob` mention in the file to `*job` / `map[string]*job`, and add the import `"github.com/jerryctt/hostrunner-mcp/internal/agent"`.

- [ ] **Step 3: Compile check**

Run: `go vet ./internal/server/`
Expected: errors only in `server.go` / handler tests (old `jobs.add(dir, label)` call and `HandleReviewStatus` references) — these are fixed in Task 6. Do **not** commit yet.

---

### Task 6: Server `Service` — review/chat/status handlers for any backend

**Files:**
- Modify: `internal/server/server.go` (handlers; tool registration is Task 7)
- Modify: `internal/server/server_test.go` (rewrite handler tests)

**Interfaces:**
- Consumes: `agent.Registry`, `agent.Backend`, `codex.New`, `claude.New`, `config.Config.Agent`, `jobStore.add(folder, agent, kind, mode)`.
- Produces:
  ```go
  type Service struct { Cfg *config.Config; Runner agent.Runner; Log zerolog.Logger; Jobs *jobStore; Backends agent.Registry }
  func NewService(cfg *config.Config, r agent.Runner, log zerolog.Logger) *Service
  func ResolveAgent(explicit, clientName string, reg agent.Registry) (agent.Backend, error)
  func (s *Service) ReviewStart(ctx context.Context, agentName, folder, scope, base, commit, prompt string) (string, bool)
  func (s *Service) ChatStart(ctx context.Context, agentName, folder, message, sessionID string) (string, bool)
  func (s *Service) JobStatus(ctx context.Context, jobID string, wait time.Duration) (string, bool)
  func (s *Service) RunCommand(ctx context.Context, command string, args []string, folder string) (string, bool)
  func (s *Service) runReview(ctx context.Context, b agent.Backend, dir, scope, base, commit, prompt string) (string, bool)
  func (s *Service) runChat(ctx context.Context, b agent.Backend, dir, message, sessionID string) (string, bool)
  ```
- Result formats:
  - review: `Mode: <scope> (<agent> exit N)\n\n--- <Agent> review ---\n<text>\n` (+ stderr section when exit≠0, timeout/truncation markers as today)
  - chat: `session_id: <id>\n\n--- <Agent> reply ---\n<text>\n`
  - status header: `status: completed (job <id>, agent <agent>, kind <kind>, elapsed <d>)\n\n`
  - running: `status: running (job <id>, agent <agent>, kind <kind>, elapsed <d>)\nStill in progress. Call job_status again with the same job_id.`

- [ ] **Step 1: Rewrite handler tests in `server_test.go`**

Keep `fakeRunner`, `newCfg`, `mustResolve`, `blockingRunner`, and the two job-store tests from Task 5. Change `newCfg` to also allow `claude` and drop `CodexExtraArgs`:

```go
	return &config.Config{
		AllowedRoots:    []string{root},
		AllowedCommands: []string{"codex", "claude", "git"},
		MaxOutputBytes:  1000,
	}, repo
```

Add a helper and replace all other tests:

```go
func newSvc(t *testing.T, r agent.Runner) (*Service, string) {
	cfg, repo := newCfg(t)
	return NewService(cfg, r, zerolog.Nop()), repo
}

func jobID(t *testing.T, out string) string {
	t.Helper()
	parts := strings.SplitAfter(out, "job_id: ")
	if len(parts) < 2 {
		t.Fatalf("output missing job_id: %q", out)
	}
	return strings.TrimSpace(strings.Split(parts[1], "\n")[0])
}

func waitStatus(t *testing.T, s *Service, id string) (string, bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, isErr := s.JobStatus(context.Background(), id, 50*time.Millisecond)
		if isErr || !strings.Contains(out, "status: running") {
			return out, isErr
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s did not finish in time", id)
		}
	}
}

const claudeJSON = `{"type":"result","subtype":"success","is_error":false,"result":"CLAUDE_SAYS","session_id":"sess-42"}`
const codexJSONL = "{\"type\":\"thread.started\",\"thread_id\":\"thr-7\"}\n{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"CODEX_SAYS\"}}\n"

func TestResolveAgent(t *testing.T) {
	reg := agent.Registry{"codex": codex.New(agent.Settings{Command: "codex"}), "claude": claude.New(agent.Settings{Command: "claude"})}
	if b, err := ResolveAgent("claude", "codex-cli", reg); err != nil || b.Name() != "claude" {
		t.Errorf("explicit wins: %v %v", b, err)
	}
	if b, err := ResolveAgent("", "claude-ai", reg); err != nil || b.Name() != "codex" {
		t.Errorf("claude client -> codex: %v %v", b, err)
	}
	if b, err := ResolveAgent("", "Codex Desktop", reg); err != nil || b.Name() != "claude" {
		t.Errorf("codex client -> claude: %v %v", b, err)
	}
	if _, err := ResolveAgent("", "mcp-inspector", reg); err == nil || !strings.Contains(err.Error(), "specify agent") {
		t.Errorf("unknown client must ask for explicit agent, got %v", err)
	}
	if _, err := ResolveAgent("gemini", "claude-ai", reg); err == nil || !strings.Contains(err.Error(), "valid agents") {
		t.Errorf("unknown agent error: %v", err)
	}
}

func TestReviewStartCodexNative(t *testing.T) {
	s, repo := newSvc(t, &fakeRunner{reply: map[string]exec.Result{
		"codex review --uncommitted": {Stdout: "VERDICT_OK", Stderr: "NOISE_TRACE", ExitCode: 0},
	}})
	out, isErr := s.ReviewStart(context.Background(), "codex", repo, "uncommitted", "", "", "")
	if isErr {
		t.Fatalf("start: %q", out)
	}
	got, isErr := waitStatus(t, s, jobID(t, out))
	if isErr {
		t.Fatalf("status: %q", got)
	}
	for _, want := range []string{"status: completed", "agent codex", "kind review", "Mode: uncommitted (codex exit 0)", "--- Codex review ---", "VERDICT_OK"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "NOISE_TRACE") {
		t.Errorf("stderr must be hidden on success: %q", got)
	}
}

func TestReviewStartClaudeViaPrompt(t *testing.T) {
	r := &recordingRunner{reply: exec.Result{Stdout: claudeJSON}}
	s, repo := newSvc(t, r)
	out, isErr := s.ReviewStart(context.Background(), "claude", repo, "base", "main", "", "check auth")
	if isErr {
		t.Fatalf("start: %q", out)
	}
	got, isErr := waitStatus(t, s, jobID(t, out))
	if isErr {
		t.Fatalf("status: %q", got)
	}
	if !strings.Contains(got, "--- Claude review ---") || !strings.Contains(got, "CLAUDE_SAYS") || !strings.Contains(got, "Mode: base (claude exit 0)") {
		t.Errorf("got %q", got)
	}
	req := r.last()
	if req.Command != "claude" || req.Args[0] != "-p" {
		t.Errorf("claude invocation = %v", req)
	}
	if !strings.Contains(req.Args[1], "base branch main") || !strings.Contains(req.Args[1], "check auth") {
		t.Errorf("review prompt = %q", req.Args[1])
	}
}

func TestChatStartNewAndResume(t *testing.T) {
	r := &recordingRunner{reply: exec.Result{Stdout: codexJSONL}}
	s, repo := newSvc(t, r)
	out, isErr := s.ChatStart(context.Background(), "codex", repo, "hello", "")
	if isErr {
		t.Fatalf("start: %q", out)
	}
	got, isErr := waitStatus(t, s, jobID(t, out))
	if isErr {
		t.Fatalf("status: %q", got)
	}
	for _, want := range []string{"kind chat", "session_id: thr-7", "--- Codex reply ---", "CODEX_SAYS"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if a := strings.Join(r.last().Args, " "); !strings.HasPrefix(a, "exec --json --sandbox read-only") || !strings.HasSuffix(a, " hello") {
		t.Errorf("new-session args = %q", a)
	}

	out, isErr = s.ChatStart(context.Background(), "codex", repo, "more", "thr-7")
	if isErr {
		t.Fatalf("resume start: %q", out)
	}
	if _, isErr = waitStatus(t, s, jobID(t, out)); isErr {
		t.Fatal("resume failed")
	}
	if a := strings.Join(r.last().Args, " "); !strings.HasPrefix(a, "exec resume --json") || !strings.HasSuffix(a, " thr-7 more") {
		t.Errorf("resume args = %q", a)
	}
}

func TestChatParseFailureIsJobError(t *testing.T) {
	s, repo := newSvc(t, &recordingRunner{reply: exec.Result{Stdout: "garbage", Stderr: "BOOM", ExitCode: 1}})
	out, _ := s.ChatStart(context.Background(), "claude", repo, "hi", "")
	got, isErr := waitStatus(t, s, jobID(t, out))
	if !isErr || !strings.Contains(got, "BOOM") || !strings.Contains(got, "claude exit 1") {
		t.Errorf("want error with stderr and exit code, got %q isErr=%v", got, isErr)
	}
}

func TestStartValidatesUpfront(t *testing.T) {
	s, repo := newSvc(t, &fakeRunner{})
	if out, isErr := s.ReviewStart(context.Background(), "codex", "/etc", "uncommitted", "", "", ""); !isErr || !strings.Contains(out, "allowed_root") {
		t.Errorf("outside root: %q %v", out, isErr)
	}
	if out, isErr := s.ReviewStart(context.Background(), "codex", repo, "base", "", "", ""); !isErr || !strings.Contains(out, "base") {
		t.Errorf("bad scope: %q %v", out, isErr)
	}
	if out, isErr := s.ReviewStart(context.Background(), "claude", repo, "commit", "", "", ""); !isErr || !strings.Contains(out, "commit") {
		t.Errorf("bad scope claude: %q %v", out, isErr)
	}
	if out, isErr := s.ChatStart(context.Background(), "codex", repo, "   ", ""); !isErr || !strings.Contains(out, "message") {
		t.Errorf("empty message: %q %v", out, isErr)
	}
	if out, isErr := s.ChatStart(context.Background(), "", repo, "hi", ""); !isErr || !strings.Contains(out, "specify agent") {
		t.Errorf("no client info and no agent: %q %v", out, isErr)
	}
}

func TestReviewStartRejectsConcurrentJobForSameWorktree(t *testing.T) {
	s, repo := newSvc(t, &fakeRunner{})
	for _, d := range []string{".git", "internal", "cmd"} {
		if err := os.Mkdir(filepath.Join(repo, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	j, err := s.Jobs.add(mustResolve(t, s.Cfg, filepath.Join(repo, "internal")), "codex", agent.KindReview, "uncommitted")
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Join(repo, "internal"), repo, filepath.Join(repo, "cmd")} {
		out, isErr := s.ReviewStart(context.Background(), "codex", dir, "uncommitted", "", "", "")
		if !isErr || !strings.Contains(out, "already running") || !strings.Contains(out, j.ID) {
			t.Errorf("dir %s: expected duplicate-job rejection mentioning %s, got %q isErr=%v", dir, j.ID, out, isErr)
		}
	}
	j.finish("done", false)
	if out, isErr := s.ReviewStart(context.Background(), "codex", repo, "uncommitted", "", "", ""); isErr {
		t.Errorf("expected new job after previous finished, got %q", out)
	}
}

func TestJobStoreShutdownCancelsRunningJobs(t *testing.T) {
	r := &blockingRunner{started: make(chan struct{})}
	s, repo := newSvc(t, r)
	if out, isErr := s.ReviewStart(context.Background(), "codex", repo, "uncommitted", "", "", ""); isErr {
		t.Fatalf("start: %q", out)
	}
	<-r.started
	done := make(chan struct{})
	go func() { s.Jobs.shutdown(2 * time.Second); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not return; running job was not cancelled")
	}
}

func TestJobStatusUnknownJob(t *testing.T) {
	s, _ := newSvc(t, &fakeRunner{})
	out, isErr := s.JobStatus(context.Background(), "nope", time.Millisecond)
	if !isErr || !strings.Contains(out, "unknown job_id") {
		t.Errorf("expected unknown job error, got %q isErr=%v", out, isErr)
	}
}

func TestRunCommand(t *testing.T) {
	s, repo := newSvc(t, &fakeRunner{reply: map[string]exec.Result{"git status": {Stdout: "clean"}}})
	if out, isErr := s.RunCommand(context.Background(), "rm", []string{"-rf", "/"}, repo); !isErr || !strings.Contains(out, "not allowed") {
		t.Errorf("expected command rejection, got %q isErr=%v", out, isErr)
	}
	if out, isErr := s.RunCommand(context.Background(), "git", []string{"status"}, repo); isErr || !strings.Contains(out, "clean") {
		t.Errorf("expected success, got %q isErr=%v", out, isErr)
	}
}

// recordingRunner returns one canned result and remembers the last request.
type recordingRunner struct {
	mu    sync.Mutex
	reply exec.Result
	reqs  []exec.Request
}

func (r *recordingRunner) Run(_ context.Context, req exec.Request) (exec.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
	return r.reply, nil
}

func (r *recordingRunner) last() exec.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reqs[len(r.reqs)-1]
}
```

Imports for the test file: `context, os, path/filepath, strings, sync, testing, time`, plus `internal/agent`, `internal/claude`, `internal/codex`, `internal/config`, `internal/exec`, `zerolog`.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/server/`
Expected: FAIL — `undefined: NewService`, `ResolveAgent`, etc.

- [ ] **Step 3: Rewrite the handler half of `server.go`**

Replace everything in `server.go` **below** the `New(...)` function (which Task 7 rewrites) with:

```go
// Service holds the dependencies the tool handlers need.
type Service struct {
	Cfg      *config.Config
	Runner   agent.Runner
	Log      zerolog.Logger
	Jobs     *jobStore
	Backends agent.Registry
}

// NewService wires the codex and claude backends from cfg.
func NewService(cfg *config.Config, r agent.Runner, log zerolog.Logger) *Service {
	cx, cl := cfg.Agent("codex"), cfg.Agent("claude")
	return &Service{
		Cfg: cfg, Runner: r, Log: log, Jobs: newJobStore(),
		Backends: agent.Registry{
			"codex":  codex.New(agent.Settings{Command: cx.Command, ExtraArgs: cx.ExtraArgs, MCPServerName: cx.MCPServerName}),
			"claude": claude.New(agent.Settings{Command: cl.Command, ExtraArgs: cl.ExtraArgs}),
		},
	}
}

// ResolveAgent picks the backend: an explicit name wins; otherwise the peer
// is inferred from the MCP client's name (Claude talks to codex, Codex talks
// to claude).
func ResolveAgent(explicit, clientName string, reg agent.Registry) (agent.Backend, error) {
	name := explicit
	if name == "" {
		inferred, ok := agent.Default(clientName)
		if !ok {
			return nil, fmt.Errorf("cannot infer which agent to use from MCP client %q; specify agent (one of: %s)", clientName, strings.Join(reg.Names(), ", "))
		}
		name = inferred
	}
	return reg.Get(name)
}

// clientName returns the MCP client's reported name for this call, or "".
func clientName(ctx context.Context) string {
	sess := mcpserver.ClientSessionFromContext(ctx)
	if sess == nil {
		return ""
	}
	if ci, ok := sess.(mcpserver.SessionWithClientInfo); ok {
		return ci.GetClientInfo().Name
	}
	return ""
}

// ReviewStart validates the request, then runs the review in a background
// goroutine so the MCP call returns immediately. The goroutine deliberately
// does not use the request context (it is cancelled as soon as this call
// returns); it runs under the job store's server-lifetime context so
// shutdown() can still cancel it.
func (s *Service) ReviewStart(ctx context.Context, agentName, folder, scope, base, commit, prompt string) (string, bool) {
	b, err := ResolveAgent(agentName, clientName(ctx), s.Backends)
	if err != nil {
		return err.Error(), true
	}
	dir, err := s.Cfg.ResolveAllowedDir(folder)
	if err != nil {
		return err.Error(), true
	}
	// Catch bad scope/base/commit combinations now, not minutes later.
	if _, _, err := b.ReviewArgs(scope, base, commit, prompt); err != nil {
		return err.Error(), true
	}
	label := scope
	if label == "" {
		label = "uncommitted"
	}
	j, err := s.Jobs.add(dir, b.Name(), agent.KindReview, label)
	if err != nil {
		return err.Error(), true
	}
	s.Log.Info().Str("tool", "review_start").Str("job", j.ID).Str("agent", b.Name()).Str("folder", dir).Str("mode", label).Msg("invocation")
	go func() {
		out, isErr := s.runReview(s.Jobs.ctx, b, dir, scope, base, commit, prompt)
		j.finish(out, isErr)
	}()
	return fmt.Sprintf(
		"Review started in the background.\njob_id: %s\nagent: %s\nmode: %s\nfolder: %s\n\nCall job_status with this job_id to get the result. Reviews typically take 1-10 minutes; the server-side limit is %s (config 'timeout').",
		j.ID, b.Name(), label, dir, s.Cfg.Timeout,
	), false
}

// ChatStart starts one conversational turn with the peer agent as a
// background job. sessionID continues an earlier conversation.
func (s *Service) ChatStart(ctx context.Context, agentName, folder, message, sessionID string) (string, bool) {
	b, err := ResolveAgent(agentName, clientName(ctx), s.Backends)
	if err != nil {
		return err.Error(), true
	}
	dir, err := s.Cfg.ResolveAllowedDir(folder)
	if err != nil {
		return err.Error(), true
	}
	if strings.TrimSpace(message) == "" {
		return "message must not be empty", true
	}
	j, err := s.Jobs.add(dir, b.Name(), agent.KindChat, "")
	if err != nil {
		return err.Error(), true
	}
	s.Log.Info().Str("tool", "chat_start").Str("job", j.ID).Str("agent", b.Name()).Str("folder", dir).Bool("resume", sessionID != "").Msg("invocation")
	go func() {
		out, isErr := s.runChat(s.Jobs.ctx, b, dir, message, sessionID)
		j.finish(out, isErr)
	}()
	cont := "a new conversation"
	if sessionID != "" {
		cont = "continuing session " + sessionID
	}
	return fmt.Sprintf(
		"Chat turn started in the background (%s).\njob_id: %s\nagent: %s\nfolder: %s\n\nCall job_status with this job_id to get the reply; it includes the session_id to pass to the next chat_start.",
		cont, j.ID, b.Name(), dir,
	), false
}

// JobStatus reports on a background job, blocking up to wait for it to
// finish (long-poll) so callers need fewer round trips.
func (s *Service) JobStatus(ctx context.Context, jobID string, wait time.Duration) (string, bool) {
	j, ok := s.Jobs.get(jobID)
	if !ok {
		return fmt.Sprintf("unknown job_id %q (finished jobs are kept for ~%s; start a new job with review_start or chat_start)", jobID, keepFinished), true
	}
	select {
	case <-j.done:
		prefix := fmt.Sprintf("status: completed (job %s, agent %s, kind %s, elapsed %s)\n\n", j.ID, j.Agent, j.Kind, j.ended.Sub(j.Started).Round(time.Second))
		return prefix + j.output, j.isErr
	case <-ctx.Done():
		return "status check cancelled by client", true
	case <-time.After(wait):
		return fmt.Sprintf(
			"status: running (job %s, agent %s, kind %s, elapsed %s)\nStill in progress. Call job_status again with the same job_id.",
			j.ID, j.Agent, j.Kind, time.Since(j.Started).Round(time.Second),
		), false
	}
}

func (s *Service) streamTo() io.Writer {
	if s.Cfg.StreamEnabled() {
		return os.Stderr
	}
	return nil
}

// run executes the backend command with the server's timeout and caps.
func (s *Service) run(ctx context.Context, b agent.Backend, dir string, args []string) (exec.Result, error) {
	return s.Runner.Run(ctx, exec.Request{
		Command: b.Command(), Args: args, Dir: dir,
		Timeout: s.Cfg.Timeout, MaxOutputBytes: s.Cfg.MaxOutputBytes, StreamTo: s.streamTo(),
	})
}

func (s *Service) runReview(ctx context.Context, b agent.Backend, dir, scope, base, commit, prompt string) (string, bool) {
	args, native, err := b.ReviewArgs(scope, base, commit, prompt)
	if err != nil {
		return err.Error(), true
	}
	if !native {
		p, err := agent.ReviewPrompt(scope, base, commit, prompt)
		if err != nil {
			return err.Error(), true
		}
		args = b.ChatArgs(p, "")
	}
	res, err := s.run(ctx, b, dir, args)
	if err != nil {
		s.Log.Info().Str("tool", "review").Str("agent", b.Name()).Str("folder", dir).Err(err).Msg("invocation")
		return fmt.Sprintf("%v (is %q installed and on PATH? set agents.%s.command in config.yaml to an absolute path)", err, b.Command(), b.Name()), true
	}
	s.Log.Info().Str("tool", "review").Str("agent", b.Name()).Str("folder", dir).Dur("elapsed", res.Elapsed).Int("exit", res.ExitCode).Msg("invocation")

	text := res.Stdout
	if !native && res.ExitCode == 0 && !res.TimedOut {
		reply, _, perr := b.ParseChat(res.Stdout)
		if perr != nil {
			return perr.Error() + "\n" + diagnostics(b, res, s.Cfg.Timeout), true
		}
		text = reply
	}
	label := scope
	if label == "" {
		label = "uncommitted"
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Mode: %s (%s exit %d)\n\n--- %s review ---\n%s\n", label, b.Name(), res.ExitCode, agent.Label(b.Name()), text)
	out.WriteString(diagnostics(b, res, s.Cfg.Timeout))
	return out.String(), false
}

func (s *Service) runChat(ctx context.Context, b agent.Backend, dir, message, sessionID string) (string, bool) {
	res, err := s.run(ctx, b, dir, b.ChatArgs(message, sessionID))
	if err != nil {
		s.Log.Info().Str("tool", "chat").Str("agent", b.Name()).Str("folder", dir).Err(err).Msg("invocation")
		return fmt.Sprintf("%v (is %q installed and on PATH? set agents.%s.command in config.yaml to an absolute path)", err, b.Command(), b.Name()), true
	}
	s.Log.Info().Str("tool", "chat").Str("agent", b.Name()).Str("folder", dir).Dur("elapsed", res.Elapsed).Int("exit", res.ExitCode).Msg("invocation")

	reply, sid, perr := b.ParseChat(res.Stdout)
	if perr != nil || res.ExitCode != 0 || res.TimedOut {
		var out strings.Builder
		fmt.Fprintf(&out, "%s chat failed (%s exit %d)", agent.Label(b.Name()), b.Name(), res.ExitCode)
		if perr != nil {
			fmt.Fprintf(&out, ": %v", perr)
		}
		out.WriteString("\n")
		if sid != "" {
			fmt.Fprintf(&out, "session_id: %s\n", sid)
		}
		out.WriteString(diagnostics(b, res, s.Cfg.Timeout))
		return out.String(), true
	}
	var out strings.Builder
	fmt.Fprintf(&out, "session_id: %s\n\n--- %s reply ---\n%s\n", sid, agent.Label(b.Name()), reply)
	out.WriteString(diagnostics(b, res, s.Cfg.Timeout))
	return out.String(), false
}

// diagnostics appends stderr (only when the CLI failed — its stderr is a
// verbose progress trace that is already streamed to the server log), plus
// timeout and truncation markers.
func diagnostics(b agent.Backend, res exec.Result, timeout time.Duration) string {
	var out strings.Builder
	if res.ExitCode != 0 && res.Stderr != "" {
		fmt.Fprintf(&out, "\n--- %s stderr (exit %d) ---\n%s\n", b.Name(), res.ExitCode, res.Stderr)
	}
	if res.TimedOut {
		fmt.Fprintf(&out, "\n[%s was killed after the server-side timeout of %s (config 'timeout'); the result is incomplete]\n", b.Name(), timeout)
	}
	if res.Truncated {
		out.WriteString("\n[output truncated]\n")
	}
	return out.String()
}

// RunCommand runs an allowlisted argv in a host folder (unchanged behaviour).
func (s *Service) RunCommand(ctx context.Context, command string, args []string, folder string) (string, bool) {
	if !s.Cfg.CommandAllowed(command) {
		return fmt.Sprintf("command %q is not allowed", command), true
	}
	dir, err := s.Cfg.ResolveAllowedDir(folder)
	if err != nil {
		return err.Error(), true
	}
	res, err := s.Runner.Run(ctx, exec.Request{
		Command: command, Args: args, Dir: dir, Timeout: s.Cfg.Timeout, MaxOutputBytes: s.Cfg.MaxOutputBytes,
		StreamTo: s.streamTo(),
	})
	if err != nil {
		s.Log.Info().Str("tool", "run_command").Str("command", command).Str("folder", dir).Err(err).Msg("invocation")
		return err.Error(), true
	}
	s.Log.Info().Str("tool", "run_command").Str("command", command).Str("folder", dir).Int("exit", res.ExitCode).Dur("elapsed", res.Elapsed).Msg("invocation")
	var b strings.Builder
	fmt.Fprintf(&b, "exit=%d elapsed=%s\n--- stdout ---\n%s\n--- stderr ---\n%s\n", res.ExitCode, res.Elapsed, res.Stdout, res.Stderr)
	if res.TimedOut {
		b.WriteString("[timed out]\n")
	}
	return b.String(), false
}
```

Imports for `server.go`: `context, fmt, io, os, strings, time`, `internal/agent`, `internal/claude`, `internal/codex`, `internal/config`, `internal/exec`, `mcp`, `mcpserver`, `zerolog`.

Temporarily make `New` compile by replacing its body with `svc := NewService(cfg, r, log); _ = svc; return nil, func() {}` so tests can run — Task 7 fills it in. Change its signature to `New(cfg *config.Config, r agent.Runner, log zerolog.Logger)`.

- [ ] **Step 4: Run tests**

Run: `go vet ./... && go test ./internal/server/`
Expected: `cmd/hostrunner` compiles (signature matches); server tests `ok`.

- [ ] **Step 5: Commit (Tasks 5+6 together — they only compile together)**

```bash
git add internal/server
git commit -m "feat(server): backend-agnostic Service with review/chat jobs keyed by root+agent+kind"
```

---

### Task 7: Tool registration (new tools + deprecated aliases), main wiring, version 0.3.0

**Files:**
- Modify: `internal/server/server.go` (`New`)
- Modify: `cmd/hostrunner/main.go`
- Modify: `.claude-plugin/plugin.json` (version `0.3.0`)
- Test: `internal/server/tools_test.go` (new)

**Interfaces:**
- Produces: `func New(cfg *config.Config, r agent.Runner, log zerolog.Logger) (*mcpserver.MCPServer, func())` registering tools `review_start`, `chat_start`, `job_status`, `codex_review_start`, `codex_review_status`, `run_command`.

- [ ] **Step 1: Write failing test `internal/server/tools_test.go`**

```go
package server

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/rs/zerolog"
)

func TestRegisteredTools(t *testing.T) {
	cfg, _ := newCfg(t)
	s, shutdown := New(cfg, &fakeRunner{}, zerolog.Nop())
	defer shutdown()

	res, err := s.HandleMessage(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, ok := res.(mcp.JSONRPCResponse)
	if !ok {
		t.Fatalf("unexpected response type %T: %v", res, res)
	}
	list, ok := resp.Result.(mcp.ListToolsResult)
	if !ok {
		t.Fatalf("unexpected result type %T", resp.Result)
	}
	var names []string
	byName := map[string]mcp.Tool{}
	for _, tl := range list.Tools {
		names = append(names, tl.Name)
		byName[tl.Name] = tl
	}
	sort.Strings(names)
	want := "chat_start,codex_review_start,codex_review_status,job_status,review_start,run_command"
	if got := strings.Join(names, ","); got != want {
		t.Errorf("tools = %s\nwant    %s", got, want)
	}
	for _, alias := range []string{"codex_review_start", "codex_review_status"} {
		if !strings.Contains(byName[alias].Description, "Deprecated") {
			t.Errorf("%s description should say Deprecated", alias)
		}
	}
	if _, ok := byName["review_start"].InputSchema.Properties["agent"]; !ok {
		t.Error("review_start must take an agent param")
	}
	if _, ok := byName["chat_start"].InputSchema.Properties["session_id"]; !ok {
		t.Error("chat_start must take a session_id param")
	}
}

func TestAliasToolRunsCodexReview(t *testing.T) {
	cfg, repo := newCfg(t)
	s, shutdown := New(cfg, &fakeRunner{reply: map[string]exec.Result{
		"codex review --uncommitted": {Stdout: "ALIAS_OK"},
	}}, zerolog.Nop())
	defer shutdown()

	call := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"codex_review_start","arguments":{"folder":` + strconv.Quote(repo) + `}}}`
	res, err := s.HandleMessage(context.Background(), []byte(call))
	if err != nil {
		t.Fatal(err)
	}
	text := toolText(t, res)
	if !strings.Contains(text, "job_id: ") || !strings.Contains(text, "agent: codex") {
		t.Fatalf("alias start = %q", text)
	}
	id := jobID(t, text)
	deadline := time.Now().Add(5 * time.Second)
	for {
		status := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"codex_review_status","arguments":{"job_id":"` + id + `"}}}`
		res, err = s.HandleMessage(context.Background(), []byte(status))
		if err != nil {
			t.Fatal(err)
		}
		text = toolText(t, res)
		if strings.Contains(text, "status: completed") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never completed: %q", text)
		}
	}
	if !strings.Contains(text, "ALIAS_OK") {
		t.Errorf("alias status = %q", text)
	}
}

func toolText(t *testing.T, res mcp.JSONRPCMessage) string {
	t.Helper()
	resp, ok := res.(mcp.JSONRPCResponse)
	if !ok {
		t.Fatalf("unexpected response %T: %v", res, res)
	}
	ctr, ok := resp.Result.(*mcp.CallToolResult)
	if !ok {
		t.Fatalf("unexpected result %T", resp.Result)
	}
	var sb strings.Builder
	for _, c := range ctr.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}
```

Add imports `strconv`, `time`, and `internal/exec` as needed. If `HandleMessage`'s result type names differ in mcp-go v0.55.1, inspect `$(go env GOMODCACHE)/github.com/mark3labs/mcp-go@v0.55.1/server/server.go` (`func (s *MCPServer) HandleMessage`) and adjust the type assertions — the behaviour under test (names, descriptions, alias round-trip) stays the same.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/server/ -run 'Registered|Alias'`
Expected: FAIL (New returns nil server → panic / no tools).

- [ ] **Step 3: Implement `New`**

```go
// New builds the MCP server. The returned shutdown func cancels any
// still-running background jobs (killing their CLI process groups) and
// should be called after the server stops serving.
func New(cfg *config.Config, r agent.Runner, log zerolog.Logger) (*mcpserver.MCPServer, func()) {
	s := mcpserver.NewMCPServer("hostrunner", version, mcpserver.WithRecovery())
	svc := NewService(cfg, r, log)
	agents := strings.Join(svc.Backends.Names(), " | ")

	text := func(out string, isErr bool) (*mcp.CallToolResult, error) {
		if isErr {
			return mcp.NewToolResultError(out), nil
		}
		return mcp.NewToolResultText(out), nil
	}

	agentOpt := mcp.WithString("agent",
		mcp.Description("Which peer agent runs it: "+agents+". Omit to use the other vendor's agent automatically (a Claude client gets codex, a Codex client gets claude)."),
		mcp.Enum(svc.Backends.Names()...),
	)
	folderOpt := mcp.WithString("folder", mcp.Required(), mcp.Description("Absolute HOST path to the git repo (e.g. /Users/you/proj). Not a sandbox path."))
	scopeOpts := []mcp.ToolOption{
		mcp.WithString("scope",
			mcp.Description("Which changes to review: uncommitted (default, staged+unstaged+untracked), base (against a base branch, requires base param), or commit (a specific commit, requires commit param)"),
			mcp.Enum("uncommitted", "base", "commit"),
		),
		mcp.WithString("base", mcp.Description("Base branch or ref, required when scope=base (e.g. main)")),
		mcp.WithString("commit", mcp.Description("Commit SHA, required when scope=commit")),
		mcp.WithString("prompt", mcp.Description("Custom review instructions. ONLY pass this when the user explicitly asks to narrow or focus the review (e.g. \"focus on concurrency\"); omit it for a normal general review. Safe to combine with any scope.")),
	}

	reviewHandler := func(forceAgent string) mcpserver.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			folder, err := req.RequireString("folder")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			a := forceAgent
			if a == "" {
				a = req.GetString("agent", "")
			}
			return text(svc.ReviewStart(ctx, a, folder,
				req.GetString("scope", "uncommitted"), req.GetString("base", ""), req.GetString("commit", ""), req.GetString("prompt", "")))
		}
	}
	statusHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("job_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return text(svc.JobStatus(ctx, id, statusLongPoll))
	}

	s.AddTool(mcp.NewTool("review_start",
		append([]mcp.ToolOption{
			mcp.WithDescription("Start a read-only code review of changes in a host project folder by a peer agent (" + agents + "). Runs in the background on the host and returns a job_id immediately; poll job_status for the result. Reviews often take several minutes, longer than the MCP client's per-call limit, hence the job model. The peer never modifies files."),
			folderOpt, agentOpt,
		}, scopeOpts...)...,
	), reviewHandler(""))

	s.AddTool(mcp.NewTool("chat_start",
		mcp.WithDescription("Send one message to a peer agent ("+agents+") that can read the host project folder (read-only) and get its reply via job_status. Use it for second opinions, design critique or a multi-round discussion: the job_status result includes a session_id; pass it back as session_id to continue the same conversation. Runs in the background; poll job_status."),
		folderOpt, agentOpt,
		mcp.WithString("message", mcp.Required(), mcp.Description("What to say to the peer. Include the context it needs (what you changed, what you want its opinion on); it only sees the repo and this conversation.")),
		mcp.WithString("session_id", mcp.Description("session_id from a previous job_status result to continue that conversation. Omit to start a new one.")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		folder, err := req.RequireString("folder")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		msg, err := req.RequireString("message")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return text(svc.ChatStart(ctx, req.GetString("agent", ""), folder, msg, req.GetString("session_id", "")))
	})

	s.AddTool(mcp.NewTool("job_status",
		mcp.WithDescription("Get the status/result of a background job started with review_start or chat_start. Blocks up to ~50s waiting for completion, then returns either the finished result or a 'running' notice; keep calling with the same job_id until it completes. Finished results stay available for ~30 minutes."),
		mcp.WithString("job_id", mcp.Required(), mcp.Description("The job_id returned by review_start / chat_start")),
	), statusHandler)

	// Deprecated aliases kept so existing installs and skills keep working.
	s.AddTool(mcp.NewTool("codex_review_start",
		append([]mcp.ToolOption{
			mcp.WithDescription("Deprecated: use review_start (optionally with agent=codex). Starts a read-only codex review in the background and returns a job_id; poll job_status or codex_review_status."),
			folderOpt,
		}, scopeOpts...)...,
	), reviewHandler("codex"))
	s.AddTool(mcp.NewTool("codex_review_status",
		mcp.WithDescription("Deprecated: use job_status. Same behaviour: blocks up to ~50s and returns the finished review or a 'running' notice."),
		mcp.WithString("job_id", mcp.Required(), mcp.Description("The job_id returned by codex_review_start / review_start")),
	), statusHandler)

	s.AddTool(mcp.NewTool("run_command",
		mcp.WithDescription("Run an allowlisted command (argv, no shell) in a host folder."),
		mcp.WithString("command", mcp.Required(), mcp.Description("Executable name; must be in the server allowlist")),
		mcp.WithArray("args", mcp.WithStringItems(), mcp.Description("Arguments as a string array")),
		folderOpt,
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		command, err := req.RequireString("command")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		folder, err := req.RequireString("folder")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return text(svc.RunCommand(ctx, command, req.GetStringSlice("args", nil), folder))
	})

	return s, func() { svc.Jobs.shutdown(3 * time.Second) }
}
```

Add near the top of `server.go`:

```go
// version is reported in the MCP initialize handshake. Keep in sync with
// .claude-plugin/plugin.json.
const version = "0.3.0"
```

If `mcp.Enum(svc.Backends.Names()...)` does not type-check (signature takes `...string` — it should), pass `"claude", "codex"` literally.

- [ ] **Step 4: Update `cmd/hostrunner/main.go`**

Only the call site changes: `internalserver.New(cfg, exec.Runner{}, log)` already matches the new signature (`exec.Runner` satisfies `agent.Runner`). Add the agents to the startup log line:

```go
	log.Info().
		Str("config", cfgPath).
		Str("timeout", cfg.Timeout.String()).
		Strs("allowed_roots", cfg.AllowedRoots).
		Strs("allowed_commands", cfg.AllowedCommands).
		Str("codex_command", cfg.Agent("codex").Command).
		Str("claude_command", cfg.Agent("claude").Command).
		Msg("config loaded")
```

- [ ] **Step 5: Bump `.claude-plugin/plugin.json`**

Set `"version": "0.3.0"` and description:
`"Local MCP server + plugin for Claude and Codex: let either agent ask the other for a read-only code review or a multi-turn discussion on your machine, bridging sandboxes to your host codex / claude CLIs."`
Update `.claude-plugin/marketplace.json` description to the same text.

- [ ] **Step 6: Run everything**

Run: `go vet ./... && go test ./...`
Expected: all `ok`.

- [ ] **Step 7: Commit**

```bash
git add internal/server cmd .claude-plugin
git commit -m "feat(server): review_start/chat_start/job_status tools with codex_* aliases; v0.3.0"
```

---

### Task 8: Skills — `peer-review`, `peer-chat`, refresh `codex-loop`

**Files:**
- Create: `skills/peer-review/SKILL.md`
- Create: `skills/peer-chat/SKILL.md`
- Modify: `skills/codex-loop/SKILL.md`

Skills are plain Markdown with `name`/`description` frontmatter — the same format Claude plugins and Codex (`~/.agents/skills/`) read. They are installed to both hosts unchanged, so they must never say "you are Claude"/"you are Codex"; the server infers the peer.

- [ ] **Step 1: Write `skills/peer-review/SKILL.md`**

````markdown
---
name: peer-review
description: >
  Ask the other vendor's coding agent (Codex when you are Claude, Claude when
  you are Codex) for a read-only code review of the current changes via the
  hostrunner MCP tools, then fix what it finds and re-review until clean. Use
  when the user says "peer review", "get a second opinion on my changes",
  "have codex/claude review this", "review loop", or "cross-review".
---

# peer-review

## What this skill does

1. Calls `review_start` with the project's **host path**. The server picks the
   peer agent automatically (the other vendor); only pass `agent` when the user
   names one explicitly ("have codex review it").
2. Polls `job_status` with the returned `job_id` until it completes.
3. Summarizes the findings, applies fixes with your own edit tools.
4. Starts a new review to verify, and repeats until the review is clean.

The peer is strictly read-only: it can read the repo and run `git diff` /
`git log` / `git status` / `git show`, but it cannot edit. All edits are yours.

Reviews run as background jobs because they often take several minutes —
longer than an MCP client's per-tool-call limit. Each start/status call
returns quickly, so the loop never hits that limit.

## Critical: always use the HOST path

`folder` must be the absolute path on the user's machine, e.g.
`/Users/yourname/code/myproject`. Never a sandbox path such as
`/sessions/.../mnt/myproject` (the server rejects those). If unsure, ask:
"What is the absolute path to this project on your machine?"

## Steps

### 1. Confirm there is something to review (optional)

```
run_command(command="git", args=["status", "--short"], folder="/Users/you/proj")
```

Empty output means nothing to review yet.

### 2. Start the review

```
review_start(folder="/Users/you/proj")
```

Options:
- `scope="base", base="main"` — review the branch against `main`.
- `scope="commit", commit="<SHA>"` — review one commit.
- `agent="codex"` / `agent="claude"` — only when the user asked for a specific peer.
- `prompt="focus on error handling"` — only when the user asked to narrow the review. Never restate the scope in the prompt.

The result contains `job_id` and `agent`.

### 3. Poll

```
job_status(job_id="<job_id>")
```

Each call blocks up to ~50s and returns either the finished result or
`status: running`. Keep polling with the same `job_id`; reviews take 1–10
minutes. Do not start a second review for the same folder while one is running
(the server refuses and tells you the running job_id).

### 4. Read the verdict

The verdict is the text under `--- Codex review ---` or `--- Claude review ---`.
Decide from that text, **not** the exit code (`codex review` exits 0 even when
it lists problems).

- Concrete issues or risks → summarize as a numbered list, fix them with your
  edit tools, then go to step 5.
- "No issues" / clean / approved → STOP and tell the user the review is clean.

A non-zero `(<agent> exit N)` with a `--- <agent> stderr ---` section means the
peer CLI itself failed (not installed, not logged in, bad flag): report it as a
tool error. A `[<agent> was killed after the server-side timeout ...]` marker
means the server's `timeout` config is too low for this repo.

### 5. Re-review

After fixing, call `review_start` again with the same parameters (a fresh job)
and poll as in step 3. Repeat from step 4 until clean.

## Tips

- The old tool names `codex_review_start` / `codex_review_status` still exist
  but are deprecated; prefer `review_start` / `job_status`.
- `[output truncated]` means the diff is very large; suggest reviewing a single
  commit or a narrower scope.
- Finished results stay retrievable via `job_status` for ~30 minutes.
````

- [ ] **Step 2: Write `skills/peer-chat/SKILL.md`**

````markdown
---
name: peer-chat
description: >
  Hold a multi-turn discussion with the other vendor's coding agent (Codex when
  you are Claude, Claude when you are Codex) about a design, a bug, or a plan,
  using the hostrunner `chat_start` / `job_status` tools. Use when the user says
  "ask codex/claude what it thinks", "discuss this with the other agent",
  "debate this design", "get a second opinion", or "what would codex/claude do".
---

# peer-chat

## What this skill does

Runs a conversation between you (the agent in the GUI) and a peer agent that
can read the same repository read-only. You drive the rounds; the server only
relays messages and keeps the peer's session alive.

1. `chat_start(folder, message)` — opens a new conversation. Returns `job_id`.
2. `job_status(job_id)` — poll until done. The result starts with
   `session_id: <id>` followed by `--- <Agent> reply ---`.
3. To continue, call `chat_start(folder, message, session_id="<id>")`. The peer
   remembers the earlier turns.
4. Stop when the user's question is answered, both sides agree, or the user
   asked for a fixed number of rounds. Then summarize both positions and your
   recommendation.

The peer is read-only: it can inspect files and run `git diff`/`log`/`status`/
`show`, nothing else. If an edit is needed, you make it.

## Critical: always use the HOST path

`folder` must be the absolute path on the user's machine
(`/Users/yourname/code/myproject`), never a `/sessions/...` sandbox path.

## Writing good messages

The peer sees only the repo and this conversation, not your chat with the user.
In the first message include: what the user is trying to do, what you changed
or propose (file paths help), and the specific question. Example:

```
chat_start(
  folder="/Users/you/proj",
  message="We're adding retry logic to internal/http/client.go (see the uncommitted diff). I propose exponential backoff with jitter, max 5 attempts, retrying on 5xx and connection errors only. Do you see correctness or production risks? Be specific and cite lines."
)
```

Follow-ups should respond to the peer's points, not restart:

```
chat_start(
  folder="/Users/you/proj",
  session_id="<from job_status>",
  message="On your point 2: the idempotency concern applies only to POST /orders; GETs are safe. Does that change your recommendation? Anything else blocking?"
)
```

## Polling

`job_status` blocks up to ~50s and returns either the reply or
`status: running`; keep calling it with the same `job_id`. A reply typically
takes 30s–3min because the peer reads the repo first. Do not start another
`chat_start` for the same folder and agent while one is running.

## When it fails

- `<Agent> chat failed (<agent> exit N)` with a stderr section: the peer CLI is
  not installed, not logged in, or rejected a flag — report that to the user.
- An unknown or expired `session_id` surfaces as a CLI error; start a new
  conversation and re-state the context.
- `cannot infer which agent to use`: pass `agent="codex"` or `agent="claude"`.

## Tips

- Pass `agent` only when the user names a specific peer; otherwise the server
  picks the other vendor automatically.
- Keep each message focused on one or two points; long monologues get
  shallow answers.
- Finished results stay retrievable via `job_status` for ~30 minutes.
````

- [ ] **Step 3: Refresh `skills/codex-loop/SKILL.md`**

Edit the existing file in place:
- In the frontmatter description, replace "calls the codex_review_start MCP tool" with "calls the review_start MCP tool with agent=codex" and "polls codex_review_status" with "polls job_status".
- Replace every `codex_review_start(` with `review_start(` and add `agent  = "codex",` as the first argument line of each example call; replace every `codex_review_status(` with `job_status(`.
- Replace the text "job_id to the next step" paragraphs accordingly (names only).
- Add this paragraph under "## Tips":
  `- The old names `codex_review_start` / `codex_review_status` still work but are deprecated aliases of `review_start(agent="codex")` / `job_status`. For an agent-neutral version of this loop (works in Codex too, reviewing with Claude) use the `peer-review` skill.`

- [ ] **Step 4: Check frontmatter parses**

Run: `for f in skills/*/SKILL.md; do head -1 "$f" | grep -q '^---$' && sed -n '2,12p' "$f" | grep -q '^name: ' && echo "ok $f" || echo "BAD $f"; done`
Expected: three `ok` lines.

- [ ] **Step 5: Commit**

```bash
git add skills
git commit -m "docs(skills): add agent-neutral peer-review and peer-chat skills; point codex-loop at new tools"
```

---

### Task 9: Scripts — shared download lib, Codex installer

**Files:**
- Create: `scripts/lib.sh` (download + checksum + cache, extracted from `launch.sh`)
- Modify: `scripts/launch.sh` (source `lib.sh`)
- Create: `scripts/install-codex.sh`

**Interfaces:**
- `lib.sh` defines `hostrunner_ensure_binary VERSION` → prints the cached binary path on stdout, downloading + verifying if needed. Requires `REPO` to be set (defaults to `jerryctt/hostrunner-mcp`).
- `install-codex.sh [--local /path/to/hostrunner] [--version X.Y.Z] [--server-name hostrunner]`.

- [ ] **Step 1: Create `scripts/lib.sh`**

```sh
# hostrunner shared shell helpers (POSIX sh). Source this file; do not execute.
# Provides: hostrunner_ensure_binary VERSION  -> prints path to a verified binary.

REPO="${REPO:-jerryctt/hostrunner-mcp}"

hostrunner_platform() {
  os="$(uname -s)"; arch="$(uname -m)"
  case "$os" in
    Darwin) os=darwin ;;
    Linux)  os=linux ;;
    *) echo "hostrunner: unsupported OS '$os' — install manually (see README)" >&2; return 1 ;;
  esac
  case "$arch" in
    x86_64|amd64) arch=amd64 ;;
    arm64|aarch64) arch=arm64 ;;
    *) echo "hostrunner: unsupported arch '$arch'" >&2; return 1 ;;
  esac
  echo "$os $arch"
}

# hostrunner_ensure_binary VERSION
# Downloads hostrunner_<ver>_<os>_<arch>.tar.gz from GitHub Releases on first
# use, verifies it against the release checksums.txt, caches it under
# ${XDG_CACHE_HOME:-$HOME/.cache}/hostrunner and prints the binary path.
hostrunner_ensure_binary() {
  ver="$1"
  set -- $(hostrunner_platform) || return 1
  os="$1"; arch="$2"
  cache_dir="${XDG_CACHE_HOME:-$HOME/.cache}/hostrunner"
  bin="$cache_dir/hostrunner-$ver-$os-$arch"
  if [ -x "$bin" ]; then
    echo "$bin"; return 0
  fi
  mkdir -p "$cache_dir"
  archive="hostrunner_${ver}_${os}_${arch}.tar.gz"
  base="https://github.com/$REPO/releases/download/v$ver"
  tmp="$(mktemp -d)"
  echo "hostrunner: downloading $archive (v$ver)..." >&2
  if ! curl -fsSL -o "$tmp/$archive" "$base/$archive" || ! curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt"; then
    rm -rf "$tmp"; echo "hostrunner: download failed" >&2; return 1
  fi
  want="$(grep " ${archive}\$" "$tmp/checksums.txt" | awk '{print $1}')"
  if [ -z "${want:-}" ]; then
    rm -rf "$tmp"; echo "hostrunner: $archive not listed in checksums.txt" >&2; return 1
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    got="$(sha256sum "$tmp/$archive" | awk '{print $1}')"
  else
    got="$(shasum -a 256 "$tmp/$archive" | awk '{print $1}')"
  fi
  if [ "$want" != "$got" ]; then
    rm -rf "$tmp"
    echo "hostrunner: checksum mismatch for $archive (expected $want, got $got)" >&2; return 1
  fi
  tar -xzf "$tmp/$archive" -C "$tmp"
  if [ ! -f "$tmp/hostrunner" ]; then
    rm -rf "$tmp"; echo "hostrunner: 'hostrunner' not found in archive" >&2; return 1
  fi
  chmod +x "$tmp/hostrunner"
  mv "$tmp/hostrunner" "$bin"
  rm -rf "$tmp"
  echo "hostrunner: installed $bin" >&2
  echo "$bin"
}
```

- [ ] **Step 2: Rewrite `scripts/launch.sh` to use it**

```sh
#!/usr/bin/env sh
# hostrunner plugin launcher (macOS/Linux) for Claude Desktop / Claude Code.
# Downloads the prebuilt binary matching this host on first run (verified
# against the release checksums.txt), caches it, then execs it. Windows is not
# supported by this launcher — install the binary manually (see README).
set -eu

PLUGIN_ROOT="${CLAUDE_PLUGIN_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
PLUGIN_JSON="$PLUGIN_ROOT/.claude-plugin/plugin.json"
. "$PLUGIN_ROOT/scripts/lib.sh"

VER="$(grep '"version"' "$PLUGIN_JSON" | head -1 | sed -E 's/.*"version"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/')"
if [ -z "${VER:-}" ]; then
  echo "hostrunner launcher: cannot read version from $PLUGIN_JSON" >&2
  exit 1
fi

BIN="$(hostrunner_ensure_binary "$VER")"
exec "$BIN" "$@"
```

- [ ] **Step 3: Create `scripts/install-codex.sh`**

```sh
#!/usr/bin/env sh
# Install hostrunner for Codex (CLI, macOS app and IDE extension share the
# same ~/.codex/config.toml and ~/.agents/skills).
#
#   scripts/install-codex.sh                 # download release binary
#   scripts/install-codex.sh --local ./hostrunner   # use a locally built binary
#   options: --version X.Y.Z  --server-name NAME (default hostrunner)
#
# What it does (idempotent):
#   1. places the binary (downloaded + checksum-verified, or --local) and links
#      it at ~/.local/bin/hostrunner
#   2. writes a [mcp_servers.<name>] block into ~/.codex/config.toml
#      (replacing a previous hostrunner block, leaving the rest untouched)
#   3. copies skills/peer-review and skills/peer-chat into ~/.agents/skills/
#   4. creates ~/.config/hostrunner/config.yaml from the example if missing
set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
. "$ROOT/scripts/lib.sh"

LOCAL=""; VER=""; NAME="hostrunner"
while [ $# -gt 0 ]; do
  case "$1" in
    --local) LOCAL="$2"; shift 2 ;;
    --version) VER="$2"; shift 2 ;;
    --server-name) NAME="$2"; shift 2 ;;
    -h|--help) sed -n '2,15p' "$0"; exit 0 ;;
    *) echo "unknown option $1" >&2; exit 2 ;;
  esac
done

if [ -z "$VER" ]; then
  VER="$(grep '"version"' "$ROOT/.claude-plugin/plugin.json" | head -1 | sed -E 's/.*"version"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/')"
fi

# 1. binary
if [ -n "$LOCAL" ]; then
  BIN="$(cd "$(dirname "$LOCAL")" && pwd)/$(basename "$LOCAL")"
  [ -x "$BIN" ] || { echo "not executable: $BIN" >&2; exit 1; }
else
  BIN="$(hostrunner_ensure_binary "$VER")"
fi
mkdir -p "$HOME/.local/bin"
ln -sf "$BIN" "$HOME/.local/bin/hostrunner"
echo "binary: $HOME/.local/bin/hostrunner -> $BIN"

# 2. ~/.codex/config.toml
CODEX_HOME="${CODEX_HOME:-$HOME/.codex}"
CFG="$CODEX_HOME/config.toml"
mkdir -p "$CODEX_HOME"
[ -f "$CFG" ] || : > "$CFG"
BEGIN="# >>> hostrunner (managed by install-codex.sh) >>>"
END="# <<< hostrunner <<<"
tmp="$(mktemp)"
awk -v b="$BEGIN" -v e="$END" '
  $0 == b { skip = 1; next }
  $0 == e { skip = 0; next }
  !skip { print }
' "$CFG" > "$tmp"
# strip trailing blank lines so the block is appended tidily
sed -e :a -e '/^\n*$/{$d;N;ba' -e '}' "$tmp" > "$CFG" 2>/dev/null || cp "$tmp" "$CFG"
rm -f "$tmp"
cat >> "$CFG" <<EOF

$BEGIN
[mcp_servers.$NAME]
command = "$BIN"
args = []
# review/chat jobs are polled via job_status (each call blocks <= 50s)
tool_timeout_sec = 120
startup_timeout_sec = 20
$END
EOF
echo "codex config: [mcp_servers.$NAME] written to $CFG"

# 3. skills
SKILLS="$HOME/.agents/skills"
mkdir -p "$SKILLS"
for s in peer-review peer-chat; do
  rm -rf "$SKILLS/$s"
  cp -R "$ROOT/skills/$s" "$SKILLS/$s"
  echo "skill: $SKILLS/$s"
done

# 4. hostrunner config
HR_CFG="$HOME/.config/hostrunner/config.yaml"
if [ ! -f "$HR_CFG" ]; then
  mkdir -p "$(dirname "$HR_CFG")"
  cp "$ROOT/examples/config.example.yaml" "$HR_CFG"
  echo "created $HR_CFG — edit allowed_roots before use"
else
  echo "config: $HR_CFG (unchanged)"
fi

cat <<EOF

Done. Next steps:
  1. Edit $HR_CFG: set allowed_roots, and if codex/claude are not on the
     GUI app's PATH set agents.codex.command / agents.claude.command to
     absolute paths (e.g. \$(which claude)).
  2. Restart the Codex app (or run 'codex mcp list' in a terminal) and check
     that '$NAME' is listed.
  3. In Codex, say "peer review my changes" or "ask claude what it thinks
     about this design".
EOF
```

- [ ] **Step 4: Test the installer in a throwaway HOME**

```bash
chmod +x scripts/install-codex.sh scripts/launch.sh
go build -o /tmp/hostrunner-test ./cmd/hostrunner
H=$(mktemp -d); printf '[model]\nname = "x"\n' > /dev/null
mkdir -p "$H/.codex"; printf 'model = "gpt-5"\n\n[mcp_servers.other]\ncommand = "o"\n' > "$H/.codex/config.toml"
HOME="$H" sh scripts/install-codex.sh --local /tmp/hostrunner-test --server-name hostrunner
HOME="$H" sh scripts/install-codex.sh --local /tmp/hostrunner-test --server-name hostrunner   # idempotency
cat "$H/.codex/config.toml"
ls "$H/.agents/skills" "$H/.config/hostrunner" "$H/.local/bin"
grep -c 'mcp_servers.hostrunner' "$H/.codex/config.toml"
```

Expected: config.toml keeps `model = "gpt-5"` and `[mcp_servers.other]`, contains **exactly one** managed block (`grep -c` prints `1`), `tool_timeout_sec = 120`; skills dir has `peer-review` and `peer-chat`; `config.yaml` created; `~/.local/bin/hostrunner` symlink exists. Also `sh -n scripts/launch.sh scripts/lib.sh scripts/install-codex.sh` prints nothing.

- [ ] **Step 5: Commit**

```bash
git add scripts
git commit -m "feat(scripts): shared download lib, Codex one-shot installer (config.toml + skills)"
```

---

### Task 10: Docs and examples

**Files:**
- Modify: `examples/config.example.yaml`
- Modify: `README.md`
- Modify: `README_zh.md`

- [ ] **Step 1: Update `examples/config.example.yaml`**

```yaml
allowed_roots:
  - /Users/you/code
allowed_commands:
  - codex
  - claude
  - git
# Max wall-clock time for one host command (a review, one chat turn, or one
# run_command). Needs a unit (600s, 10m) — a bare number is rejected.
# NOTE: this file is read once at server startup; restart Claude Desktop /
# the Codex app after editing. The effective values are logged as
# "config loaded" (Claude: ~/Library/Logs/Claude/mcp-server-hostrunner.log).
timeout: 600s
max_output_bytes: 200000
stream_output: false   # tee peer CLI output to the server log

# Peer agents. Both are optional; defaults shown. macOS GUI apps do not
# inherit your shell PATH, so use absolute paths if a CLI is not found
# (e.g. `which claude`).
agents:
  codex:
    command: codex
    extra_args: []              # e.g. ["-c", "model=o3"]
    mcp_server_name: hostrunner # [mcp_servers.<name>] entry disabled inside the codex peer (recursion guard)
  claude:
    command: claude
    extra_args: []              # e.g. ["--model", "opus"]
# legacy, still honoured and appended to agents.codex.extra_args:
# codex_extra_args: ["-c", "model=o3"]
```

- [ ] **Step 2: Update `README.md`**

Make these edits (keep everything else):

1. Tagline (first paragraph): `Local MCP server + plugin for **Claude** and **Codex**: from either app, ask the *other* vendor's agent for a read-only code review or a multi-turn discussion of your code, running on your machine with your own CLI logins.`
2. Replace the **Features** list with:
   - **`review_start` / `job_status`** — read-only review of uncommitted changes, a base-branch diff, or a commit, by a peer agent (`codex` uses `codex review`; `claude` uses `claude -p` with a review prompt). Background job model because reviews outlast MCP per-call timeouts (Claude Desktop ~180s, Codex `tool_timeout_sec`).
   - **`chat_start` / `job_status`** — one message to the peer, which can read the repo; the result carries a `session_id` to continue the conversation (`claude -p --resume`, `codex exec resume`).
   - **Automatic peer selection** — omit `agent` and a Claude client talks to codex, a Codex client talks to claude.
   - **Read-only peers** — claude runs with `--bare --permission-mode dontAsk --tools Read,Grep,Glob,Bash --allowedTools <read-only git> --disallowedTools Edit,Write,NotebookEdit`; codex chat runs `--sandbox read-only`. `--bare` / `mcp_servers.<name>.enabled=false` stop the peer from calling hostrunner recursively.
   - **`codex_review_start` / `codex_review_status`** — deprecated aliases, still work.
   - **`run_command`**, strict security, config, audit log — unchanged bullets.
3. **Requirements** table: add a row `**claude CLI** | Needed for \`agent=claude\` (installed and logged in; part of Claude Code)`, and change the codex row to `Needed for \`agent=codex\``. Add `**Codex app / CLI** | Alternative host to Claude Desktop (see "Install for Codex")`.
4. After the Claude plugin install section add:

````markdown
### Install for Codex (CLI, macOS app, IDE extension)

```bash
git clone https://github.com/jerryctt/hostrunner-mcp.git
cd hostrunner-mcp
scripts/install-codex.sh            # or: scripts/install-codex.sh --local ./hostrunner
```

This downloads and verifies the release binary, writes a
`[mcp_servers.hostrunner]` block to `~/.codex/config.toml` (with
`tool_timeout_sec = 120`), installs the `peer-review` and `peer-chat` skills
into `~/.agents/skills/`, and creates `~/.config/hostrunner/config.yaml` if
missing. Edit `allowed_roots`, then restart the Codex app. In Codex, say
"peer review my changes" or "ask claude what it thinks about this design".

Manual equivalent:

```toml
# ~/.codex/config.toml
[mcp_servers.hostrunner]
command = "/Users/you/.local/bin/hostrunner"
tool_timeout_sec = 120
startup_timeout_sec = 20
```
````

5. Replace the config example block with the new `examples/config.example.yaml` content (Step 1).
6. Add a **Tools** section after Features:

````markdown
## Tools

| Tool | Purpose |
|---|---|
| `review_start(folder, agent?, scope?, base?, commit?, prompt?)` | Start a read-only review; returns `job_id`. |
| `chat_start(folder, message, agent?, session_id?)` | Send one message to the peer; returns `job_id`. Pass the `session_id` from the previous reply to continue. |
| `job_status(job_id)` | Long-poll (≤50s) for a job's result. |
| `codex_review_start`, `codex_review_status` | Deprecated aliases of `review_start(agent=codex)` / `job_status`. |
| `run_command(command, args, folder)` | Allowlisted argv in a host folder. |

Example discussion from Codex (peer = claude):

```
chat_start(folder="/Users/you/proj", message="Critique the retry design in the uncommitted diff.")
job_status(job_id="…")   → session_id: abc…  --- Claude reply --- …
chat_start(folder="/Users/you/proj", session_id="abc…", message="Point 2 only applies to POST; does that change your view?")
```
````

7. In the **Security** notes, replace the "Read-only codex" bullet with a "Read-only peers" bullet summarising the flags above and stating that `allowed_commands` is not consulted for peer agents (their argv is fixed by the server; only `agents.<a>.command` and `extra_args` come from config).
8. Everywhere else: `codex_review_start` → `review_start`, `codex_review_status` → `job_status`, version `0.2.1` → `0.3.0`.

- [ ] **Step 3: Mirror the same edits in `README_zh.md`** (繁體中文), same structure and section order.

- [ ] **Step 4: Sanity check links/names**

Run: `grep -n "codex_review_st" README.md README_zh.md | grep -v -i "deprecated\|舊\|alias" || echo clean`
Expected: `clean` (old names only appear where marked deprecated).

- [ ] **Step 5: Commit**

```bash
git add README.md README_zh.md examples
git commit -m "docs: document peer agents, Codex installation, new tools and config"
```

---

### Task 11: Final verification, sync to the Mac, commit there, codex review

**Files:** none new.

- [ ] **Step 1: Full test + build in the cloud clone**

Run: `cd /home/claude/hostrunner-mcp && gofmt -l . && go vet ./... && go test -race ./... && go build -o /tmp/hostrunner ./cmd/hostrunner && /tmp/hostrunner -version`
Expected: `gofmt -l` prints nothing; all packages `ok`; version prints `dev`.

- [ ] **Step 2: Smoke-run the MCP server with a fake peer**

```bash
T=$(mktemp -d); mkdir -p "$T/bin" "$T/repo"
cat > "$T/bin/claude" <<'EOF'
#!/bin/sh
printf '{"type":"result","subtype":"success","is_error":false,"result":"stub reply: %s","session_id":"stub-1"}' "$2"
EOF
chmod +x "$T/bin/claude"
cat > "$T/config.yaml" <<EOF
allowed_roots: ["$T/repo"]
allowed_commands: [git]
timeout: 30s
stream_output: false
agents:
  claude:
    command: $T/bin/claude
EOF
printf '%s\n' \
 '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"codex-cli","version":"0"}}}' \
 '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
 "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"chat_start\",\"arguments\":{\"folder\":\"$T/repo\",\"message\":\"hi\"}}}" \
 | /tmp/hostrunner -config "$T/config.yaml" 2>/dev/null | head -5
```

Expected: the third response text contains `agent: claude` (inferred from the `codex-cli` client name) and a `job_id`.

- [ ] **Step 3: Sync changed files to the user's Mac**

```bash
cd /home/claude/hostrunner-mcp
git diff --name-only 8e77ab2 HEAD > /tmp/changed.txt; cat /tmp/changed.txt
rm -rf /mnt/user-data/outputs/hostrunner-sync && mkdir -p /mnt/user-data/outputs/hostrunner-sync
while read -r f; do mkdir -p "/mnt/user-data/outputs/hostrunner-sync/$(dirname "$f")"; cp "$f" "/mnt/user-data/outputs/hostrunner-sync/$f"; done < /tmp/changed.txt
```

Then one `device_commit_files` call (≤50 files) mapping each `/mnt/user-data/outputs/hostrunner-sync/<f>` to `/Users/jerryctt/code/personal/hostrunner-mcp/<f>`. The spec and plan docs are already committed on the Mac / in the clone respectively; include the plan file.

- [ ] **Step 4: Verify and commit on the Mac** (via `device_bash`)

```sh
cd $HOME/mnt/hostrunner-mcp && chmod +x scripts/*.sh && git status --short && git add -A -- . ':!.claude' && git -c user.name="Jerry Chan" -c user.email="jerrycttdev@gmail.com" commit -m "feat: peer agents — Claude <-> Codex review and chat from either host (v0.3.0)

- internal/agent Backend interface; codex + new claude backends
- review_start / chat_start / job_status tools, codex_* kept as aliases
- jobs keyed by (worktree, agent, kind); peer inferred from MCP client
- agents: config block; scripts/install-codex.sh; peer-review/peer-chat skills

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Y4RtcRnPii6ch1oiyDs8bA" && git log --oneline -3
```

Expected: one commit; `git status --short` afterwards shows only `?? .claude/`.

- [ ] **Step 5: Codex review of the change**

Invoke the `hostrunner:codex-loop` skill against `/Users/jerryctt/code/personal/hostrunner-mcp` with `scope=commit` and the SHA from Step 4 (the work is already committed), fix any real findings in the cloud clone, re-run `go test ./...`, re-sync (Step 3) and amend/commit on the Mac, then re-review until clean.

- [ ] **Step 6: Hand-off notes for the user (manual E2E, not automatable here)**

Report: (a) run `scripts/install-codex.sh --local <built binary>` on the Mac, (b) in the Codex app try `review_start` and a two-round `chat_start`, (c) in Claude Desktop try `chat_start` (peer = codex) two rounds, (d) if `codex exec resume` rejects `--sandbox`/`--skip-git-repo-check` placement, report the error text so the `ChatArgs` order can be adjusted (verify with `codex exec resume --help`), (e) push to GitHub and tag `v0.3.0` so the launcher/installer can download the release.
