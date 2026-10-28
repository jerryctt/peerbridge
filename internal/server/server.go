package server

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jerryctt/peerbridge/internal/agent"
	"github.com/jerryctt/peerbridge/internal/claude"
	"github.com/jerryctt/peerbridge/internal/codex"
	"github.com/jerryctt/peerbridge/internal/config"
	"github.com/jerryctt/peerbridge/internal/exec"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"
)

// version is reported in the MCP initialize handshake. Keep in sync with
// .claude-plugin/plugin.json.
const version = "0.5.0"

// New builds the MCP server. The returned shutdown func cancels any
// still-running background jobs (killing their CLI process groups) and
// should be called after the server stops serving.
func New(cfg *config.Config, r agent.Runner, log zerolog.Logger) (*mcpserver.MCPServer, func()) {
	s := mcpserver.NewMCPServer("peerbridge", version, mcpserver.WithRecovery())
	svc := NewService(cfg, r, log)
	statusPoll := cfg.StatusPoll
	if statusPoll <= 0 {
		statusPoll = 50 * time.Second // config.Load sets this; guard direct construction
	}
	agents := strings.Join(svc.Backends.Names(), " | ")

	text := func(out string, isErr bool) (*mcp.CallToolResult, error) {
		if isErr {
			return mcp.NewToolResultError(out), nil
		}
		return mcp.NewToolResultText(out), nil
	}

	agentOpt := mcp.WithString("agent",
		mcp.Description("Which peer agent runs it: "+agents+". Omit to use the other vendor's agent automatically (a Claude client gets codex, a Codex client gets claude)."),
		mcp.Enum(svc.Backends.Names()...),
	)
	folderOpt := mcp.WithString("folder", mcp.Required(), mcp.Description("Absolute HOST path to the git repo (e.g. /Users/you/proj). Not a sandbox path."))
	scopeOpts := []mcp.ToolOption{
		mcp.WithString("scope",
			mcp.Description("Which changes to review: uncommitted (default, staged+unstaged+untracked), base (against a base branch, requires base param), or commit (a specific commit, requires commit param)"),
			mcp.Enum("uncommitted", "base", "commit"),
		),
		mcp.WithString("base", mcp.Description("Base branch or ref, required when scope=base (e.g. main)")),
		mcp.WithString("commit", mcp.Description("Commit SHA, required when scope=commit")),
		mcp.WithString("prompt", mcp.Description("Custom review instructions. ONLY pass this when the user explicitly asks to narrow or focus the review (e.g. \"focus on concurrency\"); omit it for a normal general review. Safe to combine with any scope.")),
	}

	reviewHandler := func(forceAgent string) mcpserver.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			folder, err := req.RequireString("folder")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			a := forceAgent
			if a == "" {
				a = req.GetString("agent", "")
			}
			return text(svc.ReviewStart(ctx, a, folder,
				req.GetString("scope", "uncommitted"), req.GetString("base", ""), req.GetString("commit", ""), req.GetString("prompt", "")))
		}
	}
	statusHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("job_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return text(svc.JobStatus(ctx, id, statusPoll))
	}

	// Annotations: the peer tools never modify the host (peers are read-only
	// and the server only reads the repo), so they are marked readOnlyHint.
	// Codex honours this with default_tools_approval_mode = "writes" (prompt
	// only for non-read-only tools). Every tool is read-only: there is no
	// general command-execution tool (run_command was removed in v0.5.0).
	readOnly := []mcp.ToolOption{
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
	}
	withRO := func(opts ...mcp.ToolOption) []mcp.ToolOption {
		return append(append([]mcp.ToolOption{}, readOnly...), opts...)
	}

	s.AddTool(mcp.NewTool("review_start",
		append(withRO(
			mcp.WithDescription("Start a read-only code review of changes in a host project folder by a peer agent ("+agents+"). Runs in the background on the host and returns a job_id immediately; poll job_status for the result. Reviews often take several minutes, longer than the MCP client's per-call limit, hence the job model. The peer never modifies files."),
			folderOpt, agentOpt,
		), scopeOpts...)...,
	), reviewHandler(""))

	s.AddTool(mcp.NewTool("chat_start", withRO(
		mcp.WithDescription("Send one message to a peer agent ("+agents+") that can read the host project folder (read-only) and get its reply via job_status. Use it for second opinions, design critique or a multi-round discussion: the job_status result includes a session_id; pass it back as session_id to continue the same conversation. Runs in the background; poll job_status."),
		folderOpt, agentOpt,
		mcp.WithString("message", mcp.Required(), mcp.Description("What to say to the peer. Include the context it needs (what you changed, what you want its opinion on); it only sees the repo and this conversation.")),
		mcp.WithString("session_id", mcp.Description("session_id from a previous job_status result to continue that conversation. Omit to start a new one.")),
	)...), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		folder, err := req.RequireString("folder")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		msg, err := req.RequireString("message")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return text(svc.ChatStart(ctx, req.GetString("agent", ""), folder, msg, req.GetString("session_id", "")))
	})

	pollDesc := fmt.Sprintf("Blocks up to ~%s waiting for completion", statusPoll.Round(time.Second))
	s.AddTool(mcp.NewTool("job_status", withRO(
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithDescription("Get the status/result of a background job started with review_start or chat_start. "+pollDesc+", then returns either the finished result or a 'running' notice; keep calling with the same job_id until it completes. Finished results stay available for ~30 minutes."),
		mcp.WithString("job_id", mcp.Required(), mcp.Description("The job_id returned by review_start / chat_start")),
	)...), statusHandler)

	// Deprecated aliases kept so existing installs and skills keep working.
	s.AddTool(mcp.NewTool("codex_review_start",
		append(withRO(
			mcp.WithDescription("Deprecated: use review_start (optionally with agent=codex). Starts a read-only codex review in the background and returns a job_id; poll job_status or codex_review_status."),
			folderOpt,
		), scopeOpts...)...,
	), reviewHandler("codex"))
	s.AddTool(mcp.NewTool("codex_review_status", withRO(
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithDescription("Deprecated: use job_status. Same behaviour: "+pollDesc+" and returns the finished review or a 'running' notice."),
		mcp.WithString("job_id", mcp.Required(), mcp.Description("The job_id returned by codex_review_start / review_start")),
	)...), statusHandler)

	return s, func() { svc.Jobs.shutdown(3 * time.Second) }
}

