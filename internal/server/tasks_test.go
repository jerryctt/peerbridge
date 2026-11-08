package server

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jerryctt/peerbridge/internal/agent"
	"github.com/jerryctt/peerbridge/internal/config"
	pbexec "github.com/jerryctt/peerbridge/internal/exec"
	"github.com/rs/zerolog"
)

// hybridRunner runs git for real (worktrees need it) and fakes the peer CLI
// with peer, which may edit files in req.Dir like a real task would.
type hybridRunner struct {
	mu   sync.Mutex
	peer func(req pbexec.Request) pbexec.Result
	reqs []pbexec.Request
}

func (h *hybridRunner) Run(ctx context.Context, req pbexec.Request) (pbexec.Result, error) {
	if req.Command == "git" {
		return pbexec.Runner{}.Run(ctx, req)
	}
	h.mu.Lock()
	h.reqs = append(h.reqs, req)
	h.mu.Unlock()
	return h.peer(req), nil
}

func (h *hybridRunner) peerReqs() []pbexec.Request {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]pbexec.Request(nil), h.reqs...)
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return string(out)
}

// taskSvc returns a service with tasks enabled and a git repo with one commit.
func taskSvc(t *testing.T, h *hybridRunner) (*Service, string) {
	t.Helper()
	cfg, repo := newCfg(t)
	cfg.Timeout = 30 * time.Second
	cfg.StateDir = t.TempDir()
	cfg.Tasks = config.TasksConfig{Enabled: true, Timeout: 30 * time.Second, MaxRunning: 2}
	run(t, repo, "git", "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "app.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "git", "add", "-A")
	run(t, repo, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "init")
	return NewService(cfg, h, zerolog.Nop()), repo
}

// editingPeer edits app.txt and adds new.txt in the worktree, then replies
// with the given conversation id.
func editingPeer(thread string) func(pbexec.Request) pbexec.Result {
	return func(req pbexec.Request) pbexec.Result {
		_ = os.WriteFile(filepath.Join(req.Dir, "app.txt"), []byte("one\ntwo\n"), 0o600)
		_ = os.WriteFile(filepath.Join(req.Dir, "new.txt"), []byte("brand new\n"), 0o600)
		return pbexec.Result{Stdout: `{"type":"thread.started","thread_id":"` + thread + `"}` + "\n" +
			`{"type":"item.completed","item":{"type":"agent_message","text":"DONE_EDITING"}}` + "\n"}
	}
}

func field(t *testing.T, out, name string) string {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, name+": ") {
			return strings.TrimSpace(strings.TrimPrefix(l, name+": "))
		}
	}
	t.Fatalf("no %q line in %q", name, out)
	return ""
}

func startTask(t *testing.T, s *Service, a, repo, msg, conv, base string) (string, bool) {
	t.Helper()
	out, isErr := s.TaskStart(context.Background(), a, repo, msg, conv, base, agent.Options{})
	if isErr {
		return out, true
	}
	return waitStatus(t, s, jobID(t, out))
}

func TestTaskDisabledByDefault(t *testing.T) {
	s, repo := newSvc(t, &fakeRunner{})
	out, isErr := s.TaskStart(context.Background(), "codex", repo, "do it", "", "", agent.Options{})
	if !isErr || !strings.Contains(out, "tasks: {enabled: true}") {
		t.Errorf("want disabled error, got %q", out)
	}
}

