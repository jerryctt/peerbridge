package claude

import (
	"strings"
	"testing"

	"github.com/jerryctt/peerbridge/internal/agent"
)

func TestReviewArgsIsNotNative(t *testing.T) {
	b := New(agent.Settings{Command: "claude"})
	args, native, err := b.ReviewArgs("uncommitted", "", "", "", agent.Options{})
	if err != nil || native || args != nil {
		t.Errorf("want nil,false,nil got %v,%v,%v", args, native, err)
	}
	if _, _, err := b.ReviewArgs("base", "", "", "", agent.Options{}); err == nil {
		t.Error("base without ref should error")
	}
}

func TestChatArgs(t *testing.T) {
	b := New(agent.Settings{Command: "claude", ExtraArgs: []string{"--model", "opus"}})
	args := b.ChatArgs("--help", "", agent.Options{})
	if args[0] != "-p" || args[len(args)-2] != "--" || args[len(args)-1] != "--help" {
		t.Errorf("message must be the final positional after --: %v", args)
	}
	joined := strings.Join(args, "\x1f") // visible separator so the empty value is checkable
	for _, want := range []string{
		"--strict-mcp-config", "--setting-sources\x1f\x1f", "--output-format\x1fjson", "--permission-mode\x1fdontAsk",
		"--tools\x1fRead,Grep,Glob\x1f",
		"--allowedTools\x1fRead,Grep,Glob\x1f",
		"--disallowedTools\x1fEdit,Write,NotebookEdit,Bash",
		"--model\x1fopus",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %q", want, joined)
		}
	}
	for _, bad := range []string{"--resume", "dangerously", "acceptEdits", "--bare", "Bash(git"} {
		if strings.Contains(joined, bad) {
			t.Errorf("unexpected %q in %q", bad, joined)
		}
	}
	// No shell for the peer: Bash must not be in the available tool set.
	for i, a := range args {
		if a == "--tools" && strings.Contains(args[i+1], "Bash") {
			t.Errorf("--tools must not include Bash: %q", args[i+1])
		}
	}
	resumed := strings.Join(b.ChatArgs("again", "sess-1", agent.Options{}), " ")
	if !strings.Contains(resumed, "--resume sess-1 --model opus -- again") {
		t.Errorf("resume/extra/terminator order wrong: %q", resumed)
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

func TestChatArgsOptions(t *testing.T) {
	b := New(agent.Settings{Command: "claude"})
	got := strings.Join(b.ChatArgs("hi", "s1", agent.Options{Model: "sonnet", Effort: "high"}), " ")
	if !strings.HasSuffix(got, "--resume s1 --model sonnet --effort high -- hi") {
		t.Errorf("options placement wrong: %q", got)
	}
	if got := strings.Join(b.ChatArgs("hi", "", agent.Options{}), " "); strings.Contains(got, "--model") || strings.Contains(got, "--effort") {
		t.Errorf("zero options must add no flags: %q", got)
	}
}

func TestCheckOptions(t *testing.T) {
	plain := New(agent.Settings{Command: "claude"})
	if err := plain.CheckOptions(agent.Options{Model: "opus", Effort: "high"}); err != nil {
		t.Errorf("no extra_args: %v", err)
	}
	for _, extra := range [][]string{{"--model", "opus"}, {"--model=opus"}} {
		b := New(agent.Settings{Command: "claude", ExtraArgs: extra})
		if err := b.CheckOptions(agent.Options{Model: "sonnet"}); err == nil {
			t.Errorf("extra %v + model should conflict", extra)
		}
		if err := b.CheckOptions(agent.Options{Effort: "high"}); err != nil {
			t.Errorf("extra %v + effort only should not conflict: %v", extra, err)
		}
	}
	b := New(agent.Settings{Command: "claude", ExtraArgs: []string{"--effort=low"}})
	if err := b.CheckOptions(agent.Options{Effort: "high"}); err == nil {
		t.Error("extra --effort= + effort should conflict")
	}
}
