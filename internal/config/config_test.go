package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDefaultsAndValues(t *testing.T) {
	root := t.TempDir()
	p := writeTemp(t, "allowed_roots:\n  - "+root+"\nallowed_commands:\n  - codex\n  - git\n")
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Timeout != 180*time.Second {
		t.Errorf("default timeout = %v, want 180s", c.Timeout)
	}
	if c.MaxOutputBytes != 200000 {
		t.Errorf("default max = %d, want 200000", c.MaxOutputBytes)
	}
	if !c.CommandAllowed("codex") || c.CommandAllowed("rm") {
		t.Errorf("CommandAllowed wrong")
	}
}

func TestResolveAllowedDir(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "proj")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	c := &Config{AllowedRoots: []string{root}}

	got, err := c.ResolveAllowedDir(sub)
	if err != nil {
		t.Fatalf("inside root should pass: %v", err)
	}
	if got == "" {
		t.Errorf("expected resolved path")
	}

	if _, err := c.ResolveAllowedDir("/etc"); err == nil {
		t.Errorf("outside root should fail")
	}
	if _, err := c.ResolveAllowedDir("/sessions/x/mnt/proj"); err == nil {
		t.Errorf("sandbox path should fail")
	}
}

func TestStreamOutputDefaultTrue(t *testing.T) {
	root := t.TempDir()
	p := writeTemp(t, "allowed_roots:\n  - "+root+"\n")
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.StreamEnabled() {
		t.Errorf("StreamEnabled() = false, want true (default on)")
	}
}

func TestStreamOutputExplicitFalse(t *testing.T) {
	root := t.TempDir()
	p := writeTemp(t, "allowed_roots:\n  - "+root+"\nstream_output: false\n")
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.StreamEnabled() {
		t.Errorf("StreamEnabled() = true, want false when stream_output: false")
	}
}

func TestResolvePath(t *testing.T) {
	t.Run("explicit non-empty returns it", func(t *testing.T) {
		got := ResolvePath("/x/y.yaml")
		if got != "/x/y.yaml" {
			t.Fatalf("expected /x/y.yaml, got %q", got)
		}
	})

	t.Run("explicit empty + env set returns env", func(t *testing.T) {
		t.Setenv("HOSTRUNNER_CONFIG", "/from/env.yaml")
		got := ResolvePath("")
		if got != "/from/env.yaml" {
			t.Fatalf("expected /from/env.yaml, got %q", got)
		}
	})

	t.Run("explicit empty + env empty returns default path", func(t *testing.T) {
		t.Setenv("HOSTRUNNER_CONFIG", "")
		got := ResolvePath("")
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			t.Skip("os.UserHomeDir() unavailable; skipping default path check")
		}
		want := filepath.Join(".config", "hostrunner", "config.yaml")
		if !strings.HasSuffix(got, want) {
			t.Fatalf("expected path ending in %q, got %q", want, got)
		}
	})
}

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