func TestTaskEditsOnlyTheWorktreeAndSavesACompletePatch(t *testing.T) {
	h := &hybridRunner{peer: editingPeer("thr-1")}
	s, repo := taskSvc(t, h)
	out, isErr := startTask(t, s, "codex", repo, "add two", "", "")
	if isErr {
		t.Fatalf("task: %q", out)
	}
	wt, patch := field(t, out, "worktree"), field(t, out, "patch")
	for _, want := range []string{"Task completed", "conversation_id: thr-1", "session_id: thr-1", "DONE_EDITING", "+two", "new.txt", "staged in the worktree, not committed", "apply --index", "worktree remove"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	if !strings.HasPrefix(wt, s.Cfg.StateDir) {
		t.Errorf("worktree %s must be under state dir %s", wt, s.Cfg.StateDir)
	}
	// The user's checkout is untouched.
	if got, _ := os.ReadFile(filepath.Join(repo, "app.txt")); string(got) != "one\n" {
		t.Errorf("checkout modified: %q", got)
	}
	if st := run(t, repo, "git", "status", "--porcelain"); st != "" {
		t.Errorf("checkout not clean: %q", st)
	}
	// The patch is complete (includes the untracked new file) and applies.
	run(t, repo, "git", "apply", "--check", "--index", patch)
	// The peer ran in the worktree with a private TMPDIR, using TaskArgs.
	req := h.peerReqs()[0]
	if req.Dir != wt || !strings.Contains(strings.Join(req.Args, " "), "workspace-write") {
		t.Errorf("peer dir/args wrong: %s %v", req.Dir, req.Args)
	}
	if len(req.Env) != 1 || !strings.HasPrefix(req.Env[0], "TMPDIR="+s.Cfg.StateDir) {
		t.Errorf("TMPDIR not private: %v", req.Env)
	}
	if _, err := os.Stat(strings.TrimPrefix(req.Env[0], "TMPDIR=")); !os.IsNotExist(err) {
		t.Errorf("per-job temp dir should be removed after the turn: %v", err)
	}
	if rec, err := s.Tasks.byConversation("codex", "thr-1"); err != nil || rec == nil || rec.Worktree != wt || rec.LastPatch != patch {
		t.Errorf("task record = %+v, %v", rec, err)
	}
}

func TestTaskContinueReusesWorktree(t *testing.T) {
	h := &hybridRunner{peer: editingPeer("thr-1")}
	s, repo := taskSvc(t, h)
	first, _ := startTask(t, s, "codex", repo, "add two", "", "")
	wt := field(t, first, "worktree")
	h.peer = func(req pbexec.Request) pbexec.Result {
		_ = os.WriteFile(filepath.Join(req.Dir, "third.txt"), []byte("3\n"), 0o600)
		return editingPeer("thr-1")(req)
	}
	second, isErr := startTask(t, s, "codex", repo, "and a third file", "thr-1", "")
	if isErr || field(t, second, "worktree") != wt {
		t.Fatalf("continuation must reuse %s: %q", wt, second)
	}
	reqs := h.peerReqs()
	if args := strings.Join(reqs[1].Args, " "); !strings.Contains(args, "exec resume") || !strings.Contains(args, "-- thr-1 and a third file") {
		t.Errorf("second turn must resume thr-1: %q", args)
	}
	if !strings.Contains(second, "third.txt") || !strings.Contains(second, "new.txt") {
		t.Errorf("patch must hold all changes since base: %q", second)
	}
	if _, isErr := s.TaskStart(context.Background(), "codex", repo, "x", "thr-1", "main", agent.Options{}); !isErr {
		t.Error("base with an existing task conversation should be rejected")
	}
}

func TestTaskFromChatConversationGetsNewWorktree(t *testing.T) {
	h := &hybridRunner{peer: editingPeer("chat-9")}
	s, repo := taskSvc(t, h)
	out, isErr := startTask(t, s, "codex", repo, "implement what we discussed", "chat-9", "")
	if isErr {
		t.Fatalf("task: %q", out)
	}
	if args := strings.Join(h.peerReqs()[0].Args, " "); !strings.Contains(args, "exec resume") || !strings.Contains(args, "chat-9") {
		t.Errorf("must resume the chat conversation: %q", args)
	}
	field(t, out, "worktree")
}

func TestTaskFailureKeepsWorkRecoverable(t *testing.T) {
	h := &hybridRunner{peer: func(req pbexec.Request) pbexec.Result {
		_ = os.WriteFile(filepath.Join(req.Dir, "partial.txt"), []byte("half\n"), 0o600)
		return pbexec.Result{Stdout: `{"type":"thread.started","thread_id":"thr-f"}` + "\n", Stderr: "boom at the end", ExitCode: 1}
	}}
	s, repo := taskSvc(t, h)
	out, isErr := startTask(t, s, "codex", repo, "try", "", "")
	if !isErr {
		t.Errorf("failed task must be an error result: %q", out)
	}
	for _, want := range []string{"Task FAILED", "conversation_id: thr-f", "partial.txt", "boom at the end"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	run(t, repo, "git", "apply", "--check", field(t, out, "patch"))
}

func TestTaskIgnoresTamperedDotGit(t *testing.T) {
	other := t.TempDir()
	run(t, other, "git", "init", "-q")
	h := &hybridRunner{peer: func(req pbexec.Request) pbexec.Result {
		_ = os.WriteFile(filepath.Join(req.Dir, "evil.txt"), []byte("x\n"), 0o600)
		// Point the worktree at another repository; peerbridge must not follow.
		_ = os.WriteFile(filepath.Join(req.Dir, ".git"), []byte("gitdir: "+filepath.Join(other, ".git")+"\n"), 0o600)
		return editingPeer("thr-t")(req)
	}}
	s, repo := taskSvc(t, h)
	out, _ := startTask(t, s, "codex", repo, "x", "", "")
	if st := run(t, other, "git", "status", "--porcelain"); strings.Contains(st, "A ") {
		t.Errorf("staged into the other repo: %q", st)
	}
	if !strings.Contains(out, "evil.txt") {
		t.Errorf("changes must still be collected via the recorded git dir: %q", out)
	}
	if !strings.Contains(out, "WARNING: the worktree's .git file") || strings.Contains(out, "git -C "+field(t, out, "worktree")+" commit") {
		t.Errorf("tampering must be flagged and the commit recipe must not follow .git: %q", out)
	}
	if !strings.Contains(out, "--git-dir=") {
		t.Errorf("commit recipe must name the git dir explicitly: %q", out)
	}
}

func TestTaskLocks(t *testing.T) {
	release := make(chan struct{})
	h := &hybridRunner{peer: func(req pbexec.Request) pbexec.Result {
		<-release
		return pbexec.Result{Stdout: `{"type":"thread.started","thread_id":"c1"}` + "\n"}
	}}
	s, repo := taskSvc(t, h)
	s.Cfg.Tasks.MaxRunning = 2
	a, isErr := s.TaskStart(context.Background(), "codex", repo, "one", "c1", "", agent.Options{})
	if isErr {
		t.Fatal(a)
	}
	if out, isErr := s.TaskStart(context.Background(), "codex", repo, "same conversation", "c1", "", agent.Options{}); !isErr || !strings.Contains(out, "already has a running") {
		t.Errorf("same conversation must be refused: %q", out)
	}
	if out, isErr := s.ChatStart(context.Background(), "codex", repo, "chat in it", "c1", agent.Options{}); !isErr || !strings.Contains(out, "already has a running") {
		t.Errorf("chat in a running task's conversation must be refused: %q", out)
	}
	b, isErr := s.TaskStart(context.Background(), "claude", repo, "parallel claude", "", "", agent.Options{})
	if isErr {
		t.Errorf("codex and claude may run side by side: %q", b)
	}
	if out, isErr := s.TaskStart(context.Background(), "codex", repo, "third", "", "", agent.Options{}); !isErr || !strings.Contains(out, "max_running") {
		t.Errorf("third task must hit max_running: %q", out)
	}
	close(release)
	waitStatus(t, s, jobID(t, a))
	waitStatus(t, s, jobID(t, b))
}

func TestTaskNotesDirtyCheckoutAndBase(t *testing.T) {
	h := &hybridRunner{peer: editingPeer("thr-d")}
	s, repo := taskSvc(t, h)
	head := strings.TrimSpace(run(t, repo, "git", "rev-parse", "HEAD"))
	_ = os.WriteFile(filepath.Join(repo, "app.txt"), []byte("local edit\n"), 0o600)
	out, isErr := startTask(t, s, "codex", repo, "x", "", "main")
	if isErr || !strings.Contains(out, "uncommitted changes; they are NOT in this worktree") || !strings.Contains(out, head[:12]) {
		t.Errorf("want dirty note and base %s: %q", head[:12], out)
	}
	if out, isErr := s.TaskStart(context.Background(), "codex", repo, "x", "", "-evil", agent.Options{}); !isErr {
		t.Errorf("option-like base must be rejected: %q", out)
	}
}

func TestTaskClaudeNotesNoTests(t *testing.T) {
	h := &hybridRunner{peer: func(req pbexec.Request) pbexec.Result {
		_ = os.WriteFile(filepath.Join(req.Dir, "c.txt"), []byte("c\n"), 0o600)
		return pbexec.Result{Stdout: `{"type":"result","subtype":"success","is_error":false,"result":"OK","session_id":"cs-1"}`}
	}}
	s, repo := taskSvc(t, h)
	out, isErr := startTask(t, s, "claude", repo, "x", "", "")
	if isErr || !strings.Contains(out, "did not run tests") || !strings.Contains(out, "conversation_id: cs-1") {
		t.Errorf("claude task: %q", out)
	}
	if args := strings.Join(h.peerReqs()[0].Args, " "); !strings.Contains(args, "--restricted") || !strings.Contains(args, "acceptEdits") {
		t.Errorf("claude task args: %q", args)
	}
}

func TestTaskNoChanges(t *testing.T) {
	h := &hybridRunner{peer: func(req pbexec.Request) pbexec.Result {
		return pbexec.Result{Stdout: `{"type":"thread.started","thread_id":"n"}` + "\n" +
			`{"type":"item.completed","item":{"type":"agent_message","text":"nothing to do"}}` + "\n"}
	}}
	s, repo := taskSvc(t, h)
	out, isErr := startTask(t, s, "codex", repo, "nothing", "", "")
	if isErr || !strings.Contains(out, "made no changes") || strings.Contains(out, "patch: ") {
		t.Errorf("no-change task: %q", out)
	}
}

func TestTaskRecordSurvivesRestart(t *testing.T) {
	h := &hybridRunner{peer: editingPeer("thr-r")}
	s, repo := taskSvc(t, h)
	out, _ := startTask(t, s, "codex", repo, "x", "", "")
	wt := field(t, out, "worktree")
	// A new Service (server restart) reads the same state dir.
	s2 := NewService(s.Cfg, h, zerolog.Nop())
	out2, isErr := startTask(t, s2, "codex", repo, "continue", "thr-r", "")
	if isErr || field(t, out2, "worktree") != wt {
		t.Errorf("after restart the conversation must map to %s: %q", wt, out2)
	}
}

func TestTaskWorktreeLockedByAnotherProcess(t *testing.T) {
	h := &hybridRunner{peer: editingPeer("thr-l")}
	s, repo := taskSvc(t, h)
	out, _ := startTask(t, s, "codex", repo, "x", "", "")
	id := field(t, out, "task")
	// Another peerbridge process holds the worktree's lock (flock locks are
	// per open file, so a second open in this process conflicts the same way).
	other, err := lockFile(filepath.Join(s.Cfg.StateDir, "locks", "task-"+id+".lock"), false)
	if err != nil {
		t.Fatal(err)
	}
	if out, isErr := s.TaskStart(context.Background(), "codex", repo, "y", "thr-l", "", agent.Options{}); !isErr || !strings.Contains(out, "another peerbridge process") {
		t.Errorf("want cross-process lock refusal, got %q", out)
	}
	other()
	if out, isErr := startTask(t, s, "codex", repo, "y", "thr-l", ""); isErr {
		t.Errorf("after release the continuation must run: %q", out)
	}
}

func TestTaskNeutralisesFiltersOfTheWorktreeContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell-script filter driver")
	}
	h := &hybridRunner{peer: editingPeer("thr-flt")}
	s, repo := taskSvc(t, h)
	marker := filepath.Join(t.TempDir(), "FILTER_RAN")
	evil := filepath.Join(t.TempDir(), "evil.cfg")
	// A driver that only exists in the task branch's config context.
	script := filepath.Join(t.TempDir(), "evil.sh")
	_ = os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\nexec cat\n"), 0o700)
	_ = os.WriteFile(evil, []byte("[filter \"evil\"]\n\tclean = "+script+"\n\tsmudge = "+script+"\n"), 0o600)
	run(t, repo, "git", "config", "includeIf.onbranch:peerbridge/**.path", evil)
	_ = os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("* filter=evil\n"), 0o600)
	run(t, repo, "git", "add", ".gitattributes")
	run(t, repo, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "attrs")
	out, isErr := startTask(t, s, "codex", repo, "x", "", "")
	if isErr {
		t.Fatalf("task: %q", out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a filter driver from the worktree's config context ran on the host")
	}
}

func TestTaskIndexConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	// Two indexes on the same file stand in for two peerbridge processes.
	a, b := &taskIndex{path: filepath.Join(dir, "tasks.json")}, &taskIndex{path: filepath.Join(dir, "tasks.json")}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			x := a
			if i%2 == 1 {
				x = b
			}
			if err := x.put(taskRecord{ID: fmt.Sprintf("t%d", i)}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	n := 0
	_, _ = a.find(func(*taskRecord) bool { n++; return false })
	if n != 20 {
		t.Errorf("lost updates: %d of 20 records", n)
	}
}

func TestReviewOfTaskWorktree(t *testing.T) {
	h := &hybridRunner{peer: editingPeer("thr-rv")}
	s, repo := taskSvc(t, h)
	out, _ := startTask(t, s, "codex", repo, "x", "", "")
	wt := field(t, out, "worktree")
	if dir, err := s.resolveReadDir(wt); err != nil || dir != wt {
		t.Errorf("a verified task worktree must be reviewable: %q %v", dir, err)
	}
	if _, err := s.resolveReadDir(filepath.Join(s.Cfg.StateDir, "worktrees")); err == nil {
		t.Error("the worktrees dir itself is not a task worktree")
	}
	// A peer redirecting .git makes the worktree untrusted for reviews.
	_ = os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /tmp/elsewhere\n"), 0o600)
	if _, err := s.resolveReadDir(wt); err == nil || !strings.Contains(err.Error(), "no longer points") {
		t.Errorf("tampered worktree must be refused: %v", err)
	}
}