// Service holds the dependencies the tool handlers need.
type Service struct {
	Cfg      *config.Config
	Runner   agent.Runner
	Log      zerolog.Logger
	Jobs     *jobStore
	Backends agent.Registry
}

// NewService wires the codex and claude backends from cfg.
func NewService(cfg *config.Config, r agent.Runner, log zerolog.Logger) *Service {
	cx, cl := cfg.Agent("codex"), cfg.Agent("claude")
	return &Service{
		Cfg: cfg, Runner: r, Log: log, Jobs: newJobStore(),
		Backends: agent.Registry{
			"codex":  codex.New(agent.Settings{Command: cx.Command, ExtraArgs: cx.ExtraArgs}),
			"claude": claude.New(agent.Settings{Command: cl.Command, ExtraArgs: cl.ExtraArgs}),
		},
	}
}

// ResolveAgent picks the backend: an explicit name wins; otherwise the peer
// is inferred from the MCP client's name (Claude talks to codex, Codex talks
// to claude).
func ResolveAgent(explicit, clientName string, reg agent.Registry) (agent.Backend, error) {
	name := explicit
	if name == "" {
		inferred, ok := agent.Default(clientName)
		if !ok {
			return nil, fmt.Errorf("cannot infer which agent to use from MCP client %q; specify agent (one of: %s)", clientName, strings.Join(reg.Names(), ", "))
		}
		name = inferred
	}
	return reg.Get(name)
}

// clientName returns the MCP client's reported name for this call, or "".
func clientName(ctx context.Context) string {
	sess := mcpserver.ClientSessionFromContext(ctx)
	if sess == nil {
		return ""
	}
	if ci, ok := sess.(mcpserver.SessionWithClientInfo); ok {
		return ci.GetClientInfo().Name
	}
	return ""
}

