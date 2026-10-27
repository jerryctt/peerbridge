package codex

import (
	"strings"
	"testing"

	"github.com/jerryctt/hostrunner-mcp/internal/agent"
)

func TestReviewArgs(t *testing.T) {
	// "" and "uncommitted" both produce ["review", "--uncommitted"]
	for _, mode := range []string{"", "uncommitted"} {
		got, positional, err := ReviewArgs(mode, "", "", "")
		if err != nil {
			t.Errorf("mode=%q: unexpected error %v", mode, err)
		}
		if strings.Join(got, " ") != "review --uncommitted" || positional != "" {
			t.Errorf("mode=%q: got %v %q, want [review --uncommitted] and no positional", mode, got, positional)
		}
	}

	// base mode -- requires base ref
	got, positional, err := ReviewArgs("base", "main", "", "")
	if err != nil {
		t.Fatalf("base mode: %v", err)
	}
	if strings.Join(got, " ") != "review --base main" || positional != "" {
		t.Errorf("base mode: got %v %q", got, positional)
	}
	if _, _, err := ReviewArgs("base", "", "", ""); err == nil {
		t.Errorf("base without ref should error")
	}

	// commit mode -- requires sha
	got, positional, err = ReviewArgs("commit", "", "abc123", "")
	if err != nil {
		t.Fatalf("commit mode: %v", err)
	}
	if strings.Join(got, " ") != "review --commit abc123" || positional != "" {
		t.Errorf("commit mode: got %v %q", got, positional)
	}
	if _, _, err := ReviewArgs("commit", "", "", ""); err == nil {
		t.Errorf("commit without sha should error")
	}

	// unknown mode
	if _, _, err := ReviewArgs("unknown-mode", "", "", ""); err == nil {
		t.Errorf("unknown mode should error")
	}
}

// The codex CLI rejects scope flags combined with the positional [PROMPT]
// ("the argument '--uncommitted' cannot be used with '[PROMPT]'"), so a
// non-empty prompt must suppress the scope flag and fold the scope into the
// prompt text instead.
func TestReviewArgsPromptSuppressesScopeFlags(t *testing.T) {
	cases := []struct {
		mode, base, commit string
		wantInPrompt       string
	}{
		{"", "", "", "uncommitted"},
		{"uncommitted", "", "", "uncommitted"},
		{"base", "main", "", "base branch main"},
		{"commit", "", "abc123", "commit abc123"},
	}
	for _, c := range cases {
		args, positional, err := ReviewArgs(c.mode, c.base, c.commit, "focus on error handling")
		if err != nil {
			t.Fatalf("mode=%q: %v", c.mode, err)
		}
		if strings.Join(args, " ") != "review" {
			t.Errorf("mode=%q: args = %v, want [review] only (no scope flag with prompt)", c.mode, args)
		}
		if !strings.Contains(positional, c.wantInPrompt) {
			t.Errorf("mode=%q: positional %q should mention %q", c.mode, positional, c.wantInPrompt)
		}
		if !strings.Contains(positional, "focus on error handling") {
			t.Errorf("mode=%q: positional %q should contain the caller prompt", c.mode, positional)
		}
	}

	// base/commit validation still applies with a prompt
	if _, _, err := ReviewArgs("base", "", "", "p"); err == nil {
		t.Errorf("base without ref should error even with prompt")
	}
	if _, _, err := ReviewArgs("commit", "", "", "p"); err == nil {
		t.Errorf("commit without sha should error even with prompt")
	}
}

func newBackend() *Backend {
	return New(agent.Settings{Command: "codex"})
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
	want := "exec --json --sandbox read-only --skip-git-repo-check --ignore-user-config -- hello there"
	if got != want {
		t.Errorf("new session:\n got %q\nwant %q", got, want)
	}
	got = strings.Join(b.ChatArgs("more", "thread-123"), " ")
	// `exec resume` rejects --sandbox; the policy goes through -c instead.
	want = "exec resume --json --skip-git-repo-check --ignore-user-config -c sandbox_mode=\"read-only\" -- thread-123 more"
	if got != want {
		t.Errorf("resume:\n got %q\nwant %q", got, want)
	}
	// Extra args go after the fixed flags, before the positionals.
	b2 := New(agent.Settings{Command: "codex", ExtraArgs: []string{"-m", "o3"}})
	got = strings.Join(b2.ChatArgs("x", ""), " ")
	if !strings.HasSuffix(got, "--ignore-user-config -m o3 -- x") {
		t.Errorf("args = %q", got)
	}
	for _, a := range b.ChatArgs("x", "t") {
		if a == "--sandbox" {
			t.Errorf("resume must not receive --sandbox (codex rejects it): %v", b.ChatArgs("x", "t"))
		}
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