func TestWorktreeGitDirRelative(t *testing.T) {
	wt := t.TempDir()
	gd := filepath.Join(wt, "..", "admin")
	_ = os.MkdirAll(gd, 0o700)
	_ = os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: ../admin\n"), 0o600)
	got, err := worktreeGitDir(wt)
	want, _ := filepath.EvalSymlinks(filepath.Clean(gd))
	if err != nil || got != want {
		t.Errorf("relative gitdir = %q,%v want %q", got, err, want)
	}
}

func TestTaskPatchIgnoresRepoDiffConfig(t *testing.T) {
	h := &hybridRunner{peer: editingPeer("thr-cfg")}
	s, repo := taskSvc(t, h)
	for _, kv := range [][2]string{{"color.diff", "always"}, {"color.ui", "always"}, {"diff.noprefix", "true"}, {"diff.mnemonicPrefix", "true"}, {"diff.relative", "true"}, {"diff.context", "0"}} {
		run(t, repo, "git", "config", kv[0], kv[1])
	}
	out, isErr := startTask(t, s, "codex", repo, "x", "", "")
	if isErr {
		t.Fatalf("task: %q", out)
	}
	patch := field(t, out, "patch")
	b, _ := os.ReadFile(patch)
	if strings.Contains(string(b), "\x1b[") || !strings.Contains(string(b), "--- a/app.txt") {
		t.Errorf("patch not in plain a/ b/ format:\n%s", b)
	}
	run(t, repo, "git", "apply", "--check", "--index", patch)
}

func TestTaskDirtyCheckDoesNotRunRepoFilters(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell-script filter driver")
	}
	h := &hybridRunner{peer: editingPeer("thr-st")}
	s, repo := taskSvc(t, h)
	marker := filepath.Join(t.TempDir(), "CLEAN_RAN")
	script := filepath.Join(t.TempDir(), "clean.sh")
	_ = os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\nexec cat\n"), 0o700)
	_ = os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("*.txt filter=leak\n"), 0o600)
	run(t, repo, "git", "add", ".gitattributes")
	run(t, repo, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "attrs")
	run(t, repo, "git", "config", "filter.leak.clean", script)
	// Same size as "one\n", so status must compare contents (via the filter).
	_ = os.WriteFile(filepath.Join(repo, "app.txt"), []byte("two\n"), 0o600)
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(filepath.Join(repo, "app.txt"), future, future)
	out, _ := startTask(t, s, "codex", repo, "x", "", "")
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("a clean filter of your checkout ran on the host: %q", out)
	}
	if !strings.Contains(out, "uncommitted changes") {
		t.Errorf("dirty checkout must still be reported: %q", out)
	}
}

func TestTaskConversationLockedByAnotherProcess(t *testing.T) {
	h := &hybridRunner{peer: editingPeer("chat-x")}
	s, repo := taskSvc(t, h)
	other, err := s.lockConversation("codex", "chat-x")
	if err != nil {
		t.Fatal(err)
	}
	out, isErr := s.TaskStart(context.Background(), "codex", repo, "y", "chat-x", "", agent.Options{})
	if !isErr || !strings.Contains(out, "another peerbridge process") {
		t.Errorf("want cross-process conversation refusal, got %q", out)
	}
	other()
	if out, isErr := startTask(t, s, "codex", repo, "y", "chat-x", ""); isErr {
		t.Errorf("after release the task must run: %q", out)
	}
	// The lock is released when the turn ends.
	u, err := s.lockConversation("codex", "chat-x")
	if err != nil {
		t.Errorf("conversation lock still held after the task finished: %v", err)
	} else {
		u()
	}
}