// ReviewStart validates the request, then runs the review in a background
// goroutine so the MCP call returns immediately. The goroutine deliberately
// does not use the request context (it is cancelled as soon as this call
// returns); it runs under the job store's server-lifetime context so
// shutdown() can still cancel it.
func (s *Service) ReviewStart(ctx context.Context, agentName, folder, scope, base, commit, prompt string) (string, bool) {
	b, err := ResolveAgent(agentName, clientName(ctx), s.Backends)
	if err != nil {
		return err.Error(), true
	}
	dir, err := s.Cfg.ResolveAllowedDir(folder)
	if err != nil {
		return err.Error(), true
	}
	// Catch bad scope/base/commit combinations now, not minutes later.
	if _, _, err := b.ReviewArgs(scope, base, commit, prompt); err != nil {
		return err.Error(), true
	}
	label := scope
	if label == "" {
		label = "uncommitted"
	}
	j, err := s.Jobs.add(dir, b.Name(), agent.KindReview, label)
	if err != nil {
		return err.Error(), true
	}
	s.Log.Info().Str("tool", "review_start").Str("job", j.ID).Str("agent", b.Name()).Str("folder", dir).Str("mode", label).Msg("invocation")
	go func() {
		out, isErr := s.runReview(s.Jobs.ctx, b, dir, scope, base, commit, prompt)
		j.finish(out, isErr)
	}()
	return fmt.Sprintf(
		"Review started in the background.\njob_id: %s\nagent: %s\nmode: %s\nfolder: %s\n\nCall job_status with this job_id to get the result. Reviews typically take 1-10 minutes; the server-side limit is %s (config 'timeout').",
		j.ID, b.Name(), label, dir, s.Cfg.Timeout,
	), false
}

// ChatStart starts one conversational turn with the peer agent as a
// background job. sessionID continues an earlier conversation.
func (s *Service) ChatStart(ctx context.Context, agentName, folder, message, sessionID string) (string, bool) {
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
	if err := agent.ValidateSessionID(sessionID); err != nil {
		return err.Error(), true
	}
	j, err := s.Jobs.add(dir, b.Name(), agent.KindChat, "")
	if err != nil {
		return err.Error(), true
	}
	s.Log.Info().Str("tool", "chat_start").Str("job", j.ID).Str("agent", b.Name()).Str("folder", dir).Bool("resume", sessionID != "").Msg("invocation")
	go func() {
		out, isErr := s.runChat(s.Jobs.ctx, b, dir, message, sessionID)
		j.finish(out, isErr)
	}()
	cont := "a new conversation"
	if sessionID != "" {
		cont = "continuing session " + sessionID
	}
	return fmt.Sprintf(
		"Chat turn started in the background (%s).\njob_id: %s\nagent: %s\nfolder: %s\n\nCall job_status with this job_id to get the reply; it includes the session_id to pass to the next chat_start.",
		cont, j.ID, b.Name(), dir,
	), false
}

// JobStatus reports on a background job, blocking up to wait for it to
// finish (long-poll) so callers need fewer round trips.
func (s *Service) JobStatus(ctx context.Context, jobID string, wait time.Duration) (string, bool) {
	j, ok := s.Jobs.get(jobID)
	if !ok {
		return fmt.Sprintf("unknown job_id %q (finished jobs are kept for ~%s; start a new job with review_start or chat_start)", jobID, keepFinished), true
	}
	select {
	case <-j.done:
		prefix := fmt.Sprintf("status: completed (job %s, agent %s, kind %s, elapsed %s)\n\n", j.ID, j.Agent, j.Kind, j.ended.Sub(j.Started).Round(time.Second))
		return prefix + j.output, j.isErr
	case <-ctx.Done():
		return "status check cancelled by client", true
	case <-time.After(wait):
		return fmt.Sprintf(
			"status: running (job %s, agent %s, kind %s, elapsed %s)\nStill in progress. Call job_status again with the same job_id.",
			j.ID, j.Agent, j.Kind, time.Since(j.Started).Round(time.Second),
		), false
	}
}

func (s *Service) streamTo() io.Writer {
	if s.Cfg.StreamEnabled() {
		return os.Stderr
	}
	return nil
}

// run executes the backend command with the server's timeout. maxBytes caps
// the captured output; 0 means uncapped, used when stdout is a structured
// protocol stream that must be parsed whole before the reply is trimmed.
// stdin, when non-empty, is fed to the process (large review diffs go this
// way rather than through argv, which has a ~128 KiB per-argument limit).
func (s *Service) run(ctx context.Context, b agent.Backend, dir string, args []string, stdin string, maxBytes int) (exec.Result, error) {
	return s.Runner.Run(ctx, exec.Request{
		Command: b.Command(), Args: args, Dir: dir, Stdin: stdin,
		Timeout: s.Cfg.Timeout, MaxOutputBytes: maxBytes, StreamTo: s.streamTo(),
	})
}

