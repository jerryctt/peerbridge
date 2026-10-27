package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jerryctt/hostrunner-mcp/internal/agent"
	"github.com/jerryctt/hostrunner-mcp/internal/claude"
	"github.com/jerryctt/hostrunner-mcp/internal/codex"
	"github.com/jerryctt/hostrunner-mcp/internal/config"
	"github.com/jerryctt/hostrunner-mcp/internal/exec"
	"github.com/rs/zerolog"
)

type fakeRunner struct{ reply map[string]exec.Result }

func (f *fakeRunner) Run(_ context.Context, req exec.Request) (exec.Result, error) {
	key := req.Command + " " + strings.Join(req.Args, " ")
	return f.reply[key], nil
}

// recordingRunner returns one canned result and remembers every request.
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

func (r *recordingRunner) all() []exec.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]exec.Request(nil), r.reqs...)
}

// scriptedRunner answers by "command args" key, with a fallback result for
// anything else, and records every request.
type scriptedRunner struct {
	recordingRunner
	replies  map[string]exec.Result
	fallback exec.Result
}

func (r *scriptedRunner) Run(_ context.Context, req exec.Request) (exec.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
	if res, ok := r.replies[req.Command+" "+strings.Join(req.Args, " ")]; ok {
		return res, nil
	}
	return r.fallback, nil
}

// blockingRunner blocks until its context is cancelled, like a CLI process
// that only dies when its process group is killed.
type blockingRunner struct{ started chan struct{} }

func (b *blockingRunner) Run(ctx context.Context, _ exec.Request) (exec.Result, error) {
	close(b.started)
	<-ctx.Done()
	return exec.Result{TimedOut: true, ExitCode: -1}, nil
}

func newCfg(t *testing.T) (*config.Config, string) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	return &config.Config{
		AllowedRoots:    []string{root},
		AllowedCommands: []string{"codex", "claude", "git"},
		MaxOutputBytes:  1000,
	}, repo
}

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

