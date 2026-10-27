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