// emptyTree is git's well-known hash of the empty tree: diffing against it
// shows every tracked file as added, which is the right baseline for a
// repository whose HEAD is unborn (no commits yet).
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// gitSafety neutralises the ways plain git can execute programs or write to
// the repository while we only want to read it:
//   - core.fsmonitor=false: `git status`/`diff` would otherwise spawn the
//     configured fsmonitor hook;
//   - core.hooksPath=<devnull>: no hook directory can be found;
//   - the env drops the user's and system's git config (where git-lfs and
//     other clean/smudge filters, diff.external, etc. are normally defined;
//     the repo's own .git/config is the user's local state and still applies)
//     and GIT_OPTIONAL_LOCKS=0 stops `git status` from refreshing the index.
//
// os.DevNull is /dev/null on Unix and NUL on Windows, which git accepts for
// all three settings.
var gitSafety = []string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=" + os.DevNull}

var gitEnv = []string{
	"GIT_CONFIG_GLOBAL=" + os.DevNull,
	"GIT_CONFIG_SYSTEM=" + os.DevNull,
	"GIT_CONFIG_NOSYSTEM=1",
	"GIT_OPTIONAL_LOCKS=0",
	"GIT_TERMINAL_PROMPT=0",
}

// git runs one read-only git command in dir with the configured caps.
// extraEnv (e.g. from filterOverrides) is appended after gitEnv.
func (s *Service) git(ctx context.Context, dir string, extraEnv []string, args ...string) (exec.Result, error) {
	full := append(append([]string{}, gitSafety...), args...)
	env := append(append([]string{}, gitEnv...), extraEnv...)
	return s.Runner.Run(ctx, exec.Request{
		Command: "git", Args: full, Dir: dir, Env: env,
		Timeout: s.Cfg.Timeout, MaxOutputBytes: s.Cfg.MaxOutputBytes, StreamTo: s.streamTo(),
	})
}

var filterKeyPattern = regexp.MustCompile(`^filter\.(.+)\.(clean|smudge|process|required)$`)

// filterOverrides neutralises clean/smudge/process filter drivers the
// repository configures. Worktree diffs run clean filters to normalise file
// contents, and a filter is an arbitrary program; .gitattributes can only
// *select* a driver, so emptying every configured driver is sufficient.
//
// The probe runs without a scope flag, under the same isolated env the later
// commands use, so it sees exactly what they will: repo-local config, the
// worktree config (extensions.worktreeConfig) and anything pulled in through
// include/includeIf — while global/system are the null device. The overrides are
// passed as GIT_CONFIG_COUNT/KEY/VALUE env (git >= 2.31), which has the
// highest precedence of all config sources.
func (s *Service) filterOverrides(ctx context.Context, dir string) ([]string, error) {
	// Uncapped: a truncated list would leave a driver live. Fail closed if
	// the runner reports truncation anyway.
	args := append(append([]string{}, gitSafety...), "config", "--name-only", "--get-regexp", `^filter\.`)
	res, err := s.Runner.Run(ctx, exec.Request{
		Command: "git", Args: args, Dir: dir, Env: gitEnv,
		Timeout: s.Cfg.Timeout, MaxOutputBytes: 0, StreamTo: s.streamTo(),
	})
	if err != nil {
		return nil, fmt.Errorf("git config: %w", err)
	}
	// exit 1 = no matching keys; anything else is a real failure.
	if res.ExitCode != 0 && res.ExitCode != 1 {
		return nil, fmt.Errorf("git config --get-regexp failed (exit %d): %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	if res.Truncated {
		return nil, fmt.Errorf("git config --get-regexp output was truncated; refusing to compute a diff with possibly live filter drivers")
	}
	seen := map[string]bool{}
	var drivers []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		m := filterKeyPattern.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		drivers = append(drivers, m[1])
	}
	if len(drivers) == 0 {
		return nil, nil
	}
	var env []string
	n := 0
	add := func(k, v string) {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", n, k), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", n, v))
		n++
	}
	for _, d := range drivers {
		add("filter."+d+".clean", "")
		add("filter."+d+".smudge", "")
		add("filter."+d+".process", "")
		add("filter."+d+".required", "false")
	}
	return append([]string{fmt.Sprintf("GIT_CONFIG_COUNT=%d", n)}, env...), nil
}