func TestTaskRequiresRepoRootInAllowedRoots(t *testing.T) {
	h := &hybridRunner{peer: editingPeer("thr-sub")}
	s, repo := taskSvc(t, h)
	sub := filepath.Join(repo, "pkg")
	_ = os.Mkdir(sub, 0o700)
	s.Cfg.AllowedRoots = []string{sub}
	out, isErr := s.TaskStart(context.Background(), "codex", sub, "x", "", "", agent.Options{})
	if !isErr || !strings.Contains(out, "repository root") {
		t.Errorf("a repo whose root is not allowed must be refused: %q", out)
	}
}

func TestTaskIncompletePatchIsNotOfferedForApply(t *testing.T) {
	h := &hybridRunner{}
	s, repo := taskSvc(t, h)
	h.peer = func(req pbexec.Request) pbexec.Result {
		_ = os.WriteFile(filepath.Join(req.Dir, "app.txt"), []byte("changed\n"), 0o600)
		_ = os.WriteFile(filepath.Join(req.Dir, "untracked.txt"), []byte("u\n"), 0o600)
		// Make staging fail (stand-in for any git add failure).
		if gd, err := worktreeGitDir(req.Dir); err == nil {
			_ = os.WriteFile(filepath.Join(gd, "index.lock"), nil, 0o600)
		}
		return editingPeerReply("thr-inc")
	}
	out, _ := startTask(t, s, "codex", repo, "x", "", "")
	if !strings.Contains(out, "INCOMPLETE") || strings.Contains(out, "apply --index") || strings.Contains(out, "worktree remove") {
		t.Errorf("incomplete patch must not be offered for apply/cleanup: %q", out)
	}
	if !strings.Contains(out, "Do not remove the worktree") {
		t.Errorf("must point to worktree recovery: %q", out)
	}
}

func editingPeerReply(thread string) pbexec.Result {
	return pbexec.Result{Stdout: `{"type":"thread.started","thread_id":"` + thread + `"}` + "\n" +
		`{"type":"item.completed","item":{"type":"agent_message","text":"DONE"}}` + "\n"}
}

func TestChatContinuationTakesConversationLock(t *testing.T) {
	h := &hybridRunner{peer: editingPeer("conv-z")}
	s, repo := taskSvc(t, h)
	other, err := s.lockConversation("codex", "conv-z")
	if err != nil {
		t.Fatal(err)
	}
	if out, isErr := s.ChatStart(context.Background(), "codex", repo, "hi", "conv-z", agent.Options{}); !isErr || !strings.Contains(out, "another peerbridge process") {
		t.Errorf("chat must respect the cross-process conversation lock: %q", out)
	}
	other()
	out, isErr := s.ChatStart(context.Background(), "codex", repo, "hi", "conv-z", agent.Options{})
	if isErr {
		t.Fatalf("after release: %q", out)
	}
	waitStatus(t, s, jobID(t, out))
	if u, err := s.lockConversation("codex", "conv-z"); err != nil {
		t.Errorf("chat must release the lock when done: %v", err)
	} else {
		u()
	}
}

func TestTaskPatchIsCumulativeFromBase(t *testing.T) {
	h := &hybridRunner{peer: editingPeer("thr-cum")}
	s, repo := taskSvc(t, h)
	out, _ := startTask(t, s, "codex", repo, "x", "", "")
	wt := field(t, out, "worktree")
	// The user commits the first result on the task branch, then continues.
	run(t, wt, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "partial")
	h.peer = func(req pbexec.Request) pbexec.Result {
		_ = os.WriteFile(filepath.Join(req.Dir, "later.txt"), []byte("later\n"), 0o600)
		return editingPeerReply("thr-cum")
	}
	out, isErr := startTask(t, s, "codex", repo, "more", "thr-cum", "")
	if isErr {
		t.Fatalf("continue: %q", out)
	}
	b, _ := os.ReadFile(field(t, out, "patch"))
	for _, f := range []string{"new.txt", "later.txt", "app.txt"} {
		if !strings.Contains(string(b), f) {
			t.Errorf("cumulative patch is missing %s:\n%s", f, b)
		}
	}
	run(t, repo, "git", "apply", "--check", "--index", field(t, out, "patch"))
}

func TestImmediateContinuationAfterCompletion(t *testing.T) {
	h := &hybridRunner{peer: editingPeer("thr-now")}
	s, repo := taskSvc(t, h)
	for i := 0; i < 30; i++ {
		out, isErr := s.ChatStart(context.Background(), "codex", repo, "hi", "thr-now", agent.Options{})
		if isErr {
			t.Fatalf("turn %d refused right after the previous one completed: %q", i, out)
		}
		waitStatus(t, s, jobID(t, out))
	}
	for i := 0; i < 10; i++ {
		out, isErr := s.TaskStart(context.Background(), "codex", repo, "x", "thr-now", "", agent.Options{})
		if isErr {
			t.Fatalf("task turn %d refused right after the previous one completed: %q", i, out)
		}
		waitStatus(t, s, jobID(t, out))
	}
}

