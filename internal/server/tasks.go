package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/jerryctt/peerbridge/internal/agent"
	"github.com/jerryctt/peerbridge/internal/exec"
)

// A task lets a peer edit files, but only inside a git worktree peerbridge
// creates for it under the state dir (never the user's checkout). The peer
// cannot commit (codex's sandbox denies the main repo's .git; claude's file
// tools are confined to the worktree), and peerbridge never commits or
// merges: it stages the result in the worktree, saves a complete binary
// patch, and hands both back for the caller to review and apply.

// taskRecord is the persistent record of one task worktree. Host-side git in
// a worktree only ever uses the recorded GitDir/Worktree, never the
// worktree's own .git file, which the peer could have rewritten.
type taskRecord struct {
	ID             string    `json:"id"`
	Agent          string    `json:"agent"`
	Repo           string    `json:"repo"`     // canonical top level of the source checkout
	Worktree       string    `json:"worktree"` // server-created, under state_dir/worktrees
	GitDir         string    `json:"git_dir"`  // the worktree's admin dir, recorded at creation
	Branch         string    `json:"branch"`
	Base           string    `json:"base"` // commit the worktree started from
	ConversationID string    `json:"conversation_id,omitempty"`
	LastPatch      string    `json:"last_patch,omitempty"`
	Created        time.Time `json:"created"`
	Updated        time.Time `json:"updated"`
}

// errLocked reports a lock held by another peerbridge process.
var errLocked = errors.New("locked by another peerbridge process")

// taskIndex persists task records as JSON in state_dir/tasks.json. Several
// peerbridge processes (e.g. Claude Desktop and Codex) may share it, so every
// access holds an interprocess lock on tasks.json.lock as well as mu.
type taskIndex struct {
	mu   sync.Mutex
	path string
}

// locked runs fn holding both the in-process and the interprocess lock.
func (x *taskIndex) locked(fn func() error) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(x.path), 0o700); err != nil {
		return err
	}
	unlock, err := lockFile(x.path+".lock", true)
	if err != nil {
		return fmt.Errorf("lock task index: %w", err)
	}
	defer unlock()
	return fn()
}

func (x *taskIndex) loadLocked() (map[string]*taskRecord, error) {
	m := map[string]*taskRecord{}
	raw, err := os.ReadFile(x.path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read task index: %w", err)
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse task index %s: %w", x.path, err)
	}
	return m, nil
}

// put inserts or replaces r: read-modify-write under the interprocess lock,
// written to a unique temp file and renamed into place.
func (x *taskIndex) put(r taskRecord) error {
	return x.locked(func() error {
		m, err := x.loadLocked()
		if err != nil {
			return err
		}
		r.Updated = time.Now()
		m[r.ID] = &r
		raw, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return err
		}
		f, err := os.CreateTemp(filepath.Dir(x.path), "tasks-*.json.tmp")
		if err != nil {
			return err
		}
		tmp := f.Name()
		_, werr := f.Write(raw)
		cerr := f.Close()
		if werr != nil || cerr != nil {
			os.Remove(tmp)
			return errors.Join(werr, cerr)
		}
		if err := os.Rename(tmp, x.path); err != nil {
			os.Remove(tmp)
			return err
		}
		return nil
	})
}

// find returns the most recently updated record matching keep, or nil.
func (x *taskIndex) find(keep func(*taskRecord) bool) (*taskRecord, error) {
	var found *taskRecord
	err := x.locked(func() error {
		m, err := x.loadLocked()
		if err != nil {
			return err
		}
		for _, r := range m {
			if keep(r) && (found == nil || r.Updated.After(found.Updated)) {
				found = r
			}
		}
		return nil
	})
	return found, err
}

// byConversation returns the task of agent whose conversation is cid, or nil.
func (x *taskIndex) byConversation(agentName, cid string) (*taskRecord, error) {
	return x.find(func(r *taskRecord) bool { return r.Agent == agentName && r.ConversationID == cid })
}

// byWorktree returns the task whose worktree is wt, or nil.
func (x *taskIndex) byWorktree(wt string) (*taskRecord, error) {
	return x.find(func(r *taskRecord) bool { return r.Worktree == wt })
}

