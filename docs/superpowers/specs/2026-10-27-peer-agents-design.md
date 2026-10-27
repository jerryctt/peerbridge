# hostrunner-mcp — Peer Agents (Claude ⇄ Codex) Design Spec

Date: 2026-10-27
Status: Approved for planning
Author: Jerry (with Claude / Cowork)
Supersedes parts of: `2026-06-27-hostrunner-mcp-design.md` (tool surface, config)

## 1. Problem & Purpose

Today hostrunner lets Claude (Desktop/Cowork/Code) run a read-only `codex
review` on the host. The user also holds a paid Codex subscription with the
Codex macOS app, and a paid Claude subscription with the `claude` CLI. The goal
is to make hostrunner **symmetric and host-agnostic**:

- Installable in **Claude Desktop/Code** (existing plugin path) **and** in
  **Codex** (CLI, macOS app, IDE extension — they share `~/.codex/config.toml`
  and `~/.agents/skills/`).
- From either host, the agent in the GUI can ask the *other* vendor's agent to
  **review** the current changes, or hold a **multi-turn discussion** with it
  (design critique, second opinion, debate), all read-only.

### Goals

- One Go binary, one config, two registration paths (Claude plugin, Codex
  `config.toml`).
- Generic tools with an `agent` selector (`codex` | `claude`), defaulting to
  "the other agent" based on the MCP client that connected.
- Multi-turn chat with session continuation on both backends
  (`claude -p --resume`, `codex exec resume`).
- Peer agents are strictly **read-only**: they may read the repo and run
  read-only git commands; only the agent in the GUI edits files.
- Backward compatible: `codex_review_start` / `codex_review_status` keep
  working unchanged (thin aliases) so the published plugin and existing
  `codex-loop` skill don't break.
- One-shot Codex installer script; README sections for both hosts.

### Non-goals

- No server-driven N-round auto-debate tool; the GUI agent drives rounds.
- No write access for peers (no `allow_write` config).
- No third backend (gemini etc.) in this change — but adding one must be a
  single new package plus one registry line.
- No Windows support for the installer scripts (binary still builds).

## 2. Constraints & Key Decisions