func TestTaskDiffstatIsCapped(t *testing.T) {
	h := &hybridRunner{peer: func(req pbexec.Request) pbexec.Result {
		for i := 0; i < 300; i++ {
			_ = os.WriteFile(filepath.Join(req.Dir, fmt.Sprintf("file-with-a-long-name-%04d.txt", i)), []byte("x\n"), 0o600)
		}
		return editingPeerReply("thr-many")
	}}
	s, repo := taskSvc(t, h)
	out, isErr := startTask(t, s, "codex", repo, "x", "", "")
	if isErr {
		t.Fatalf("task: %q", out)
	}
	section := strings.SplitN(strings.SplitN(out, "--- changes ---", 2)[1], "--- diff ---", 2)[0]
	if len(section) > s.Cfg.MaxOutputBytes+100 || !strings.Contains(section, "[output truncated]") {
		t.Errorf("diffstat must be capped at max_output_bytes (%d): got %d bytes", s.Cfg.MaxOutputBytes, len(section))
	}
	b, _ := os.ReadFile(field(t, out, "patch"))
	if !strings.Contains(string(b), "file-with-a-long-name-0299.txt") {
		t.Error("the saved patch must stay complete")
	}
}

func TestTaskPatchIncludesSubmoduleChanges(t *testing.T) {
	h := &hybridRunner{peer: func(req pbexec.Request) pbexec.Result {
		// An embedded repository becomes a gitlink when staged.
		sub := filepath.Join(req.Dir, "vendored")
		_ = os.MkdirAll(sub, 0o700)
		cmd := exec.Command("sh", "-c", "git init -q && echo v > v && git add v && git -c user.name=t -c user.email=t@t commit -qm v")
		cmd.Dir = sub
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		_ = cmd.Run()
		return editingPeerReply("thr-sm")
	}}
	s, repo := taskSvc(t, h)
	run(t, repo, "git", "config", "diff.ignoreSubmodules", "all")
	run(t, repo, "git", "config", "diff.submodule", "log")
	out, isErr := startTask(t, s, "codex", repo, "x", "", "")
	if isErr {
		t.Fatalf("task: %q", out)
	}
	b, _ := os.ReadFile(field(t, out, "patch"))
	if !strings.Contains(string(b), "Subproject commit") {
		t.Errorf("gitlink change missing from the patch despite diff.ignoreSubmodules=all:\n%s\n%s", b, out)
	}
	// The nested repo's contents are not in the patch: never offer cleanup.
	if !strings.Contains(out, "nested repositories") || strings.Contains(out, "worktree remove") || strings.Contains(out, "apply --index") {
		t.Errorf("nested-repo work must be flagged incomplete, without apply/cleanup: %q", out)
	}
}

func TestTaskPatchPerTurnNotOverwritten(t *testing.T) {
	h := &hybridRunner{peer: editingPeer("thr-pp")}
	s, repo := taskSvc(t, h)
	first, _ := startTask(t, s, "codex", repo, "x", "", "")
	p1 := field(t, first, "patch")
	b1, _ := os.ReadFile(p1)
	h.peer = func(req pbexec.Request) pbexec.Result {
		_ = os.WriteFile(filepath.Join(req.Dir, "turn2.txt"), []byte("2\n"), 0o600)
		return editingPeerReply("thr-pp")
	}
	second, _ := startTask(t, s, "codex", repo, "y", "thr-pp", "")
	p2 := field(t, second, "patch")
	if p1 == p2 {
		t.Fatalf("two turns share a patch file: %s", p1)
	}
	if b, _ := os.ReadFile(p1); string(b) != string(b1) {
		t.Error("the first turn's patch was overwritten")
	}
}

// gitIn runs git with an isolated config in dir.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	run(t, dir, append([]string{"git", "-c", "user.name=t", "-c", "user.email=t@t", "-c", "protocol.file.allow=always"}, args...)...)
}

func TestTaskDirtySubmoduleIsNotDeclaredComplete(t *testing.T) {
	h := &hybridRunner{}
	s, repo := taskSvc(t, h)
	src := t.TempDir()
	gitIn(t, src, "init", "-q")
	_ = os.WriteFile(filepath.Join(src, "lib.txt"), []byte("lib\n"), 0o600)
	gitIn(t, src, "add", "-A")
	gitIn(t, src, "commit", "-qm", "lib")
	gitIn(t, repo, "submodule", "add", "-q", src, "lib")
	gitIn(t, repo, "commit", "-qm", "add submodule")
	h.peer = func(req pbexec.Request) pbexec.Result {
		// Work inside the (uninitialised) submodule directory, gitlink unchanged.
		_ = os.WriteFile(filepath.Join(req.Dir, "lib", "edited.txt"), []byte("edit\n"), 0o600)
		return editingPeerReply("thr-dsm")
	}
	out, _ := startTask(t, s, "codex", repo, "x", "", "")
	if !strings.Contains(out, "nested repositories") || strings.Contains(out, "worktree remove") {
		t.Errorf("work inside a submodule must block cleanup: %q", out)
	}
}