// lockWorktree takes the interprocess writer lock of task id without
// waiting: one turn at a time per worktree, across all peerbridge processes.
// Held for the whole turn, including change collection.
func (s *Service) lockWorktree(id string) (func(), error) {
	dir := s.stateDir("locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return lockFile(filepath.Join(dir, "task-"+id+".lock"), false)
}

// lockConversation takes the interprocess lock of (agent, conversation)
// without waiting.
func (s *Service) lockConversation(agentName, cid string) (func(), error) {
	dir := s.stateDir("locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	h := sha256.Sum256([]byte(agentName + "\x00" + cid))
	return lockFile(filepath.Join(dir, "conv-"+hex.EncodeToString(h[:8])+".lock"), false)
}

// worktreeGitDir reads a worktree's .git pointer, resolving a relative path
// (worktree.useRelativePaths) against the worktree itself.
func worktreeGitDir(wt string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(wt, ".git"))
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(line, "gitdir:") {
		return "", fmt.Errorf("%s/.git is not a gitdir pointer", wt)
	}
	gd := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if !filepath.IsAbs(gd) {
		gd = filepath.Join(wt, gd)
	}
	if r, err := filepath.EvalSymlinks(gd); err == nil {
		gd = r
	}
	return filepath.Clean(gd), nil
}

// isTaskWorktree reports whether dir is inside the server's task worktrees.
func (s *Service) isTaskWorktree(dir string) bool {
	root := s.stateDir("worktrees")
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	return s.Cfg.StateDir != "" && strings.HasPrefix(dir, root+string(filepath.Separator))
}

// resolveReadDir resolves the folder of a review or chat. Besides
// allowed_roots it accepts a task worktree peerbridge created, provided its
// source repo is still allowed and its .git file still points at the git dir
// recorded at creation (a peer could have redirected it).
func (s *Service) resolveReadDir(folder string) (string, error) {
	abs, aerr := filepath.Abs(folder)
	if aerr == nil {
		if r, rerr := filepath.EvalSymlinks(abs); rerr == nil {
			abs = r
		}
	}
	// Task worktrees always get the integrity check below, even when
	// allowed_roots happens to cover state_dir (e.g. the home directory).
	if aerr != nil || !s.isTaskWorktree(abs) {
		return s.Cfg.ResolveAllowedDir(folder)
	}
	err := fmt.Errorf("%s is inside peerbridge's task worktrees but is not a recorded task worktree", abs)
	rec, ferr := s.Tasks.find(func(r *taskRecord) bool {
		return abs == r.Worktree || strings.HasPrefix(abs, r.Worktree+string(filepath.Separator))
	})
	if ferr != nil || rec == nil {
		return "", err
	}
	if abs != rec.Worktree {
		// A subdirectory could be (or sit inside) a repository the peer made.
		return "", fmt.Errorf("pass the task worktree root %s, not a directory inside it", rec.Worktree)
	}
	if _, rerr := s.Cfg.ResolveAllowedDir(rec.Repo); rerr != nil {
		return "", fmt.Errorf("task worktree %s belongs to %s, which is no longer in allowed_roots", rec.Worktree, rec.Repo)
	}
	if gd, gerr := worktreeGitDir(rec.Worktree); gerr != nil || gd != rec.GitDir {
		return "", fmt.Errorf("task worktree %s: its .git no longer points at the recorded git dir %s; refusing to run git there", rec.Worktree, rec.GitDir)
	}
	return abs, nil
}

func (s *Service) stateDir(parts ...string) string {
	return filepath.Join(append([]string{s.Cfg.StateDir}, parts...)...)
}

// gitTrusted runs hardened git with an explicit maxBytes (0 = uncapped up to
// the executor's hard cap). prefix goes before the gitSafety flags' args,
// e.g. --git-dir/--work-tree for a task worktree.
func (s *Service) gitTrusted(ctx context.Context, dir string, env []string, maxBytes int, args ...string) (exec.Result, error) {
	full := append(append([]string{}, gitSafety...), args...)
	return s.Runner.Run(ctx, exec.Request{
		Command: "git", Args: full, Dir: dir, Env: append(append([]string{}, gitEnv...), env...),
		Timeout: s.Cfg.Timeout, MaxOutputBytes: maxBytes, StreamTo: s.streamTo(),
	})
}

// gitOK runs gitTrusted and turns a non-zero exit into an error.
func (s *Service) gitOK(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	res, err := s.gitTrusted(ctx, dir, env, 0, args...)
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	if res.ExitCode != 0 || res.TimedOut {
		return "", fmt.Errorf("git %s failed (exit %d): %s", strings.Join(args, " "), res.ExitCode, strings.TrimSpace(agent.Tail(res.Stderr, 2000)))
	}
	return strings.TrimSpace(res.Stdout), nil
}

// repoTop returns the canonical top level of the checkout containing dir.
func (s *Service) repoTop(ctx context.Context, dir string) (string, error) {
	top, err := s.gitOK(ctx, dir, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("%s is not inside a git repository: %w", dir, err)
	}
	if r, err := filepath.EvalSymlinks(top); err == nil {
		top = r
	}
	return top, nil
}

// TaskStart validates the request and runs one task turn in the background.
// conversationID continues an earlier conversation: if it belongs to a task
// of the same agent and repo, that task's worktree is reused; otherwise (an
// id from a chat or review) the conversation continues in a new worktree.
func (s *Service) TaskStart(ctx context.Context, agentName, folder, message, conversationID, base string, opts agent.Options) (string, bool) {
	if !s.Cfg.Tasks.Enabled {
		return "task_start is disabled; set `tasks: {enabled: true}` in config.yaml and restart the app to allow peers to edit files in a separate git worktree", true
	}
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
	if err := agent.ValidateSessionID(conversationID); err != nil {
		return strings.Replace(err.Error(), "session_id", "conversation_id", 1), true
	}
	if base != "" {
		if err := agent.ValidateRef("base", base); err != nil {
			return err.Error(), true
		}
	}
	o, err := s.resolveOptions(b, opts)
	if err != nil {
		return err.Error(), true
	}
	if s.isTaskWorktree(dir) {
		return "folder is a task worktree; to keep working in it, call task_start on the original repo with that task's conversation_id", true
	}
	repo, err := s.repoTop(ctx, dir)
	if err != nil {
		return err.Error(), true
	}
	// A task checks out the whole repository, so its root (not just folder)
	// must be allowed; this also keeps later reviews of the worktree valid.
	if _, err := s.Cfg.ResolveAllowedDir(repo); err != nil {
		return fmt.Sprintf("task_start needs the repository root %s inside allowed_roots (a task works on the whole repo): %v", repo, err), true
	}
	// Interprocess lock on the conversation, held for the whole turn: two
	// peerbridge processes must not both resume it (or both create a new
	// worktree for it) at once.
	var convUnlock func()
	if conversationID != "" {
		if convUnlock, err = s.lockConversation(b.Name(), conversationID); err != nil {
			if errors.Is(err, errLocked) {
				return fmt.Sprintf("conversation %s already has a running turn (in this or another peerbridge process); wait for it to finish", conversationID), true
			}
			return "lock conversation: " + err.Error(), true
		}
	}
	release := func() {
		if convUnlock != nil {
			convUnlock()
		}
	}
	var rec *taskRecord
	if conversationID != "" {
		if rec, err = s.Tasks.byConversation(b.Name(), conversationID); err != nil {
			release()
			return err.Error(), true
		}
		if rec != nil && rec.Repo != repo {
			release()
			return fmt.Sprintf("conversation %s belongs to a task on %s, not %s", conversationID, rec.Repo, repo), true
		}
		if rec != nil && base != "" {
			release()
			return "base only applies to a new task; this conversation already has worktree " + rec.Worktree, true
		}
	}
	sp := jobSpec{Folder: repo, Agent: b.Name(), Kind: agent.KindTask, Conversation: conversationID, MaxTasks: s.Cfg.Tasks.MaxRunning}
	var unlock func()
	if rec != nil {
		sp.Worktree = rec.Worktree
		if unlock, err = s.lockWorktree(rec.ID); err != nil {
			release()
			if errors.Is(err, errLocked) {
				return fmt.Sprintf("a task is already working in %s (in this or another peerbridge process, e.g. the other app); wait for it to finish", rec.Worktree), true
			}
			return "lock task worktree: " + err.Error(), true
		}
	}
	j, err := s.Jobs.addSpec(sp)
	if err != nil {
		if unlock != nil {
			unlock()
		}
		release()
		return err.Error(), true
	}
	s.Log.Info().Str("tool", "task_start").Str("job", j.ID).Str("agent", b.Name()).Str("repo", repo).
		Str("conversation", conversationID).Bool("reuse_worktree", rec != nil).Str("model", o.Model).Str("effort", o.Effort).Msg("invocation")
	go func() {
		// runTask releases the worktree lock itself; the conversation lock is
		// released here, both before finish so the next turn can start at once.
		out, isErr := s.runTask(s.Jobs.ctx, j, b, repo, rec, unlock, message, conversationID, base, o)
		release()
		j.finish(out, isErr)
	}()
	where := "a new git worktree (created now; your checkout is not touched)"
	if rec != nil {
		where = "existing worktree " + rec.Worktree
	}
	cont := "a new conversation"
	if conversationID != "" {
		cont = "continuing conversation " + conversationID
	}
	return fmt.Sprintf(
		"Task started in the background (%s) in %s.\njob_id: %s\nagent: %s\n%srepo: %s\n\nCall job_status with this job_id. Tasks can take a while; the server-side limit is %s (config tasks.timeout).",
		cont, where, j.ID, b.Name(), optionLines(o), repo, s.Cfg.Tasks.Timeout,
	), false
}

// createWorktree makes a fresh worktree + branch for task id from base
// (default HEAD), with the same hardening as review diffs: no hooks (so no
// post-checkout), repo filter drivers neutralised (so no smudge on checkout),
// global/system config ignored, no fsmonitor.
func (s *Service) createWorktree(ctx context.Context, id, agentName, repo, base string) (*taskRecord, error) {
	fenv, err := s.filterOverrides(ctx, repo)
	if err != nil {
		return nil, err
	}
	rev := "HEAD"
	if base != "" {
		rev = base
	}
	sha, err := s.gitOK(ctx, repo, fenv, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil || !shaPattern.MatchString(sha) {
		return nil, fmt.Errorf("cannot resolve %q to a commit (a task needs at least one commit to start from): %v", rev, err)
	}
	common, err := s.gitOK(ctx, repo, fenv, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	if r, err := filepath.EvalSymlinks(common); err == nil {
		common = r
	}
	h := sha256.Sum256([]byte(repo))
	parent := s.stateDir("worktrees", filepath.Base(repo)+"-"+hex.EncodeToString(h[:4]))
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, fmt.Errorf("create worktree dir: %w", err)
	}
	wt := filepath.Join(parent, "task-"+id)
	branch := "peerbridge/task-" + id
	// --no-checkout: files are written below, once the filter drivers of the
	// worktree's own config context (includeIf gitdir:/onbranch: can differ
	// from the main checkout's) are known and neutralised.
	if _, err := s.gitOK(ctx, repo, fenv, "worktree", "add", "--no-checkout", "-b", branch, wt, sha); err != nil {
		return nil, err
	}
	if r, err := filepath.EvalSymlinks(wt); err == nil {
		wt = r
	}
	// Record the admin dir now, while the worktree is still exactly what git
	// wrote; from here on its .git file is peer-writable and never trusted.
	gitDir, err := worktreeGitDir(wt)
	if err != nil {
		return nil, fmt.Errorf("read new worktree's .git: %w", err)
	}
	if filepath.Dir(gitDir) != filepath.Join(common, "worktrees") {
		return nil, fmt.Errorf("new worktree's git dir %q is not under %s/worktrees", gitDir, common)
	}
	rec := &taskRecord{ID: id, Agent: agentName, Repo: repo, Worktree: wt, GitDir: gitDir, Branch: branch, Base: sha, Created: time.Now()}
	wenv, err := s.worktreeFilterOverrides(ctx, rec)
	if err != nil {
		return nil, err
	}
	if _, err := s.gitOK(ctx, wt, wenv, append(rec.gitArgs(), "reset", "--hard", "-q", sha)...); err != nil {
		return nil, fmt.Errorf("populate worktree: %w", err)
	}
	return rec, nil
}

// gitArgs points git at the recorded admin dir and worktree explicitly, so
// the worktree's own (peer-writable) .git file is never consulted.
func (r *taskRecord) gitArgs() []string {
	return []string{"--git-dir=" + r.GitDir, "--work-tree=" + r.Worktree}
}

// worktreeFilterOverrides neutralises the filter drivers visible in the
// task worktree's own config context.
func (s *Service) worktreeFilterOverrides(ctx context.Context, r *taskRecord) ([]string, error) {
	return s.filterOverridesIn(ctx, r.Worktree, r.gitArgs())
}

// nestedRepos lists gitlink paths (submodules or embedded repositories) in
// the task worktree that either changed against the base or hold content.
// An uninitialised submodule from the base (empty directory, unchanged id)
// is not listed: there is nothing in it to lose.
func (s *Service) nestedRepos(g func(int, ...string) (exec.Result, error), rec *taskRecord) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	raw, err := g(0, "diff", "--cached", "--raw", "-z", "--no-abbrev", "--ignore-submodules=none", rec.Base, "--")
	if err != nil || raw.ExitCode != 0 {
		return nil, fmt.Errorf("git diff --raw: %v %s", err, strings.TrimSpace(raw.Stderr))
	}
	if raw.StdoutTruncated {
		return nil, fmt.Errorf("git diff --raw output exceeded %d bytes", exec.StdoutHardCap) // fail closed
	}
	// -z raw: ":<old mode> <new mode> <old> <new> <status>\0<path>\0[<path2>\0]"
	parts := strings.Split(raw.Stdout, "\x00")
	for i := 0; i < len(parts); i++ {
		meta := parts[i]
		if !strings.HasPrefix(meta, ":") {
			continue
		}
		f := strings.Fields(meta)
		if len(f) < 5 || i+1 >= len(parts) {
			continue
		}
		path := parts[i+1]
		if f[0] == ":160000" || f[1] == "160000" {
			add(path)
		}
		i++
		if st := f[4]; strings.HasPrefix(st, "R") || strings.HasPrefix(st, "C") {
			i++ // renames/copies carry a second path
		}
	}
	ls, err := g(0, "ls-files", "-s", "-z")
	if err != nil || ls.ExitCode != 0 {
		return nil, fmt.Errorf("git ls-files: %v %s", err, strings.TrimSpace(ls.Stderr))
	}
	if ls.StdoutTruncated {
		return nil, fmt.Errorf("git ls-files output exceeded %d bytes", exec.StdoutHardCap) // fail closed
	}
	for _, e := range strings.Split(ls.Stdout, "\x00") {
		meta, path, ok := strings.Cut(e, "\t")
		if !ok || !strings.HasPrefix(meta, "160000 ") {
			continue
		}
		if entries, err := os.ReadDir(filepath.Join(rec.Worktree, filepath.FromSlash(path))); err == nil && len(entries) > 0 {
			add(path)
		}
	}
	return out, nil
}

// taskChanges is what collectChanges found in a worktree.
type taskChanges struct {
	stat, diff, patch string
	complete          bool
	notes             []string
	ignored           []string // ignored files/dirs left out of the patch
}

// collectChanges stages everything in the task worktree and saves a binary
// patch of all changes against the base. It never trusts the worktree's .git
// file: git is pointed at the recorded admin dir explicitly.
func (s *Service) collectChanges(ctx context.Context, rec *taskRecord, jobID string) (taskChanges, error) {
	c := taskChanges{complete: true}
	if _, err := os.Stat(rec.GitDir); err != nil {
		return c, fmt.Errorf("worktree admin dir %s is gone (was the worktree removed?): %w", rec.GitDir, err)
	}
	fenv, err := s.worktreeFilterOverrides(ctx, rec)
	if err != nil {
		return c, err
	}
	g := func(maxBytes int, args ...string) (exec.Result, error) {
		return s.gitTrusted(ctx, rec.Worktree, fenv, maxBytes, append(rec.gitArgs(), args...)...)
	}
	add, err := g(0, "add", "-A")
	if err != nil {
		return c, fmt.Errorf("git add: %w", err)
	}
	if w := strings.TrimSpace(add.Stderr); w != "" {
		c.notes = append(c.notes, "git add said: "+agent.Tail(w, 1000))
	}
	// Files matching .gitignore are not staged, so not in the patch either.
	// Usually build output, but it could be something the peer meant to add
	// (e.g. .env.example under a .env* rule): report them before any cleanup.
	ig, err := g(0, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	switch {
	case err != nil || ig.ExitCode != 0:
		c.ignored = append(c.ignored, "(could not list ignored files)")
	default:
		for _, p := range strings.Split(ig.Stdout, "\x00") {
			if p != "" {
				c.ignored = append(c.ignored, p)
			}
		}
		if ig.StdoutTruncated {
			c.ignored = append(c.ignored, "(list truncated)")
		}
	}
	// The saved patch must be consumable by `git apply` whatever the repo's
	// local config says: no colour, standard a/ b/ prefixes (diff.noprefix,
	// diff.mnemonicPrefix), whole-repo paths (diff.relative), normal context
	// (diff.context=0 patches are rejected by git apply), submodule changes
	// included and in patch form (diff.ignoreSubmodules, diff.submodule), no
	// helpers.
	plain := []string{"--no-ext-diff", "--no-textconv", "--no-color", "--src-prefix=a/", "--dst-prefix=b/", "--no-relative", "--unified=3", "--submodule=short"}
	// --cached compares gitlink ids only, so "none" never descends into a
	// nested repository (whose config the peer may have written).
	diffArgs := append(append([]string{"diff"}, plain...), "--cached", "--ignore-submodules=none")
	if add.ExitCode != 0 {
		// Staging failed: fall back to tracked changes only and say so.
		// "dirty" keeps git from running status inside nested repositories.
		c.complete = false
		diffArgs = append(append([]string{"diff"}, plain...), "--ignore-submodules=dirty")
		c.notes = append(c.notes, "INCOMPLETE: staging failed, so the patch has tracked changes only; untracked files are missing (see git status in the worktree)")
	}
	// Against the recorded base, not HEAD: if the user committed part of the
	// work on the task branch, the patch must still hold all of it.
	patch, err := g(0, append(append([]string{}, diffArgs...), "--binary", rec.Base, "--")...)
	if err != nil || patch.ExitCode != 0 {
		return c, fmt.Errorf("git diff failed: %v %s", err, strings.TrimSpace(patch.Stderr))
	}
	if patch.StdoutTruncated {
		c.complete = false
		c.notes = append(c.notes, "INCOMPLETE: the patch exceeded the output cap and was cut")
	}
	if st, err := g(0, append(append([]string{}, diffArgs...), "--stat", rec.Base, "--")...); err == nil {
		c.stat = strings.TrimRight(st.Stdout, "\n")
	}
	// Work inside a nested repository (an embedded repo or a submodule) can
	// never be carried by the patch, which records at most a commit id: its
	// files and commits exist only in the worktree. Detected from git
	// metadata and the filesystem, without running git inside it.
	if nested, err := s.nestedRepos(g, rec); err != nil {
		c.complete = false
		c.notes = append(c.notes, "INCOMPLETE: could not check for nested repositories: "+err.Error())
	} else if len(nested) > 0 {
		c.complete = false
		c.notes = append(c.notes, "INCOMPLETE: the worktree has nested repositories / submodules with content ("+strings.Join(nested, ", ")+"); the patch cannot carry their contents, which exist only in the worktree")
	}
	if patch.Stdout == "" {
		c.notes = append(c.notes, "the peer made no changes outside nested repositories")
		return c, nil
	}
	c.diff = patch.Stdout
	if err := os.MkdirAll(s.stateDir("patches"), 0o700); err != nil {
		return c, err
	}
	// One file per turn (job ids are unique), never overwritten: each
	// result keeps pointing at exactly the patch it showed.
	c.patch = s.stateDir("patches", fmt.Sprintf("task-%s-%s.patch", rec.ID, jobID))
	f, err := os.OpenFile(c.patch, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		c.patch = ""
		return c, fmt.Errorf("save patch: %w", err)
	}
	_, werr := f.WriteString(patch.Stdout)
	if cerr := f.Close(); werr != nil || cerr != nil {
		return c, fmt.Errorf("save patch: %w", errors.Join(werr, cerr))
	}
	return c, nil
}

// runTask runs one task turn and always reports where the work is, even on
// failure, so nothing the peer did is lost.
func (s *Service) runTask(ctx context.Context, j *job, b agent.Backend, repo string, rec *taskRecord, unlock func(), message, conversationID, base string, o agent.Options) (string, bool) {
	if unlock != nil {
		defer unlock()
	}
	var notes []string
	failed := ""
	if rec == nil {
		u, err := s.lockWorktree(j.ID)
		if err != nil {
			return "task failed before the peer started: lock: " + err.Error(), true
		}
		defer u()
		rec, err = s.createWorktree(ctx, j.ID, b.Name(), repo, base)
		if err != nil {
			return "task failed before the peer started: " + err.Error(), true
		}
		s.Jobs.setWorktree(j, rec.Worktree)
		rec.ConversationID = conversationID
		if err := s.Tasks.put(*rec); err != nil {
			notes = append(notes, "could not save the task record: "+err.Error())
		}
		if fenv, err := s.filterOverrides(ctx, repo); err != nil {
			notes = append(notes, "could not check your checkout for uncommitted changes: "+err.Error())
		} else if st, err := s.gitOK(ctx, repo, fenv, "status", "--porcelain", "--ignore-submodules=dirty"); err == nil && st != "" {
			// --ignore-submodules=dirty: never run git inside a submodule
			// (its own filter drivers are not neutralised by fenv).
			notes = append(notes, "your checkout has uncommitted changes; they are NOT in this worktree (it starts from "+short(rec.Base)+")")
		}
	} else if _, err := os.Stat(rec.Worktree); err != nil {
		return fmt.Sprintf("task worktree %s is gone (removed?); start a new task without conversation_id: %v", rec.Worktree, err), true
	}

	tmp := s.stateDir("tmp", j.ID)
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return "create task temp dir: " + err.Error(), true
	}
	defer os.RemoveAll(tmp)

	res, err := s.Runner.Run(ctx, exec.Request{
		Command: b.Command(), Args: b.TaskArgs(message, conversationID, o), Dir: rec.Worktree,
		Env: []string{"TMPDIR=" + tmp}, Timeout: s.Cfg.Tasks.Timeout, StreamTo: s.streamTo(),
	})
	var reply, sid string
	switch {
	case err != nil:
		failed = notFoundHint(b, err)
	default:
		var perr error
		reply, sid, perr = b.ParseChat(res.Stdout)
		switch {
		case res.TimedOut:
			failed = fmt.Sprintf("%s was killed after tasks.timeout (%s)", b.Name(), s.Cfg.Tasks.Timeout)
		case res.StdoutTruncated:
			failed = fmt.Sprintf("%s output exceeded %d bytes", b.Name(), exec.StdoutHardCap)
		case res.ExitCode != 0:
			failed = fmt.Sprintf("%s exited %d", b.Name(), res.ExitCode)
		case perr != nil:
			failed = perr.Error()
		}
	}
	s.Log.Info().Str("tool", "task").Str("job", j.ID).Str("agent", b.Name()).Str("worktree", rec.Worktree).
		Dur("elapsed", res.Elapsed).Int("exit", res.ExitCode).Str("failed", failed).Msg("invocation")
	if sid != "" {
		rec.ConversationID = sid
	}

	ch, cerr := s.collectChanges(ctx, rec, j.ID)
	if ch.patch != "" {
		rec.LastPatch = ch.patch
	}
	if err := s.Tasks.put(*rec); err != nil {
		notes = append(notes, "could not save the task record: "+err.Error())
	}
	notes = append(notes, ch.notes...)
	if n := len(ch.ignored); n > 0 {
		shown := ch.ignored
		more := ""
		if n > 20 {
			shown, more = shown[:20], fmt.Sprintf(", … (%d more)", n-20)
		}
		notes = append(notes, fmt.Sprintf("%d ignored file(s)/dir(s) in the worktree are NOT in the patch (matched by .gitignore; often build output): %s%s", n, strings.Join(shown, ", "), more))
	}
	if cerr != nil {
		notes = append(notes, "COULD NOT COLLECT CHANGES: "+cerr.Error()+" — inspect the worktree directly")
	}
	if gd, err := worktreeGitDir(rec.Worktree); err != nil || gd != rec.GitDir {
		notes = append(notes, "WARNING: the worktree's .git file no longer points at its git dir (the peer may have changed it); use only the commands below, which name the git dir explicitly")
	}
	if b.Name() == "claude" {
		notes = append(notes, "claude has no shell in tasks: it did not run tests or builds")
	}

	var out strings.Builder
	status := "completed"
	if failed != "" {
		status = "FAILED: " + failed
	}
	fmt.Fprintf(&out, "Task %s\n", status)
	fmt.Fprintf(&out, "task: %s\nagent: %s\n", rec.ID, b.Name())
	if rec.ConversationID != "" {
		fmt.Fprintf(&out, "conversation_id: %s\nsession_id: %s\n", rec.ConversationID, rec.ConversationID)
	}
	fmt.Fprintf(&out, "repo: %s\nworktree: %s\nbranch: %s (from %s)\n", rec.Repo, rec.Worktree, rec.Branch, short(rec.Base))
	if ch.patch != "" {
		fmt.Fprintf(&out, "patch: %s\n", ch.patch)
	}
	fmt.Fprintf(&out, "changes: staged in the worktree, not committed\n")
	if reply != "" {
		fmt.Fprintf(&out, "\n--- %s reply ---\n%s\n", agent.Label(b.Name()), s.capReply(reply))
	}
	if failed != "" {
		out.WriteString(diagnostics(b, res, s.Cfg.Tasks.Timeout))
	}
	if ch.stat != "" {
		fmt.Fprintf(&out, "\n--- changes ---\n%s\n", s.capReply(ch.stat))
	}
	if ch.diff != "" {
		fmt.Fprintf(&out, "\n--- diff ---\n%s\n", s.capReply(ch.diff))
	}
	if len(notes) > 0 {
		out.WriteString("\nNotes:\n")
		for _, n := range notes {
			fmt.Fprintf(&out, "- %s\n", n)
		}
	}
	out.WriteString("\nNext steps:\n")
	out.WriteString("- Review the diff above (it is the peer's work, not yet verified).\n")
	recoverable := ch.complete && cerr == nil
	if ch.patch != "" && recoverable {
		fmt.Fprintf(&out, "- Apply to your checkout: git -C %s apply --index %s\n", shellQuote(rec.Repo), shellQuote(ch.patch))
		// Explicit --git-dir/--work-tree: never trust the worktree's .git file,
		// which the peer could have pointed at another repository.
		fmt.Fprintf(&out, "  or commit it on the task branch and merge: %s\n", seq(
			fmt.Sprintf(`git --git-dir=%s --work-tree=%s commit -m "..."`, shellQuote(rec.GitDir), shellQuote(rec.Worktree)),
			fmt.Sprintf("git -C %s merge %s", shellQuote(rec.Repo), rec.Branch)))
	} else if !recoverable {
		fmt.Fprintf(&out, "- The patch is INCOMPLETE: do not apply it. Recover the work from the worktree itself: git --git-dir=%s --work-tree=%s status\n", shellQuote(rec.GitDir), shellQuote(rec.Worktree))
	}
	if rec.ConversationID != "" {
		fmt.Fprintf(&out, "- Continue in the same worktree: task_start(conversation_id=%q, ...)\n", rec.ConversationID)
	}
	if recoverable && len(ch.ignored) > 0 {
		fmt.Fprintf(&out, "- Before cleaning up, check the ignored files listed in the notes (they are not in the patch); then: %s\n", seq(fmt.Sprintf("git -C %s worktree remove --force %s", shellQuote(rec.Repo), shellQuote(rec.Worktree)), fmt.Sprintf("git -C %s branch -D %s", shellQuote(rec.Repo), rec.Branch)))
	} else if recoverable {
		fmt.Fprintf(&out, "- Clean up when done: %s\n", seq(fmt.Sprintf("git -C %s worktree remove --force %s", shellQuote(rec.Repo), shellQuote(rec.Worktree)), fmt.Sprintf("git -C %s branch -D %s", shellQuote(rec.Repo), rec.Branch)))
	} else {
		out.WriteString("- Do not remove the worktree until you have recovered the changes from it.\n")
	}
	return out.String(), failed != ""
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// The copy-pastable commands in task results target the user's shell:
// POSIX sh on macOS/Linux, PowerShell on Windows (where 5.1 has no `&&`).

// shellQuote quotes p for the current platform's shell.
func shellQuote(p string) string { return quoteFor(runtime.GOOS, p) }

// seq joins commands so each runs only if the previous one succeeded.
func seq(cmds ...string) string { return seqFor(runtime.GOOS, cmds...) }

func quoteFor(goos, p string) string {
	if p != "" && strings.IndexFunc(p, func(r rune) bool {
		return !(r == '/' || r == '.' || r == '-' || r == '_' || r == '+' || r == '@' || (goos == "windows" && (r == '\\' || r == ':')) ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'))
	}) < 0 {
		return p
	}
	if goos == "windows" {
		return "'" + strings.ReplaceAll(p, "'", "''") + "'" // PowerShell: '' inside '...'
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}

func seqFor(goos string, cmds ...string) string {
	if goos != "windows" {
		return strings.Join(cmds, " && ")
	}
	out := cmds[0]
	for _, c := range cmds[1:] {
		out += "; if ($LASTEXITCODE -eq 0) { " + c + " }"
	}
	return out
}