| # | Decision | Rationale |
|---|----------|-----------|
| D1 | Abstract an `agent.Backend` interface; `internal/codex` and new `internal/claude` implement it. `internal/codex` stays at its current path (no rename). | Server code becomes backend-agnostic; no file moves so the change applies cleanly on the user's machine. |
| D2 | Chat also uses the background job model (`chat_start` + `job_status`), never a synchronous tool call. | Codex's MCP `tool_timeout_sec` defaults to 60s; Claude Desktop kills calls at ~180s. A `claude -p` reply that reads a repo often takes 1–3 min. |
| D3 | Single `job_status` tool for every job kind. | One polling contract for the skills; aliases map onto it. |
| D4 | `agent` is optional; default inferred from MCP `clientInfo.name` (contains `claude` → `codex`; contains `codex` → `claude`); otherwise error "specify agent". | Same skill text works unchanged in both hosts. |
| D5 | Duplicate-job key = (worktree root, agent, kind). | One repo may run a codex review and a claude chat concurrently, but never two codex reviews. |
| D6 | Peer `claude` runs `claude -p <msg> --strict-mcp-config --setting-sources "" --output-format json --permission-mode dontAsk --tools Read,Grep,Glob --allowedTools Read,Grep,Glob --disallowedTools Edit,Write,NotebookEdit,Bash`. No shell at all; review diffs are computed by hostrunner (D11). | `--strict-mcp-config` with no `--mcp-config` loads zero MCP servers (no recursion) while keeping the user's OAuth login; `--bare` was rejected in review because it disables OAuth/keychain auth. `--setting-sources ""` stops user/project permission rules and hooks from widening the peer (`--allowedTools` is additive). Bash was removed in review: claude has no OS sandbox and `Bash(git diff:*)` would still allow `git diff --output=FILE`. |
| D7 | Peer `codex` chat runs `codex exec --json --sandbox read-only --skip-git-repo-check --ignore-user-config "<prompt>"`; resume uses `codex exec resume --json --skip-git-repo-check --ignore-user-config -c sandbox_mode="read-only" <SESSION_ID> "<prompt>"` (the `resume` subcommand rejects `--sandbox`, verified live). Review keeps `codex review` as today. | `--json` gives `thread.started.thread_id` and `item.completed/agent_message`. `--ignore-user-config` skips `~/.codex/config.toml` (auth still read from `CODEX_HOME`) and was verified live to drop the user's MCP servers; `-c mcp_servers={}` merges rather than replaces (servers still listed), and `mcp_servers.<name>.enabled=false` fails with "invalid transport" when the entry is absent — both rejected. Model preferences can be re-supplied via `extra_args`. |
| D8 | Claude has no `review` subcommand → server composes a review prompt (scope sentence + fixed instructions + user prompt) and runs it through the chat path, but as kind `review`. | Keeps `review_start` semantics identical for both agents. |
| D9 | Config gains an `agents:` map; legacy `codex_extra_args` still honoured (merged into `agents.codex.extra_args`). | Backward compatible config. |
| D10 | Commands are referenced by configurable path (`agents.<a>.command`, default `codex` / `claude`). | macOS GUI apps don't inherit shell `PATH`; users can put absolute paths. |
| D11 | For backends without a native review (claude), hostrunner runs read-only `git` itself (`status --short --untracked-files=all` + `diff HEAD`; `diff <base>...HEAD`; `show --format=medium <sha>`), caps the output at `max_output_bytes`, and feeds it to the peer on **stdin** prefixed by `--- diff ---` (argv has a ~128 KiB per-argument limit; the CLIs append piped stdin to the prompt). Refs pass `agent.ValidateRef` (no leading `-`, restricted charset) and git invocations end with `--`, so a caller value can never become a git option. An unborn HEAD diffs against the empty tree. A failing git command fails the job before the peer runs. | The peer has no shell (D6), so it cannot compute the diff itself. This revises the v1 non-goal "no git handling on our side" for the claude path only; codex still computes its own diff. |
| D12 | Structured peer output (claude JSON, codex JSONL) is captured uncapped, parsed, and only the extracted reply is trimmed to `max_output_bytes`. Native `codex review` text output stays capped at capture time. | Capping JSONL before parsing dropped the final `agent_message` on long tool traces (review finding). |

## 3. Tool Surface

All folder parameters are absolute **host** paths inside `allowed_roots`
(sandbox `/sessions/...` paths rejected, as today).

### `review_start`
Params: `folder` (req), `agent?` (`codex`|`claude`), `scope?`
(`uncommitted`|`base`|`commit`, default `uncommitted`), `base?`, `commit?`,
`prompt?`.
Returns immediately: `job_id`, `agent`, `mode`, `folder`, hint to poll
`job_status`.
- `agent=codex`: exactly today's `codex review` behaviour (`codex.ReviewArgs`).
- `agent=claude`: composes `<scope sentence> <standard review instructions>
  <user prompt>` and runs the claude chat command (fresh session, no resume).

### `chat_start`
Params: `folder` (req), `message` (req), `agent?`, `session_id?`.
Returns immediately: `job_id`, `agent`, `folder`, whether this continues a
session.
- `session_id` absent → new conversation.
- `session_id` present → `claude -p --resume <id>` / `codex exec resume <id>`.
  A `session_id` that the backend rejects surfaces as a failed job with the
  CLI's stderr.

### `job_status`
Params: `job_id` (req). Long-polls up to 50s (unchanged). Result header:
`status: completed (job …, agent …, kind …, elapsed …)`.
- review job body: `Mode: … (<agent> exit N)\n\n--- <Agent> review ---\n<text>`
- chat job body: `session_id: <id>\n\n--- <Agent> reply ---\n<text>`
Timeout/truncation markers as today. Finished jobs kept ~30 min.

### Aliases (deprecated, kept for compatibility)
- `codex_review_start(folder, scope, base, commit, prompt)` → `review_start`
  with `agent=codex`.
- `codex_review_status(job_id)` → `job_status`.
Descriptions say "Deprecated: use review_start / job_status".

### `run_command`
Unchanged.

## 4. Architecture