// gitArgs returns the read-only git invocations whose output describes the
// review scope. Used only for backends without a native review command: the
// claude peer has no shell, so peerbridge computes the diff for it. Refs were
// validated by agent.ValidateRef (never option-like); the trailing "--" ends
// option parsing anyway so a ref can only ever be a revision.
//
// --no-ext-diff and --no-textconv keep git from running diff.external or
// textconv helpers from the user's or repo's git config: those would execute
// arbitrary programs on the host outside any peer sandbox.
//
// For scope=base, mergeBase is the resolved merge-base of base and HEAD: the
// worktree is diffed against it (not `base...HEAD`), so uncommitted fixes made
// during an edit → review → edit loop are part of the next review.
func gitArgs(scope, base, commit string, headUnborn bool, mergeBase string) [][]string {
	const safe1, safe2 = "--no-ext-diff", "--no-textconv"
	switch scope {
	case "base":
		return [][]string{
			{"status", "--short", "--untracked-files=all"},
			{"diff", safe1, safe2, mergeBase, "--"},
		}
	case "commit":
		// --diff-merges=first-parent: for a merge commit, plain `show` prints a
		// combined diff that omits hunks matching either parent (often nothing
		// at all); the first-parent diff is what the merge brought in.
		return [][]string{{"show", safe1, safe2, "--diff-merges=first-parent", "--format=medium", commit, "--"}}
	default: // uncommitted
		head := "HEAD"
		if headUnborn {
			head = emptyTree
		}
		// Staged (index vs HEAD) and unstaged (worktree vs index) separately:
		// a single `diff HEAD` hides a staged change that was reverted in the
		// worktree, and the peer has no shell to look at the index itself.
		return [][]string{
			{"status", "--short", "--untracked-files=all"},
			{"diff", safe1, safe2, "--cached", head, "--"},
			{"diff", safe1, safe2, "--"},
		}
	}
}

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