func TestTaskUntouchedSubmoduleStaysComplete(t *testing.T) {
	h := &hybridRunner{peer: editingPeer("thr-usm")}
	s, repo := taskSvc(t, h)
	src := t.TempDir()
	gitIn(t, src, "init", "-q")
	_ = os.WriteFile(filepath.Join(src, "lib.txt"), []byte("lib\n"), 0o600)
	gitIn(t, src, "add", "-A")
	gitIn(t, src, "commit", "-qm", "lib")
	gitIn(t, repo, "submodule", "add", "-q", src, "lib")
	gitIn(t, repo, "commit", "-qm", "add submodule")
	out, isErr := startTask(t, s, "codex", repo, "x", "", "")
	if isErr || strings.Contains(out, "INCOMPLETE") || !strings.Contains(out, "apply --index") {
		t.Errorf("an untouched, uninitialised submodule must not block apply: %q", out)
	}
}

func TestTaskTextMentioningSubprojectIsComplete(t *testing.T) {
	h := &hybridRunner{peer: func(req pbexec.Request) pbexec.Result {
		_ = os.WriteFile(filepath.Join(req.Dir, "fixture.txt"), []byte("Subproject commit 0123456789abcdef0123456789abcdef01234567\n"), 0o600)
		return editingPeerReply("thr-txt")
	}}
	s, repo := taskSvc(t, h)
	out, isErr := startTask(t, s, "codex", repo, "x", "", "")
	if isErr || strings.Contains(out, "INCOMPLETE") || !strings.Contains(out, "apply --index") {
		t.Errorf("ordinary text must not be mistaken for a gitlink: %q", out)
	}
}

func TestReviewOfTaskWorktreeNeverRunsGitInNestedRepo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell-script filter driver")
	}
	marker := filepath.Join(t.TempDir(), "NESTED_FILTER_RAN")
	h := &hybridRunner{}
	s, repo := taskSvc(t, h)
	h.peer = func(req pbexec.Request) pbexec.Result {
		if !strings.Contains(strings.Join(req.Args, " "), "workspace-write") {
			// The review turn: a claude-style JSON reply.
			return pbexec.Result{Stdout: `{"type":"result","subtype":"success","is_error":false,"result":"reviewed","session_id":"rv"}`}
		}
		// The task turn plants a nested repo whose own config runs a filter.
		n := filepath.Join(req.Dir, "evil")
		_ = os.MkdirAll(n, 0o700)
		script := filepath.Join(req.Dir, "evil-filter.sh")
		_ = os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\nexec cat\n"), 0o700)
		cmd := exec.Command("sh", "-c", "git init -q && echo a > a.txt && echo '* filter=x' > .gitattributes && git add -A && git -c user.name=t -c user.email=t@t commit -qm n && git config filter.x.clean "+script+" && echo b > a.txt")
		cmd.Dir = n
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		_ = cmd.Run()
		return editingPeerReply("thr-nest")
	}
	out, _ := startTask(t, s, "codex", repo, "plant", "", "")
	wt := field(t, out, "worktree")
	_ = os.Remove(marker) // only the review below matters
	rv, isErr := s.ReviewStart(context.Background(), "claude", wt, "uncommitted", "", "", "", agent.Options{})
	if isErr {
		t.Fatalf("review start: %q", rv)
	}
	got, _ := waitStatus(t, s, jobID(t, rv))
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("reviewing a task worktree ran a filter from a peer-made nested repo: %q", got)
	}
}

func TestTaskWorktreeIntegrityEvenWhenAllowlisted(t *testing.T) {
	h := &hybridRunner{peer: editingPeer("thr-al")}
	s, repo := taskSvc(t, h)
	out, _ := startTask(t, s, "codex", repo, "x", "", "")
	wt := field(t, out, "worktree")
	// allowed_roots now also covers the state dir (like allowing $HOME).
	s.Cfg.AllowedRoots = append(s.Cfg.AllowedRoots, s.Cfg.StateDir)
	if _, err := s.resolveReadDir(wt); err != nil {
		t.Errorf("intact worktree must still be reviewable: %v", err)
	}
	_ = os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /tmp/elsewhere\n"), 0o600)
	if _, err := s.resolveReadDir(wt); err == nil {
		t.Error("a tampered worktree must be refused even when allowed_roots covers it")
	}
	if out, isErr := s.TaskStart(context.Background(), "codex", wt, "x", "", "", agent.Options{}); !isErr || !strings.Contains(out, "task worktree") {
		t.Errorf("task_start on a task worktree must be refused: %q", out)
	}
}

func TestTaskReviewOnlyAtWorktreeRoot(t *testing.T) {
	h := &hybridRunner{peer: func(req pbexec.Request) pbexec.Result {
		_ = os.MkdirAll(filepath.Join(req.Dir, "nested"), 0o700)
		_ = os.WriteFile(filepath.Join(req.Dir, "nested", "f.txt"), []byte("f\n"), 0o600)
		return editingPeerReply("thr-root")
	}}
	s, repo := taskSvc(t, h)
	out, _ := startTask(t, s, "codex", repo, "x", "", "")
	wt := field(t, out, "worktree")
	if _, err := s.resolveReadDir(filepath.Join(wt, "nested")); err == nil || !strings.Contains(err.Error(), "root") {
		t.Errorf("a directory inside a task worktree must be refused: %v", err)
	}
}

// gitSpy records requests and forwards them.
type gitSpy struct {
	inner agent.Runner
	mu    sync.Mutex
	reqs  []pbexec.Request
}

