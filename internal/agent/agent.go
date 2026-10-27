// Package agent defines the host-CLI "peer agent" abstraction shared by the
// codex and claude backends, plus helpers the server uses to pick a backend
// and build review prompts.
package agent

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jerryctt/peerbridge/internal/exec"
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

// refPattern is the set of git revision spellings we accept: refs, SHAs,
// and the usual suffixes (origin/main, v1.2.0, HEAD~3, abc123^, a...b). It
// deliberately excludes a leading '-' so a caller-supplied value can never
// be parsed as an option by git or by the peer CLI.
var refPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._/@^~+-]*$`)

// ValidateRef rejects revisions that are empty, option-like, or contain
// characters outside refPattern.
func ValidateRef(kind, ref string) error {
	if ref == "" {
		return fmt.Errorf("mode=%s requires a %s", kind, map[string]string{"base": "base ref", "commit": "commit sha"}[kind])
	}
	if strings.HasPrefix(ref, "-") || !refPattern.MatchString(ref) {
		return fmt.Errorf("invalid %s %q: must be a git revision (letters, digits, . _ / @ ^ ~ + -) and must not start with '-'", kind, ref)
	}
	return nil
}

// sessionPattern accepts UUIDs and the thread names/ids the CLIs hand out;
// like refPattern it forbids a leading '-' so an id can never be an option.
var sessionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidateSessionID rejects option-like or otherwise malformed session ids.
// Empty is allowed (it means "start a new session").
func ValidateSessionID(id string) error {
	if id == "" {
		return nil
	}
	if !sessionPattern.MatchString(id) {
		return fmt.Errorf("invalid session_id %q: expected a UUID or thread id (letters, digits, . _ -), not starting with '-'", id)
	}
	return nil
}

// ScopeSentence describes the review scope in prose and validates the
// scope/base/commit combination. The wording is shared by both backends.
func ScopeSentence(scope, base, commit string) (string, error) {
	switch scope {
	case "", "uncommitted":
		return "Review the uncommitted changes (staged, unstaged, and untracked).", nil
	case "base":
		if err := ValidateRef("base", base); err != nil {
			return "", err
		}
		return fmt.Sprintf("Review the changes against base branch %s.", base), nil
	case "commit":
		if err := ValidateRef("commit", commit); err != nil {
			return "", err
		}
		return fmt.Sprintf("Review the changes introduced by commit %s.", commit), nil
	default:
		return "", fmt.Errorf("unknown mode %q", scope)
	}
}

const reviewInstructions = "You are a read-only code reviewer working in the current repository. " +
	"The diff under review is provided as attached input (it begins with a line reading '--- diff ---'); use the Read, Grep and Glob tools to inspect surrounding code as needed (you have no shell). " +
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
