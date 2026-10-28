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
	// allowed_commands is obsolete (run_command was removed) but old configs
	// that still set it must keep loading.
	if len(c.AllowedCommands) != 2 {
		t.Errorf("legacy allowed_commands not parsed: %v", c.AllowedCommands)
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

	outside := t.TempDir()
	if _, err := c.ResolveAllowedDir(outside); err == nil || !strings.Contains(err.Error(), "allowed_root") {
		t.Errorf("outside root should fail with an allowed_root error, got %v", err)
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
		t.Setenv("HOSTRUNNER_CONFIG", "")
		t.Setenv("PEERBRIDGE_CONFIG", "/from/env.yaml")
		got := ResolvePath("")
		if got != "/from/env.yaml" {
			t.Fatalf("expected /from/env.yaml, got %q", got)
		}
	})

	t.Run("explicit empty + env empty returns default path", func(t *testing.T) {
		// Hermetic: a developer's real ~/.config/hostrunner (legacy) or
		// exported HOSTRUNNER_CONFIG must not influence the default.
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
		t.Setenv("AppData", filepath.Join(home, "AppData", "Roaming"))
		t.Setenv("PEERBRIDGE_CONFIG", "")
		t.Setenv("HOSTRUNNER_CONFIG", "")
		got := ResolvePath("")
		want := filepath.Join(".config", "peerbridge", "config.yaml")
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
	if got := c.Agent("codex"); got.Command != "codex" {
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
		"  codex:\n    command: /opt/homebrew/bin/codex\n"+
		"  claude:\n    command: /usr/local/bin/claude\n    extra_args: [\"--model\", \"opus\"]\n")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	cx := c.Agent("codex")
	if cx.Command != "/opt/homebrew/bin/codex" {
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

func TestResolvePathPrefersExistingCandidate(t *testing.T) {
	home := t.TempDir()
	// Cover every variable os.UserHomeDir / os.UserConfigDir consult, so the
	// test is hermetic on Linux, macOS and Windows and never touches a real
	// %APPDATA% or ~/.config.
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("AppData", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("PEERBRIDGE_CONFIG", "")
	t.Setenv("HOSTRUNNER_CONFIG", "")

	paths := DefaultPaths()
	if len(paths) < 2 {
		t.Fatalf("want at least two candidates (~/.config and the platform config dir), got %v", paths)
	}
	if want := filepath.Join(home, ".config", "peerbridge", "config.yaml"); paths[0] != want {
		t.Errorf("first candidate = %q, want %q", paths[0], want)
	}
	// Nothing exists yet: fall back to the conventional first candidate.
	if got := ResolvePath(""); got != paths[0] {
		t.Errorf("ResolvePath with nothing present = %q, want %q", got, paths[0])
	}
	// Only the platform config dir has a file: it wins.
	alt := paths[1]
	if err := os.MkdirAll(filepath.Dir(alt), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(alt, []byte("allowed_roots: [/x]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ResolvePath(""); got != alt {
		t.Errorf("ResolvePath = %q, want existing %q", got, alt)
	}
	// Explicit and env still take precedence over everything.
	t.Setenv("PEERBRIDGE_CONFIG", "/env/c.yaml")
	if ResolvePath("") != "/env/c.yaml" || ResolvePath("/flag.yaml") != "/flag.yaml" {
		t.Error("explicit/env precedence broken")
	}
}

func TestStatusPollDefaultAndBounds(t *testing.T) {
	root := t.TempDir()
	c, err := Load(writeTemp(t, "allowed_roots:\n  - "+root+"\n"))
	if err != nil || c.StatusPoll != 50*time.Second {
		t.Fatalf("default status_poll = %v err=%v, want 50s", c.StatusPoll, err)
	}
	c, err = Load(writeTemp(t, "allowed_roots:\n  - "+root+"\nstatus_poll: 100s\n"))
	if err != nil || c.StatusPoll != 100*time.Second {
		t.Errorf("status_poll = %v err=%v, want 100s", c.StatusPoll, err)
	}
	if _, err := Load(writeTemp(t, "allowed_roots:\n  - "+root+"\nstatus_poll: 10m\n")); err == nil || !strings.Contains(err.Error(), "status_poll") {
		t.Errorf("out-of-range status_poll should be rejected, got %v", err)
	}
}

func TestLegacyHostrunnerLocationsStillWork(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("AppData", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("PEERBRIDGE_CONFIG", "")
	t.Setenv("HOSTRUNNER_CONFIG", "")

	paths := DefaultPaths()
	if !strings.Contains(paths[0], "peerbridge") {
		t.Errorf("new name must come first: %v", paths)
	}
	legacy := filepath.Join(home, ".config", "hostrunner", "config.yaml")
	found := false
	for _, p := range paths {
		if p == legacy {
			found = true
		}
	}
	if !found {
		t.Fatalf("legacy ~/.config/hostrunner path missing from %v", paths)
	}
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("allowed_roots: [/x]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ResolvePath(""); got != legacy {
		t.Errorf("existing legacy config should be picked up, got %q", got)
	}
	// Legacy env var still honoured, new one wins.
	t.Setenv("HOSTRUNNER_CONFIG", "/old/env.yaml")
	if ResolvePath("") != "/old/env.yaml" {
		t.Error("HOSTRUNNER_CONFIG should still be honoured")
	}
	t.Setenv("PEERBRIDGE_CONFIG", "/new/env.yaml")
	if ResolvePath("") != "/new/env.yaml" {
		t.Error("PEERBRIDGE_CONFIG must take precedence")
	}
}
