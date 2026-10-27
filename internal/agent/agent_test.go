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
	for _, want := range []string{"base branch main", "focus on concurrency", "Do not modify", "--- diff ---", "Read, Grep and Glob", "no issues"} {
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

func (s stub) Name() string                                         { return s.name }
func (s stub) Command() string                                      { return s.name }
func (s stub) ReviewArgs(_, _, _, _ string) ([]string, bool, error) { return nil, false, nil }
func (s stub) ChatArgs(_, _ string) []string                        { return nil }
func (s stub) ParseChat(_ string) (string, string, error)           { return "", "", nil }

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