// changeContext runs gitArgs in dir and returns their concatenated output
// (capped at MaxOutputBytes per command), for feeding to a review prompt.
// truncated reports whether any command's output hit the cap, so the caller
// can tell the user the review saw an incomplete diff.
func (s *Service) changeContext(ctx context.Context, dir, scope, base, commit string) (text string, truncated bool, err error) {
	fenv, err := s.filterOverrides(ctx, dir)
	if err != nil {
		return "", false, err
	}
	headUnborn := false
	mergeBase := ""
	switch scope {
	case "", "uncommitted":
		res, err := s.git(ctx, dir, fenv, "rev-parse", "--verify", "--quiet", "HEAD")
		if err != nil {
			return "", false, fmt.Errorf("git rev-parse: %w", err)
		}
		headUnborn = res.ExitCode != 0
	case "base":
		res, err := s.git(ctx, dir, fenv, "merge-base", base, "HEAD")
		if err != nil {
			return "", false, fmt.Errorf("git merge-base: %w", err)
		}
		if res.ExitCode != 0 {
			return "", false, fmt.Errorf("git merge-base %s HEAD failed (exit %d): %s", base, res.ExitCode, strings.TrimSpace(res.Stderr))
		}
		mergeBase = strings.TrimSpace(res.Stdout)
		if !shaPattern.MatchString(mergeBase) {
			return "", false, fmt.Errorf("git merge-base %s HEAD returned %q, not a commit id", base, agent.Tail(mergeBase, 80))
		}
	}
	var out strings.Builder
	for _, args := range gitArgs(scope, base, commit, headUnborn, mergeBase) {
		res, err := s.git(ctx, dir, fenv, args...)
		if err != nil {
			return "", false, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		if res.ExitCode != 0 {
			return "", false, fmt.Errorf("git %s failed (exit %d): %s", strings.Join(args, " "), res.ExitCode, strings.TrimSpace(res.Stderr))
		}
		fmt.Fprintf(&out, "$ git %s\n%s\n", strings.Join(args, " "), res.Stdout)
		if res.Truncated {
			truncated = true
			out.WriteString("[diff truncated by max_output_bytes]\n")
		}
	}
	return out.String(), truncated, nil
}

// capReply trims a parsed reply to MaxOutputBytes with a marker.
func (s *Service) capReply(text string) string {
	if s.Cfg.MaxOutputBytes > 0 && len(text) > s.Cfg.MaxOutputBytes {
		return text[:s.Cfg.MaxOutputBytes] + "\n[output truncated]\n"
	}
	return text
}

func notFoundHint(b agent.Backend, err error) string {
	return fmt.Sprintf("%v (is %q installed and on PATH? set agents.%s.command in config.yaml to an absolute path)", err, b.Command(), b.Name())
}

func (s *Service) runReview(ctx context.Context, b agent.Backend, dir, scope, base, commit, prompt string) (string, bool) {
	args, native, err := b.ReviewArgs(scope, base, commit, prompt)
	if err != nil {
		return err.Error(), true
	}
	maxBytes := s.Cfg.MaxOutputBytes
	var stdin string
	inputTruncated := false
	if !native {
		p, err := agent.ReviewPrompt(scope, base, commit, prompt)
		if err != nil {
			return err.Error(), true
		}
		diff, truncated, err := s.changeContext(ctx, dir, scope, base, commit)
		if err != nil {
			return err.Error(), true
		}
		inputTruncated = truncated
		args = b.ChatArgs(p, "")
		// The diff can be far larger than an argv element may be; the CLIs
		// append piped stdin to the prompt, so it travels that way.
		stdin = "--- diff ---\n" + diff
		maxBytes = 0 // structured output: parse whole, cap the reply afterwards
	}
	res, err := s.run(ctx, b, dir, args, stdin, maxBytes)
	if err != nil {
		s.Log.Info().Str("tool", "review").Str("agent", b.Name()).Str("folder", dir).Err(err).Msg("invocation")
		return notFoundHint(b, err), true
	}
	s.Log.Info().Str("tool", "review").Str("agent", b.Name()).Str("folder", dir).Dur("elapsed", res.Elapsed).Int("exit", res.ExitCode).Msg("invocation")

	text := res.Stdout
	if !native {
		if res.ExitCode != 0 || res.TimedOut {
			return fmt.Sprintf("%s review failed (%s exit %d)\n", agent.Label(b.Name()), b.Name(), res.ExitCode) + diagnostics(b, res, s.Cfg.Timeout), true
		}
		reply, _, perr := b.ParseChat(res.Stdout)
		if perr != nil {
			return perr.Error() + "\n" + diagnostics(b, res, s.Cfg.Timeout), true
		}
		text = s.capReply(reply)
	}
	label := scope
	if label == "" {
		label = "uncommitted"
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Mode: %s (%s exit %d)\n\n--- %s review ---\n%s\n", label, b.Name(), res.ExitCode, agent.Label(b.Name()), text)
	if inputTruncated {
		fmt.Fprintf(&out, "\n[WARNING: the diff given to %s was truncated at max_output_bytes (%d); files beyond that point were not reviewed. Narrow the scope or raise max_output_bytes.]\n", b.Name(), s.Cfg.MaxOutputBytes)
	}
	out.WriteString(diagnostics(b, res, s.Cfg.Timeout))
	return out.String(), false
}

func (s *Service) runChat(ctx context.Context, b agent.Backend, dir, message, sessionID string) (string, bool) {
	res, err := s.run(ctx, b, dir, b.ChatArgs(message, sessionID), "", 0)
	if err != nil {
		s.Log.Info().Str("tool", "chat").Str("agent", b.Name()).Str("folder", dir).Err(err).Msg("invocation")
		return notFoundHint(b, err), true
	}
	s.Log.Info().Str("tool", "chat").Str("agent", b.Name()).Str("folder", dir).Dur("elapsed", res.Elapsed).Int("exit", res.ExitCode).Msg("invocation")

	reply, sid, perr := b.ParseChat(res.Stdout)
	if perr != nil || res.ExitCode != 0 || res.TimedOut {
		var out strings.Builder
		fmt.Fprintf(&out, "%s chat failed (%s exit %d)", agent.Label(b.Name()), b.Name(), res.ExitCode)
		if perr != nil {
			fmt.Fprintf(&out, ": %v", perr)
		}
		out.WriteString("\n")
		if sid != "" {
			fmt.Fprintf(&out, "session_id: %s\n", sid)
		}
		out.WriteString(diagnostics(b, res, s.Cfg.Timeout))
		return out.String(), true
	}
	var out strings.Builder
	fmt.Fprintf(&out, "session_id: %s\n\n--- %s reply ---\n%s\n", sid, agent.Label(b.Name()), s.capReply(reply))
	out.WriteString(diagnostics(b, res, s.Cfg.Timeout))
	return out.String(), false
}

// diagnostics appends stderr (only when the CLI failed — its stderr is a
// verbose progress trace that is already streamed to the server log), plus
// timeout and truncation markers.
func diagnostics(b agent.Backend, res exec.Result, timeout time.Duration) string {
	var out strings.Builder
	if res.ExitCode != 0 && res.Stderr != "" {
		fmt.Fprintf(&out, "\n--- %s stderr (exit %d) ---\n%s\n", b.Name(), res.ExitCode, agent.Tail(res.Stderr, 8192))
	}
	if res.TimedOut {
		fmt.Fprintf(&out, "\n[%s was killed after the server-side timeout of %s (config 'timeout'); the result is incomplete]\n", b.Name(), timeout)
	}
	if res.Truncated {
		out.WriteString("\n[output truncated]\n")
	}
	return out.String()
}