func mustResolve(t *testing.T, cfg *config.Config, dir string) string {
	t.Helper()
	resolved, err := cfg.ResolveAllowedDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// gitKey builds a scriptedRunner key for a git invocation, including the
// safety prefix the server always prepends.
func gitKey(args string) string {
	return "git " + strings.Join(gitSafety, " ") + " " + args
}

// gitCalls returns the git subcommands a runner saw, without the safety
// prefix, and checks every call carried the isolation env.
func gitCalls(t *testing.T, reqs []exec.Request) []string {
	t.Helper()
	var calls []string
	for _, q := range reqs {
		if q.Command != "git" {
			continue
		}
		if len(q.Args) < len(gitSafety) || strings.Join(q.Args[:len(gitSafety)], " ") != strings.Join(gitSafety, " ") {
			t.Errorf("git call missing safety prefix: %v", q.Args)
			continue
		}
		if strings.Join(q.Env, " ") != strings.Join(gitEnv, " ") {
			t.Errorf("git call missing isolation env: %v", q.Env)
		}
		calls = append(calls, strings.Join(q.Args[len(gitSafety):], " "))
	}
	return calls
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
	// Result stays retrievable after completion.
	again, isErr := s.JobStatus(context.Background(), jobID(t, out), time.Millisecond)
	if isErr || !strings.Contains(again, "VERDICT_OK") {
		t.Errorf("re-fetch = %q isErr=%v, want cached result", again, isErr)
	}
}

func TestReviewStartCodexFailureIncludesStderr(t *testing.T) {
	s, repo := newSvc(t, &fakeRunner{reply: map[string]exec.Result{
		"codex review --uncommitted": {Stdout: "partial", Stderr: "BOOM", ExitCode: 2},
	}})
	out, _ := s.ReviewStart(context.Background(), "codex", repo, "uncommitted", "", "", "")
	got, isErr := waitStatus(t, s, jobID(t, out))
	if isErr {
		t.Fatalf("expected non-error tool result, got error: %q", got)
	}
	if !strings.Contains(got, "BOOM") || !strings.Contains(got, "codex exit 2") {
		t.Errorf("want stderr and exit code, got %q", got)
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
	promptArg := req.Args[len(req.Args)-1]
	if req.Command != "claude" || req.Args[0] != "-p" || req.Args[len(req.Args)-2] != "--" {
		t.Errorf("claude invocation = %v", req)
	}
	if !strings.Contains(promptArg, "base branch main") || !strings.Contains(promptArg, "check auth") {
		t.Errorf("review prompt = %q", promptArg)
	}
	// The diff rides on stdin (argv has a per-argument size limit), not in
	// the prompt argument.
	if !strings.HasPrefix(req.Stdin, "--- diff ---\n") || strings.Contains(promptArg, "$ git") {
		t.Errorf("diff must be passed via stdin: stdin=%q arg=%q", req.Stdin, promptArg)
	}
	if req.MaxOutputBytes != 0 {
		t.Errorf("structured peer output must not be pre-truncated, got cap %d", req.MaxOutputBytes)
	}
	// hostrunner computed the diff itself with read-only git, since the
	// claude peer has no shell; "--" ends option parsing after the ref.
	gitCalls := gitCalls(t, r.all())
	if strings.Join(gitCalls, ";") != "diff --no-ext-diff --no-textconv main...HEAD --" {
		t.Errorf("git calls = %v", gitCalls)
	}
}

func TestChatStartRejectsOptionLikeSessionID(t *testing.T) {
	s, repo := newSvc(t, &recordingRunner{reply: exec.Result{Stdout: codexJSONL}})
	for _, bad := range []string{"--last", "-p", "a b"} {
		out, isErr := s.ChatStart(context.Background(), "codex", repo, "hi", bad)
		if !isErr || !strings.Contains(out, "invalid session_id") {
			t.Errorf("%q: want rejection, got %q isErr=%v", bad, out, isErr)
		}
	}
}

func TestReviewStartRejectsOptionLikeRefs(t *testing.T) {
	s, repo := newSvc(t, &recordingRunner{reply: exec.Result{Stdout: claudeJSON}})
	for _, c := range []struct{ scope, base, commit string }{
		{"commit", "", "--output=/tmp/pwned"},
		{"base", "-p", ""},
		{"base", "main --output=x", ""},
	} {
		for _, a := range []string{"claude", "codex"} {
			out, isErr := s.ReviewStart(context.Background(), a, repo, c.scope, c.base, c.commit, "")
			if !isErr || !strings.Contains(out, "invalid") {
				t.Errorf("%s %+v: want rejection, got %q isErr=%v", a, c, out, isErr)
			}
		}
	}
}

func TestReviewStartClaudeUncommittedGitAndFailure(t *testing.T) {
	r := &scriptedRunner{replies: map[string]exec.Result{
		gitKey("status --short --untracked-files=all"):     {Stdout: "?? new.go\n"},
		gitKey("diff --no-ext-diff --no-textconv HEAD --"): {Stdout: "+++ b/new.go\n"},
	}, fallback: exec.Result{Stdout: claudeJSON}}
	s, repo := newSvc(t, r)
	out, _ := s.ReviewStart(context.Background(), "claude", repo, "uncommitted", "", "", "")
	got, isErr := waitStatus(t, s, jobID(t, out))
	if isErr {
		t.Fatalf("status: %q", got)
	}
	in := r.last().Stdin
	if !strings.Contains(in, "?? new.go") || !strings.Contains(in, "+++ b/new.go") {
		t.Errorf("stdin should carry status and diff: %q", in)
	}
	gitCalls := gitCalls(t, r.all())
	if want := "rev-parse --verify --quiet HEAD;status --short --untracked-files=all;diff --no-ext-diff --no-textconv HEAD --"; strings.Join(gitCalls, ";") != want {
		t.Errorf("git calls = %v", gitCalls)
	}

	// Unborn HEAD (no commits yet): diff against the empty tree instead.
	r3 := &scriptedRunner{replies: map[string]exec.Result{
		gitKey("rev-parse --verify --quiet HEAD"):                       {ExitCode: 1},
		gitKey("diff --no-ext-diff --no-textconv " + emptyTree + " --"): {Stdout: "+++ b/first.go\n"},
	}, fallback: exec.Result{Stdout: claudeJSON}}
	s3, repo3 := newSvc(t, r3)
	out, _ = s3.ReviewStart(context.Background(), "claude", repo3, "", "", "", "")
	got, isErr = waitStatus(t, s3, jobID(t, out))
	if isErr || !strings.Contains(r3.last().Stdin, "+++ b/first.go") {
		t.Errorf("unborn HEAD should fall back to the empty tree: %q isErr=%v stdin=%q", got, isErr, r3.last().Stdin)
	}

	// A failing git command aborts the job before the peer runs.
	r2 := &scriptedRunner{replies: map[string]exec.Result{
		gitKey("diff --no-ext-diff --no-textconv main...HEAD --"): {ExitCode: 128, Stderr: "fatal: bad revision"},
	}, fallback: exec.Result{Stdout: claudeJSON}}
	s2, repo2 := newSvc(t, r2)
	out, _ = s2.ReviewStart(context.Background(), "claude", repo2, "base", "main", "", "")
	got, isErr = waitStatus(t, s2, jobID(t, out))
	if !isErr || !strings.Contains(got, "bad revision") {
		t.Errorf("want git failure surfaced, got %q isErr=%v", got, isErr)
	}
	for _, q := range r2.all() {
		if q.Command == "claude" {
			t.Error("claude must not run when the diff cannot be computed")
		}
	}
}

func TestChatReplyIsCappedAfterParsing(t *testing.T) {
	big := strings.Repeat("x", 5000)
	r := &recordingRunner{reply: exec.Result{Stdout: `{"type":"result","is_error":false,"result":"` + big + `","session_id":"s1"}`}}
	s, repo := newSvc(t, r) // MaxOutputBytes is 1000 in newCfg
	out, _ := s.ChatStart(context.Background(), "claude", repo, "hi", "")
	got, isErr := waitStatus(t, s, jobID(t, out))
	if isErr {
		t.Fatalf("status: %q", got)
	}
	if !strings.Contains(got, "session_id: s1") || !strings.Contains(got, "[output truncated]") || strings.Count(got, "x") > 1100 {
		t.Errorf("reply should be parsed then capped: len=%d", len(got))
	}
	if r.last().MaxOutputBytes != 0 {
		t.Errorf("raw JSON must not be pre-truncated, got cap %d", r.last().MaxOutputBytes)
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
	// Make repo look like a git worktree with subdirectories.
	for _, d := range []string{".git", "internal", "cmd"} {
		if err := os.Mkdir(filepath.Join(repo, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// A job started from repo/internal that never finishes.
	j, err := s.Jobs.add(mustResolve(t, s.Cfg, filepath.Join(repo, "internal")), "codex", agent.KindReview, "uncommitted")
	if err != nil {
		t.Fatal(err)
	}
	// Same folder, the repo root, and a sibling subdirectory are all the same
	// worktree and must be rejected while the job is running.
	for _, dir := range []string{filepath.Join(repo, "internal"), repo, filepath.Join(repo, "cmd")} {
		out, isErr := s.ReviewStart(context.Background(), "codex", dir, "uncommitted", "", "", "")
		if !isErr || !strings.Contains(out, "already running") || !strings.Contains(out, j.ID) {
			t.Errorf("dir %s: expected duplicate-job rejection mentioning %s, got %q isErr=%v", dir, j.ID, out, isErr)
		}
	}
	// Once finished, a new review for the same worktree is allowed.
	j.finish("done", false)
	if out, isErr := s.ReviewStart(context.Background(), "codex", repo, "uncommitted", "", "", ""); isErr {
		t.Errorf("expected new job after previous finished, got %q", out)
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
