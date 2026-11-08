package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jerryctt/peerbridge/internal/agent"
)

// keepFinished is how long a finished job's result stays retrievable.
const keepFinished = 30 * time.Minute

// job is a background peer-agent invocation (review, chat or task). The
// output/isErr/ended fields are written exactly once by finish() before done
// is closed; readers must only access them after observing <-done (the
// channel close provides the happens-before edge).
type job struct {
	ID     string
	Folder string
	Root   string // worktree root; part of the duplicate-detection key
	Agent  string
	Kind   agent.Kind
	Mode   string // review scope label, or "" for chat
	// Conversation is the conversation a chat/task turn continues ("" for a
	// new one); Worktree is a task's worktree ("" until one is created).
	Conversation string
	Worktree     string
	Started      time.Time

	done   chan struct{}
	output string
	isErr  bool
	ended  time.Time
}

func (j *job) finish(output string, isErr bool) {
	j.output = output
	j.isErr = isErr
	j.ended = time.Now()
	close(j.done)
}

func (j *job) finished() bool {
	select {
	case <-j.done:
		return true
	default:
		return false
	}
}

type jobStore struct {
	mu   sync.Mutex
	jobs map[string]*job

	// ctx is the server-lifetime context background jobs run under, so that
	// shutdown() can cancel in-flight codex processes instead of orphaning
	// them (the executor kills the whole process group on cancellation).
	ctx    context.Context
	cancel context.CancelFunc
}

func newJobStore() *jobStore {
	ctx, cancel := context.WithCancel(context.Background())
	return &jobStore{jobs: make(map[string]*job), ctx: ctx, cancel: cancel}
}

// shutdown cancels all running jobs and waits up to grace for their
// goroutines to observe the cancellation and kill their codex processes.
func (s *jobStore) shutdown(grace time.Duration) {
	s.cancel()
	deadline := time.After(grace)
	s.mu.Lock()
	var pending []*job
	for _, j := range s.jobs {
		if !j.finished() {
			pending = append(pending, j)
		}
	}
	s.mu.Unlock()
	for _, j := range pending {
		select {
		case <-j.done:
		case <-deadline:
			return
		}
	}
}

// jobSpec describes a job to register; see addSpec for the locking rules.
type jobSpec struct {
	Folder       string
	Agent        string
	Kind         agent.Kind
	Mode         string
	Conversation string // conversation this turn continues, if any
	Worktree     string // task: existing worktree being reused, if any
	MaxTasks     int    // task: cap on concurrently running tasks (0 = none)
}

// add registers a review or chat job (see addSpec).
func (s *jobStore) add(folder, agentName string, kind agent.Kind, mode string) (*job, error) {
	return s.addSpec(jobSpec{Folder: folder, Agent: agentName, Kind: kind, Mode: mode})
}

// addSpec registers a new job, refusing it while a conflicting one runs:
//   - review/chat: same (worktree root, agent, kind). Each job spawns a
//     long-running CLI on the host, so retries must poll the existing job
//     instead of stacking processes. Different agents or kinds on the same
//     repo (a codex review plus a claude chat) may run together.
//   - any kind: same (agent, conversation), so two turns never write the
//     same conversation concurrently.
//   - task: same worktree (one writer at a time), and at most MaxTasks
//     running tasks. Tasks on the same repo in different worktrees (codex
//     and claude implementing side by side) are allowed.
func (s *jobStore) addSpec(sp jobSpec) (*job, error) {
	root := worktreeRoot(sp.Folder)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked()
	running := func(j *job) string {
		return fmt.Sprintf("(job %s, started %s ago); poll job_status with that job_id instead of starting another", j.ID, time.Since(j.Started).Round(time.Second))
	}
	tasks := 0
	for _, j := range s.jobs {
		if j.finished() {
			continue
		}
		if sp.Kind != agent.KindTask && j.Kind == sp.Kind && j.Root == root && j.Agent == sp.Agent {
			return nil, fmt.Errorf("a %s %s of %s is already running %s", sp.Agent, sp.Kind, j.Folder, running(j))
		}
		if sp.Conversation != "" && j.Agent == sp.Agent && j.Conversation == sp.Conversation {
			return nil, fmt.Errorf("conversation %s already has a running %s turn %s", sp.Conversation, j.Kind, running(j))
		}
		if j.Kind == agent.KindTask {
			tasks++
			if sp.Worktree != "" && j.Worktree == sp.Worktree {
				return nil, fmt.Errorf("a task is already working in %s %s", sp.Worktree, running(j))
			}
		}
	}
	if sp.Kind == agent.KindTask && sp.MaxTasks > 0 && tasks >= sp.MaxTasks {
		return nil, fmt.Errorf("%d tasks are already running (tasks.max_running = %d); wait for one to finish", tasks, sp.MaxTasks)
	}
	j := &job{
		ID:           newJobID(),
		Folder:       sp.Folder,
		Root:         root,
		Agent:        sp.Agent,
		Kind:         sp.Kind,
		Mode:         sp.Mode,
		Conversation: sp.Conversation,
		Worktree:     sp.Worktree,
		Started:      time.Now(),
		done:         make(chan struct{}),
	}
	s.jobs[j.ID] = j
	return j, nil
}

// setWorktree records the worktree a new task created, so later tasks see
// the writer lock.
func (s *jobStore) setWorktree(j *job, wt string) {
	s.mu.Lock()
	j.Worktree = wt
	s.mu.Unlock()
}

func (s *jobStore) get(id string) (*job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked() // enforce keepFinished on retrieval, not only on add
	j, ok := s.jobs[id]
	return j, ok
}

// gcLocked drops jobs that finished more than keepFinished ago.
// Callers must hold s.mu.
func (s *jobStore) gcLocked() {
	now := time.Now()
	for id, j := range s.jobs {
		if j.finished() && now.Sub(j.ended) > keepFinished {
			delete(s.jobs, id)
		}
	}
}

// worktreeRoot returns the nearest ancestor of dir (including dir itself)
// containing a .git entry — the same worktree codex will discover from any
// directory inside it, so it serves as the duplicate-detection key for jobs
// started from different subdirectories (e.g. /repo/internal and /repo/cmd).
// The .git entry may be a directory or a file (linked worktrees, submodules);
// os.Stat accepts both. Falls back to dir when no .git is found. This is a
// pure filesystem check — the server still never runs git itself. dir is
// already absolute and symlink-resolved by config.ResolveAllowedDir.
func worktreeRoot(dir string) string {
	p := dir
	for {
		if _, err := os.Stat(filepath.Join(p, ".git")); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return dir
		}
		p = parent
	}
}

func newJobID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is effectively unrecoverable; fall back to time.
		return hex.EncodeToString([]byte(time.Now().Format("150405.000")))
	}
	return hex.EncodeToString(b)
}