```
cmd/hostrunner/main.go
internal/config        Config (+ agents map, legacy codex_extra_args merge)
internal/exec          argv executor (unchanged)
internal/agent         Backend interface, Kind, registry, review-prompt builder
internal/codex         Backend impl: ReviewArgs (existing), ChatArgs, ParseChat (JSONL)
internal/claude        Backend impl: ChatArgs, ParseChat (JSON), review via chat
internal/server        tools, handlers, job store (generic job: agent+kind)
skills/codex-loop      existing skill (updated to prefer review_start/job_status)
skills/peer-review     agent-neutral review loop skill (both hosts)
skills/peer-chat       agent-neutral discussion skill (both hosts)
scripts/launch.sh      unchanged (Claude plugin launcher)
scripts/install-codex.sh  new: binary + config.toml entry + skills → ~/.agents/skills
```

### `internal/agent`

```go
type Kind string // "review" | "chat"

type Backend interface {
    Name() string                                   // "codex" | "claude"
    // ReviewArgs returns argv (after the command) for a review of scope.
    // ok=false means the backend has no native review; caller uses ChatArgs
    // with agent.ReviewPrompt(...) instead.
    ReviewArgs(scope, base, commit, prompt string) (args []string, native bool, err error)
    // ChatArgs returns argv for one chat turn. sessionID "" starts a new session.
    ChatArgs(message, sessionID string) []string
    // ParseChat extracts the assistant reply and the session id from stdout.
    ParseChat(stdout string) (reply, sessionID string, err error)
}

func ReviewPrompt(scope, base, commit, userPrompt string) (string, error)
func Default(clientName string) (string, bool) // infer peer from MCP clientInfo
```

`Settings` (per agent, from config): `Command string`, `ExtraArgs []string`. Backends receive settings at construction.

### `internal/codex` additions
- `ChatArgs`: `exec --json --sandbox read-only --skip-git-repo-check
  --ignore-user-config [extra] <message>`; with session:
  `exec resume --json --skip-git-repo-check --ignore-user-config
  -c sandbox_mode="read-only" [extra] <sessionID> <message>`.
- `ParseChat`: scan JSONL; `thread.started` → `thread_id`; last
  `item.completed` with `item.type == "agent_message"` → `text`. Error if no
  agent message found (include tail of stdout for diagnostics).
- Existing `ReviewArgs` unchanged, wrapped to satisfy the interface
  (`native=true`).

### `internal/claude`
- `ReviewArgs` → `native=false`.
- `ChatArgs`: `-p <message> --strict-mcp-config --setting-sources ""
  --output-format json --permission-mode dontAsk --tools Read,Grep,Glob
  --allowedTools Read,Grep,Glob --disallowedTools Edit,Write,NotebookEdit,Bash
  [--resume <id>] [extra]`.
- `ParseChat`: JSON object; `session_id`, `result`; if `is_error` true →
  error carrying `result`.

### `internal/server`
- `job` generalised: `Agent`, `Kind`, plus existing fields. Duplicate key
  `(Root, Agent, Kind)`.
- `HandleReviewStart(ctx, …, agent, folder, scope, base, commit, prompt)`:
  resolve dir, resolve agent (param or inferred from
  `server.ClientSessionFromContext(ctx).(SessionWithClientInfo).GetClientInfo().Name`),
  validate args early, spawn goroutine.
- `HandleChatStart(ctx, …, agent, folder, message, sessionID)`.
- `HandleJobStatus` (renamed from `HandleReviewStatus`, same behaviour,
  richer header).
- Alias tools registered with the old names and old schemas, delegating to the
  same handlers with `agent="codex"`.

### Config (`config.yaml`)

```yaml
allowed_roots: [/Users/you/code]
allowed_commands: [codex, claude, git]
timeout: 600s
max_output_bytes: 200000
stream_output: false
agents:
  codex:
    command: codex              # or absolute path
    extra_args: []              # e.g. ["-c", "model=o3"]
  claude:
    command: claude
    extra_args: []              # e.g. ["--model", "opus"]
# legacy, still honoured: codex_extra_args: [...]
```

Defaults are filled when `agents` or an entry is missing, so existing config
files keep working.