func (g *gitSpy) Run(ctx context.Context, req pbexec.Request) (pbexec.Result, error) {
	g.mu.Lock()
	g.reqs = append(g.reqs, req)
	g.mu.Unlock()
	return g.inner.Run(ctx, req)
}

func TestTaskReviewUsesRecordedGitDir(t *testing.T) {
	h := &hybridRunner{peer: editingPeer("thr-gd")}
	s, repo := taskSvc(t, h)
	out, _ := startTask(t, s, "codex", repo, "x", "", "")
	wt := field(t, out, "worktree")
	rec, _ := s.Tasks.byWorktree(wt)
	spy := &gitSpy{inner: h}
	s.Runner = spy
	if _, _, err := s.changeContext(context.Background(), wt, "uncommitted", "", ""); err != nil {
		t.Fatalf("changeContext: %v", err)
	}
	if len(spy.reqs) == 0 {
		t.Fatal("no git calls recorded")
	}
	for _, q := range spy.reqs {
		if q.Command == "git" && !strings.Contains(strings.Join(q.Args, " "), "--git-dir="+rec.GitDir) {
			t.Errorf("review git call without the recorded git dir: %v", q.Args)
		}
	}
}

func TestTaskReportsIgnoredFiles(t *testing.T) {
	h := &hybridRunner{peer: func(req pbexec.Request) pbexec.Result {
		_ = os.WriteFile(filepath.Join(req.Dir, ".env.example"), []byte("KEY=\n"), 0o600)
		return editingPeerReply("thr-ign")
	}}
	s, repo := taskSvc(t, h)
	_ = os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".env*\n"), 0o600)
	gitIn(t, repo, "add", ".gitignore")
	gitIn(t, repo, "commit", "-qm", "ignore")
	out, _ := startTask(t, s, "codex", repo, "x", "", "")
	if !strings.Contains(out, ".env.example") || !strings.Contains(out, "NOT in the patch") || !strings.Contains(out, "Before cleaning up") {
		t.Errorf("ignored files must be reported before cleanup: %q", out)
	}
}

func TestShellCommandsPerPlatform(t *testing.T) {
	if got := quoteFor("darwin", "/Users/me/it's here"); got != `'/Users/me/it'\''s here'` {
		t.Errorf("posix quote = %s", got)
	}
	if got := quoteFor("windows", `C:\Users\me\it's here`); got != `'C:\Users\me\it''s here'` {
		t.Errorf("powershell quote = %s", got)
	}
	if got := quoteFor("windows", `C:\Users\me\repo`); got != `C:\Users\me\repo` {
		t.Errorf("plain windows path should stay unquoted: %s", got)
	}
	if got := seqFor("linux", "a", "b"); got != "a && b" {
		t.Errorf("posix seq = %s", got)
	}
	if got := seqFor("windows", "a", "b", "c"); got != "a; if ($LASTEXITCODE -eq 0) { b }; if ($LASTEXITCODE -eq 0) { c }" {
		t.Errorf("powershell seq = %s", got)
	}
}

func TestTaskDirtyCheckSkipsSubmoduleInternals(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell-script filter driver")
	}
	h := &hybridRunner{peer: editingPeer("thr-subf")}
	s, repo := taskSvc(t, h)
	src := t.TempDir()
	gitIn(t, src, "init", "-q")
	_ = os.WriteFile(filepath.Join(src, "a.txt"), []byte("one\n"), 0o600)
	_ = os.WriteFile(filepath.Join(src, ".gitattributes"), []byte("*.txt filter=subf\n"), 0o600)
	gitIn(t, src, "add", "-A")
	gitIn(t, src, "commit", "-qm", "s")
	gitIn(t, repo, "submodule", "add", "-q", src, "sub")
	gitIn(t, repo, "commit", "-qm", "sub")
	marker := filepath.Join(t.TempDir(), "SUB_FILTER_RAN")
	script := filepath.Join(t.TempDir(), "f.sh")
	_ = os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\nexec cat\n"), 0o700)
	gitIn(t, filepath.Join(repo, "sub"), "config", "filter.subf.clean", script)
	_ = os.WriteFile(filepath.Join(repo, "sub", "a.txt"), []byte("two\n"), 0o600) // same size, dirty
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(filepath.Join(repo, "sub", "a.txt"), future, future)
	_ = os.Remove(marker)
	out, _ := startTask(t, s, "codex", repo, "x", "", "")
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("the dirty check ran a submodule's filter: %q", out)
	}
}

// capRunner reports every git ls-files / diff --raw as truncated.
type capRunner struct{ inner agent.Runner }

func (c capRunner) Run(ctx context.Context, req pbexec.Request) (pbexec.Result, error) {
	res, err := c.inner.Run(ctx, req)
	j := strings.Join(req.Args, " ")
	if req.Command == "git" && (strings.Contains(j, " ls-files -s -z") || strings.Contains(j, " --raw ")) {
		res.StdoutTruncated = true
	}
	return res, err
}

func TestTaskTruncatedMetadataFailsClosed(t *testing.T) {
	h := &hybridRunner{peer: editingPeer("thr-cap")}
	s, repo := taskSvc(t, h)
	s.Runner = capRunner{inner: h}
	out, _ := startTask(t, s, "codex", repo, "x", "", "")
	if !strings.Contains(out, "INCOMPLETE: could not check for nested repositories") || strings.Contains(out, "worktree remove") {
		t.Errorf("truncated metadata must not be trusted: %q", out)
	}
}