## 5. Data Flow (chat, Codex host → Claude peer)

1. Codex app calls `chat_start(folder=/Users/j/proj, message="Critique this
   design…")`. clientInfo name contains "codex" → agent=claude.
2. Server validates folder, registers job (root, claude, chat), returns job_id.
3. Goroutine runs `claude -p … "Critique…"` in the folder with timeout.
4. Codex polls `job_status`; each call blocks ≤50s (< tool_timeout_sec=120 set
   by the installer).
5. On finish: parse JSON → reply + session_id; result text stored.
6. Codex reads reply, replies with `chat_start(…, session_id=<id>, message=
   "I disagree because…")`; server runs `claude -p --resume <id> …`.

## 6. Error Handling

- Unknown agent / unable to infer → tool error listing valid agents.
- Backend binary not found → job fails with `exec` error text and a hint about
  `agents.<a>.command` and macOS PATH.
- CLI non-zero exit → job result is error, includes stderr (as today).
- Parse failure (no reply found) → error with last 2 KB of stdout.
- Timeout → `[<agent> was killed after … timeout]` marker (as today).
- Bad `session_id` → whatever the CLI reports, surfaced verbatim.
- Duplicate running job → same refusal message as today, including the
  existing job_id.

## 7. Codex Installation (`scripts/install-codex.sh`)

1. Reuse launch.sh's download+checksum logic (factored into a shared
   `scripts/lib.sh` sourced by both) to place the binary at
   `~/.cache/hostrunner/hostrunner-<ver>-<os>-<arch>`; symlink
   `~/.local/bin/hostrunner`. `--local <path>` flag skips the download and uses
   a locally built binary (for development).
2. Write/replace a `[mcp_servers.hostrunner]` block in `~/.codex/config.toml`
   with `command`, `tool_timeout_sec = 120`, `startup_timeout_sec = 20`
   (idempotent: replaces an existing block delimited by marker comments).
3. Copy `skills/peer-review` and `skills/peer-chat` into `~/.agents/skills/`.
4. Create `~/.config/hostrunner/config.yaml` from the example if absent.
5. Print next steps (edit allowed_roots, restart Codex app).

The Claude plugin (`.claude-plugin/`, `.mcp.json`, `launch.sh`) is unchanged
apart from also bundling the two new skills.

## 8. Skills

- `peer-review`: agent-neutral copy of today's codex-loop flow using
  `review_start` / `job_status`, no `agent` param by default (server infers),
  explicit `agent` only when the user names one.
- `peer-chat`: start with `chat_start`, poll `job_status`, keep `session_id`,
  continue for as many rounds as the user wants or until agreement; summarise
  both positions at the end. Instructs the GUI agent that the peer is
  read-only and cannot edit.
- `codex-loop`: keep, but switch examples to `review_start(agent="codex")` /
  `job_status` and note the old names still work.

Skills are written once and installed into both hosts (Claude plugin
`skills/`, Codex `~/.agents/skills/`); the frontmatter format is shared.

## 9. Testing

Unit (`go test ./...`, no real CLIs):
- `codex.ChatArgs` / `claude.ChatArgs` argv tables incl. resume and extra args.
- `codex.ParseChat` on JSONL fixtures (normal, no agent_message, garbage).
- `claude.ParseChat` on JSON fixtures (normal, `is_error`, invalid JSON).
- `agent.ReviewPrompt` scope handling and validation errors.
- `agent.Default` inference table.
- Config: legacy `codex_extra_args` merge, defaults, absolute command paths.
- Job store: duplicate key per (root, agent, kind); concurrent review+chat
  allowed.
- Server handlers with a fake `Runner` returning canned stdout: review (both
  agents), chat new + resume, alias tools produce identical results.

Manual E2E (documented in README "Verify" section):
- Claude Desktop → `review_start` (codex) and `chat_start` (codex) two rounds.
- Codex app → `review_start` (claude) and `chat_start` (claude) two rounds.

## 10. Versioning / Release

- Bump to `0.3.0` (plugin.json, server version string, README).
- goreleaser unchanged; installer downloads tag `v0.3.0`.
