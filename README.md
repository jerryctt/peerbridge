English | [繁體中文](README_zh.md)

# peerbridge

[![CI](https://github.com/jerryctt/peerbridge/actions/workflows/ci.yml/badge.svg)](https://github.com/jerryctt/peerbridge/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Local MCP server + plugin for **Claude** and **Codex**: from either app, ask the *other* vendor's agent for a read-only code review or a multi-turn discussion of your code, running on your machine with your own CLI logins. Optionally (off by default), let the peer implement a change in a separate git worktree that never touches your checkout.

> **v0.7.0: opt-in `task_start`, `conversation_id`.** `task_start` (enable with `tasks: {enabled: true}`) lets the peer edit files in a git worktree peerbridge creates; see [Tasks](#task_start-opt-in). The continuation parameter is now `conversation_id` (`session_id` still works, but Claude Desktop/Cowork drops an argument with that name, so multi-turn chats from there silently started over). Reviews now return a `conversation_id` too, so you can ask follow-up questions or hand the findings to a task. Codex reviews run as `codex exec review --json --ignore-user-config`: your codex `config.toml` (including `review_model`) no longer applies to reviews; use the `model` / `effort` parameters or `agents.codex.model` / `.effort` instead.

> **v0.5.0: `run_command` removed.** peerbridge no longer runs arbitrary allowlisted commands; it only starts the peer CLIs with server-fixed, read-only arguments. Every tool is now read-only. `allowed_commands` in `config.yaml` is ignored (a warning is logged); you can delete it.

> **Renamed from `hostrunner-mcp` (v0.3.2).** The old GitHub URL redirects. If you installed the previous version: the binary, MCP server, plugin and config directory are now called `peerbridge`. Your existing `~/.config/hostrunner/config.yaml` and `HOSTRUNNER_CONFIG` are still read until you move them; re-running `scripts/install-codex.sh` / `scripts/install.ps1` replaces the old `[mcp_servers.hostrunner]` block with `[mcp_servers.peerbridge]`; in Claude Desktop re-install the plugin as **peerbridge** (and remove the old **hostrunner** plugin), or update `claude_desktop_config.json` if you registered the binary by hand. Permission rules change accordingly (`mcp__plugin_peerbridge_peerbridge__*`). The MCP tool names (`review_start`, `chat_start`, `job_status`, `run_command`) are unchanged.

---

## Purpose / Why

Coding agents live in their own apps. [Claude](https://claude.ai) (Desktop / Cowork / Code) edits code inside a sandbox that cannot run your host's `codex` CLI; the Codex app cannot ask Claude anything. Yet a second opinion from a *different* model is one of the cheapest ways to catch bugs and blind spots.

**peerbridge** bridges the gap in both directions:

- It runs as a **native process on your Mac or Linux machine**, spawned over stdio by Claude Desktop/Code **or** by Codex (CLI, macOS app, IDE extension).
- It has access to your host filesystem and to your `codex` and `claude` binaries with their existing logins.
- The agent in the GUI edits files; peerbridge lets it call the **peer** agent (`codex` from Claude, `claude` from Codex) to **review** the changes or to **discuss** a design over several rounds — always read-only.
- This gives a tight **edit → peer review → edit** loop, or a **debate** between two models, without leaving your editor.
- Optionally, the peer can **implement** something itself (`task_start`): codex and claude can even work side by side, each in its own worktree, and review each other's result.

---

## How It Works

```
┌────────────────────────────┐        ┌────────────────────────────┐
│  Claude Desktop / Code     │        │  Codex app / CLI / IDE     │
│  (agent edits files)       │        │  (agent edits files)       │
│   review_start / chat_start│        │   review_start / chat_start│
└──────────────┬─────────────┘        └──────────────┬─────────────┘
               │ stdio (MCP)                         │ stdio (MCP)
               ▼                                     ▼
┌───────────────────────────────────────────────────────────────────┐
│  HOST MACHINE — peerbridge (native process, one binary)           │
│                                                                   │
│  peer = the other vendor (inferred from the MCP client name)      │
│                                                                   │
│   agent=codex  → codex exec review … / codex exec --sandbox read-only│
│   agent=claude → claude -p --strict-mcp-config --permission-mode dontAsk │
│                         ▼                                         │
│                 git repo  /Users/…/proj   (read-only for peers)    │
└───────────────────────────────────────────────────────────────────┘
```

---

## Features

- **`review_start` / `job_status`** — read-only review of uncommitted changes, a base-branch diff, or a commit, by a peer agent: `codex` runs `codex review`; `claude` runs `claude -p` with a review prompt. Reviews run as **background jobs** because they outlast MCP per-call timeouts (Claude Desktop ~180s, Codex `tool_timeout_sec`); start/status calls are fast, the review itself may run up to your configured `timeout`.
- **`chat_start` / `job_status`** — send one message to the peer, which can read the repo; the result carries a `conversation_id` to continue the conversation (`claude -p --resume`, `codex exec resume`). Your GUI agent drives the rounds. Reviews return a `conversation_id` as well, so a review can be followed up in a chat or a task.
- **`task_start` (opt-in)** — the peer implements a change in a git worktree peerbridge creates under its state dir; you get its summary, the diff, a complete patch file and the branch, and decide what to apply. Continuing with the task's `conversation_id` keeps working in the same worktree.
- **Automatic peer selection** — omit `agent` and a Claude client talks to codex, a Codex client talks to claude. Pass `agent` to pick explicitly.
- **Read-only reviews and chats** — the claude peer gets file-reading tools only (`--tools Read,Grep,Glob`, no shell; peerbridge computes the review diff with read-only git and feeds it to the peer on stdin) and loads no user settings or MCP servers (`--setting-sources "" --strict-mcp-config`); the codex peer runs in codex's own `--sandbox read-only` with `--ignore-user-config`. Neither can reach peerbridge recursively, and your normal CLI logins keep working.
- **`codex_review_start` / `codex_review_status`** — deprecated aliases of `review_start(agent=codex)` / `job_status`; existing installs keep working.
- **Strict security** — root-directory containment, symlink-resolved path checks, fixed read-only argv for peers, no general command execution, no shell ever.
- **Model and effort per call** — `review_start` / `chat_start` take optional `model` and `effort` (`low`…`max`), with per-agent defaults in config.
- **Configurable** — allowed roots, timeout, max output size, per-agent command path, extra flags, default model and effort, task settings.
- **Audit log** — every tool invocation is logged to stderr via zerolog.

---

## Requirements

| Requirement | Notes |
|---|---|
| **codex CLI** | Needed for `agent=codex` — installed and logged in on the host |
| **claude CLI** | Needed for `agent=claude` — installed and logged in on the host (ships with Claude Code) |
| **Claude Desktop / Claude Code** *or* **Codex app / CLI** | One of them registers and spawns the MCP server (both can) |
| **Go 1.25+** | Only needed if building from source; pre-built release binaries need no Go |

---

## Installation

### Option 1: Install with `go install` (recommended if you have Go)

```bash
go install github.com/jerryctt/peerbridge/cmd/peerbridge@latest
```

The binary is placed in `$GOPATH/bin/peerbridge` (typically `~/go/bin/peerbridge`).

### Option 2: Download a release binary

Download the pre-built binary for your platform from the [Releases page](https://github.com/jerryctt/peerbridge/releases), extract it, and place `peerbridge` somewhere in your `$PATH` (e.g. `/usr/local/bin/peerbridge`; on Windows, `peerbridge.exe` anywhere you like).

Every release ships these archives (static binaries, no runtime needed):

| OS | Architectures | Archive |
|---|---|---|
| macOS | `amd64` (Intel), `arm64` (Apple silicon) | `peerbridge_<ver>_darwin_<arch>.tar.gz` |
| Linux | `amd64`, `arm64`, `386` (32-bit x86), `armv6`, `armv7` (32-bit ARM, e.g. Raspberry Pi) | `peerbridge_<ver>_linux_<arch>.tar.gz` |
| Windows | `amd64`, `arm64`, `386` | `peerbridge_<ver>_windows_<arch>.zip` |

The installers below pick the right one automatically (`uname -m` / `PROCESSOR_ARCHITECTURE`) and verify it against the release `checksums.txt`.

### Option 3: Build from source

```bash
git clone https://github.com/jerryctt/peerbridge.git
cd peerbridge
make build          # produces ./peerbridge
```

### Option 4: Install as a Claude plugin (marketplace)

**In Claude Desktop:** go to Add marketplace → enter `jerryctt/peerbridge`, then install the **peerbridge** plugin.

**In Claude Code:**
```
/plugin marketplace add jerryctt/peerbridge
```
Then install the `peerbridge` plugin from the listing.

Installing via the marketplace installs the MCP server (auto-started by the bundled launcher) and the `peer-review`, `peer-chat` and `peer-task` skills in one step.

**Prerequisite — create a config file:** the server looks for its config at `~/.config/peerbridge/config.yaml` by default. Create it from `examples/config.example.yaml` before starting Claude Desktop/Code:

```bash
mkdir -p ~/.config/peerbridge
cp examples/config.example.yaml ~/.config/peerbridge/config.yaml
# edit the file to match your paths
```

You can override the config path with the `-config` flag or the `PEERBRIDGE_CONFIG` environment variable.

> **macOS GUI apps and environment variables:** because macOS does not pass shell environment variables to GUI apps like Claude Desktop or the Codex app, setting `PEERBRIDGE_CONFIG` (or `PATH`) in your shell profile has no effect when the server is launched from a GUI. Place your config at the default path, and if `codex`/`claude` are not found set `agents.<name>.command` to an absolute path.

**macOS/Linux:** the bundled `scripts/launch.sh` launcher downloads the matching release binary on first run and verifies it against the release `checksums.txt` before executing it. No manual binary install needed.

**Windows:** the marketplace plugin's launcher is a POSIX shell script, so on Windows the plugin's MCP server will not start by itself (its skills still work). Use `scripts\install.ps1` instead — see "Install on Windows" below — which registers `peerbridge.exe` directly in `claude_desktop_config.json`.

> **Trust note:** the launcher downloads and runs a binary from GitHub Releases, checksum-verified against the release. Review the source at `scripts/launch.sh` and `scripts/lib.sh` if you have concerns.

### Install on Windows (Codex and/or Claude Desktop)

```powershell
git clone https://github.com/jerryctt/peerbridge.git
cd peerbridge
powershell -ExecutionPolicy Bypass -File scripts\install.ps1          # Codex + Claude Desktop
powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -Codex   # or -Claude for one of them
# ... -Local .\peerbridge.exe to use a locally built binary; -NoClaudeConfigWrite to only print the JSON snippet
```

Works with Windows PowerShell 5.1 and PowerShell 7. It downloads and checksum-verifies the matching zip, installs `peerbridge.exe` to `%LOCALAPPDATA%\peerbridge\`, creates `%APPDATA%\peerbridge\config.yaml` from the example, writes the Codex `[mcp_servers.peerbridge]` block to `%USERPROFILE%\.codex\config.toml` and the skills to `%USERPROFILE%\.agents\skills\`, and merges `mcpServers.peerbridge` into `%APPDATA%\Claude\claude_desktop_config.json` (a `.bak.peerbridge` copy is kept). Edit `allowed_roots` (Windows paths such as `C:/Users/you/code` are fine), then restart the apps.

Windows notes: `codex` and `claude` are usually `.cmd` shims from npm — if the GUI app cannot find them, set `agents.codex.command` / `agents.claude.command` to the full path printed by `(Get-Command claude).Source`. On a timeout peerbridge kills the whole peer process tree with `taskkill /T`.

### Install for Codex (CLI, macOS app, IDE extension)

```bash
git clone https://github.com/jerryctt/peerbridge.git
cd peerbridge
scripts/install-codex.sh            # or: scripts/install-codex.sh --local ./peerbridge
```

This downloads and verifies the release binary (links it at `~/.local/bin/peerbridge`), writes a `[mcp_servers.peerbridge]` block to `~/.codex/config.toml` (with `tool_timeout_sec = 120`), installs the `peer-review`, `peer-chat` and `peer-task` skills into `~/.agents/skills/`, and creates `~/.config/peerbridge/config.yaml` if missing. Edit `allowed_roots`, then restart the Codex app (the CLI, the macOS app and the IDE extension share this configuration). In Codex, say "peer review my changes" or "ask claude what it thinks about this design".

Manual equivalent:

```toml
# ~/.codex/config.toml
[mcp_servers.peerbridge]
command = "/Users/you/.local/bin/peerbridge"
tool_timeout_sec = 120
startup_timeout_sec = 20
```

---

## Configuration

The server resolves its config file in this order:
1. The path given by the `-config` flag, if provided.
2. The `PEERBRIDGE_CONFIG` environment variable, if set.
3. The first of these that exists, otherwise the first one: `~/.config/peerbridge/config.yaml` (all platforms), then the OS user config dir — `%APPDATA%\peerbridge\config.yaml` on Windows, `~/Library/Application Support/peerbridge/config.yaml` on macOS.

**Recommended:** place your config at `~/.config/peerbridge/config.yaml`. GUI apps do not inherit shell environment variables, so `PEERBRIDGE_CONFIG` set in your shell profile will not be visible to a server launched from Claude Desktop or the Codex app.

Create a config file from the provided example — **the paths in it are placeholders; change them to match your own setup**:

```bash
mkdir -p ~/.config/peerbridge
cp examples/config.example.yaml ~/.config/peerbridge/config.yaml
```

```yaml
# Absolute host paths that tools are allowed to access.
# The tool rejects any request that resolves outside these roots.
allowed_roots:
  - /Users/yourname/code

# Maximum wall-clock time for a single job on the host (a review or one chat
# turn). Must include a unit (e.g. 600s, 10m). Jobs run in
# the background, so this can be much larger than the MCP client's per-call
# limit.
timeout: 600s

# Maximum bytes returned from a single tool invocation (output is truncated with a notice).
max_output_bytes: 200000

# How long job_status waits before answering "running". Longer = fewer polls =
# fewer permission prompts. Must stay below the client's per-call timeout
# (Codex tool_timeout_sec: 120 via our installers; Claude Desktop ~180s).
status_poll: 100s

# Tee peer CLI output to the server's stderr in real time (default: true).
# Claude Desktop captures this in ~/Library/Logs/Claude/mcp-server-peerbridge.log.
stream_output: true

# Peer agents. Both optional; defaults shown. Use absolute paths when the GUI
# app cannot find the CLI on its PATH (e.g. the output of `which claude`).
agents:
  codex:
    command: codex
    extra_args: []              # e.g. ["--search"]
    # model: gpt-5.5            # default when a call passes no model
    # effort: medium            # low | medium | high | xhigh | max
  claude:
    command: claude
    extra_args: []
    # model: opus
    # effort: high

# Legacy, still honoured and appended to agents.codex.extra_args:
# codex_extra_args: ["-c", "model=o3"]

# task_start: let a peer edit files in a separate git worktree. Off by default.
# tasks:
#   enabled: true
#   timeout: 30m        # per task turn
#   max_running: 2      # concurrently running tasks

# Where task worktrees, patches and the task index live.
# Default: $XDG_STATE_HOME/peerbridge, else ~/.local/state/peerbridge
# state_dir: /Users/yourname/.local/state/peerbridge
```

> **Note:** the config file is read **once at server startup**. After editing `config.yaml`, fully restart Claude Desktop or the Codex app (they respawn the server) for changes to take effect. The effective values are logged at startup (look for the `config loaded` line — it shows the config path, timeout and agent commands actually in use).

---

## Register with Claude Desktop

Add the following to your Claude Desktop config (usually `~/Library/Application Support/Claude/claude_desktop_config.json` on macOS):

```json
{
  "mcpServers": {
    "peerbridge": {
      "command": "/usr/local/bin/peerbridge",
      "args": ["-config", "/Users/yourname/.config/peerbridge/config.yaml"]
    }
  }
}
```

Replace `/usr/local/bin/peerbridge` with the actual path to the binary (e.g. `~/go/bin/peerbridge`), and update the config path accordingly. The file `examples/claude_desktop_config.example.json` in this repo shows the same snippet.

Restart Claude Desktop after editing the config. You should see **peerbridge** appear in the MCP servers list.

---

## Available Tools

| Tool | Purpose |
|---|---|
| `review_start(folder, agent?, scope?, base?, commit?, prompt?)` | Start a read-only review by a peer agent; returns `job_id`. |
| `chat_start(folder, message, agent?, model?, effort?, conversation_id?)` | Send one message to the peer; returns `job_id`. Pass the `conversation_id` from an earlier result to continue that conversation. |
| `task_start(folder, message, agent?, model?, effort?, conversation_id?, base?)` | **Opt-in.** The peer implements a change in a separate git worktree; returns `job_id`. |
| `job_status(job_id)` | Long-poll (≤50s) for a job's result. |
| `codex_review_start`, `codex_review_status` | Deprecated aliases of `review_start(agent=codex)` / `job_status`. |

All `folder` parameters are absolute **HOST** paths (e.g. `/Users/you/proj`), never `/sessions/…` sandbox paths.

### `review_start`

| Parameter | Type | Required | Description |
|---|---|---|---|
| `folder` | string | yes | Absolute host path to the git repo |
| `agent` | string | no | `codex` or `claude`. Omit to use the other vendor automatically (Claude client → codex, Codex client → claude) |
| `scope` | string | no | `uncommitted` (default — staged+unstaged+untracked), `base` (against a base branch), or `commit` (a specific commit) |
| `base` | string | no | Base branch or ref; required when `scope=base` (e.g. `main`) |
| `commit` | string | no | Commit SHA; required when `scope=commit` |
| `prompt` | string | no | Custom review instructions (e.g. `"focus on error handling"`). Only for an explicitly focused review. Safe with any `scope`. |
| `model` | string | no | Peer model (see [Model and effort](#model-and-effort)) |
| `effort` | string | no | `low`, `medium`, `high`, `xhigh` or `max` (see [Model and effort](#model-and-effort)) |

`agent=codex` runs `codex exec review --json` (native: codex computes the diff itself) in a read-only sandbox without your codex config. `agent=claude` runs `claude -p` with a review prompt; peerbridge computes the diff with hardened read-only git and passes it on stdin. The finished result includes a `conversation_id` for follow-ups (`chat_start` / `task_start`); each `review_start` itself always starts a fresh review.

**Returns:**

```
Review started in the background.
job_id: 3fa4c19e02d1
agent: codex
mode: uncommitted
folder: /Users/you/proj
```

### `chat_start`

| Parameter | Type | Required | Description |
|---|---|---|---|
| `folder` | string | yes | Absolute host path the peer may read |
| `message` | string | yes | What to say. Include the context the peer needs — it only sees the repo and this conversation. |
| `agent` | string | no | `codex` or `claude`; omit for automatic peer selection |
| `conversation_id` | string | no | `conversation_id` from an earlier `job_status` result (chat, review or task), to continue that conversation |
| `session_id` | string | no | Deprecated alias of `conversation_id`. Claude Desktop/Cowork strips an argument with this name, so use `conversation_id` |
| `model` | string | no | Peer model for this turn |
| `effort` | string | no | Reasoning effort for this turn: `low`, `medium`, `high`, `xhigh` or `max` |

### Model and effort

`model` and `effort` pick the peer's model and reasoning depth for one call, e.g. `review_start(folder=…, agent="claude", model="opus", effort="high")`.

- **claude** gets `--model <model> --effort <effort>`; `model` is an alias (`opus`, `sonnet`, …) or a full model id.
- **codex** gets `-c model="<model>"` (plus `-c review_model="<model>"` for reviews) and `-c model_reasoning_effort="<effort>"`.
- Precedence: the call's value, else `agents.<name>.model` / `.effort` in `config.yaml`, else the CLI's own default. Each call resolves afresh; when continuing a chat, an omitted value is not re-sent and the CLI decides what to use.
- Which efforts a model supports is up to the vendor CLI; an unsupported combination fails with the CLI's own error.
- If `extra_args` already sets a model or effort (e.g. `["-m", "o3"]`), passing one too is rejected rather than guessing which flag wins: move it to `model:` / `effort:` in config.

### `task_start` (opt-in)

Lets the peer **edit files** — but only inside a git worktree peerbridge creates for the task. Your checkout is never touched and nothing is committed. Disabled unless `config.yaml` has `tasks: {enabled: true}`; when disabled the tool is not even listed.

| Parameter | Type | Required | Description |
|---|---|---|---|
| `folder` | string | yes | Absolute host path inside the git repo (must be under `allowed_roots`) |
| `message` | string | yes | What to implement, with scope and acceptance criteria |
| `agent` | string | no | `codex` or `claude`; omit for automatic peer selection |
| `model`, `effort` | string | no | As for chat (see [Model and effort](#model-and-effort)) |
| `conversation_id` | string | no | From an earlier **task**: continue in that task's worktree. From a **chat or review**: continue that conversation in a new worktree |
| `base` | string | no | Branch, tag or commit a new task starts from (default `HEAD`). Your uncommitted changes are not included |

How it works:

1. peerbridge creates `peerbridge/task-<id>` as a worktree under `<state_dir>/worktrees/`, with hooks disabled and every filter driver neutralised — including drivers that only the worktree's own config context enables (`includeIf onbranch:` / `gitdir:`).
2. The peer runs with the worktree as its working directory:
   - **codex** — `codex exec --sandbox workspace-write` with network off and `/tmp` excluded; its `$TMPDIR` is a per-job directory. It can run commands (tests, builds) inside that sandbox.
   - **claude** — `claude -p --restricted --permission-mode acceptEdits --tools Read,Grep,Glob,Edit,Write`: file tools confined to the worktree, **no shell**, so it cannot run tests.
3. peerbridge stages everything in the worktree (`git add -A`, via the git dir it recorded at creation — it never trusts the worktree's own `.git` file) and saves a complete binary patch to `<state_dir>/patches/`.
4. The result has the peer's summary, a diffstat, the diff, the patch path, the worktree and branch, the `conversation_id`, and copy-paste commands to apply (`git apply --index <patch>`, or commit in the worktree and merge the branch) and to clean up (`git worktree remove`). It is returned even when the peer fails or times out, so partial work is never lost. Each turn gets its own patch file. When the patch cannot carry everything (staging failed, output cap hit, or the change touches a submodule / nested repository, whose contents a patch can't hold), the result says **INCOMPLETE** and offers no apply or cleanup commands: recover from the worktree instead.

Verified on macOS (codex 0.166, claude 2.1.318): writes outside the worktree, in `$HOME` and (for claude) in `/tmp` are denied; the peer cannot `git commit`; codex has no network. These are **write** restrictions: like reviews, a task peer can still *read* files outside the repo.

Concurrency: one running turn per conversation (across chat, review follow-ups and tasks), one task per worktree — enforced with a file lock, so also across peerbridge processes (e.g. Claude Desktop and Codex both running one) — and at most `tasks.max_running` tasks per process. To review a task's work, `review_start(folder=<worktree>)` (the worktree root printed in the result) is accepted even though the worktree is outside `allowed_roots`, as long as it is a recorded task worktree of an allowed repo whose `.git` still points where peerbridge created it. Tasks in different worktrees of the same repo may run together — e.g. codex and claude each implementing the same request, then reviewing each other's branch.

### `job_status`

Blocks up to ~50s waiting for completion, then returns either the finished result or a `running` notice — call it again until it completes. Finished results stay retrievable for ~30 minutes.

| Parameter | Type | Required | Description |
|---|---|---|---|
| `job_id` | string | yes | The id returned by `review_start` / `chat_start` / `task_start` |

**Returns (review finished):**

```
status: completed (job 3fa4c19e02d1, agent codex, kind review, elapsed 4m12s)

Mode: uncommitted (codex exit 0)
conversation_id: 01a1baf7-…
session_id: 01a1baf7-…

--- Codex review ---
… findings …
```

**Returns (chat finished):**

```
status: completed (job 9b1d…, agent claude, kind chat, elapsed 1m05s)

conversation_id: 6c2f0a4e-…
session_id: 6c2f0a4e-…

--- Claude reply ---
… the peer's answer …
```

**Returns (still running):**

```
status: running (job 3fa4c19e02d1, agent codex, kind review, elapsed 1m40s)
Still in progress. Call job_status again with the same job_id.
```

Example discussion driven from Codex (peer = claude):

```
chat_start(folder="/Users/you/proj", message="Critique the retry design in the uncommitted diff.")
job_status(job_id="…")   → conversation_id: abc…  --- Claude reply --- …
chat_start(folder="/Users/you/proj", conversation_id="abc…", message="Point 2 only applies to POST; does that change your view?")
```

---

## Permission prompts

Every host asks before running an MCP tool unless you tell it not to. A review is one `review_start` plus several `job_status` polls, so with the defaults you get a prompt every ~50 seconds. Three knobs:

- **Fewer polls.** `status_poll` in `config.yaml` is how long `job_status` waits before answering "running". The example sets `100s` (safe with the `tool_timeout_sec = 120` our installers write and with Claude Desktop's ~180s); the built-in default is `50s`. It must stay below the client's per-call timeout.
- **Codex.** The installers write `default_tools_approval_mode = "writes"` into the `[mcp_servers.peerbridge]` block: Codex then only asks for tools that are *not* marked read-only. `review_start`, `chat_start` and `job_status` carry the MCP `readOnlyHint` annotation (peers never modify anything), so they run without prompts. `task_start` (when enabled) is not read-only, so Codex asks before each task; set the mode to `"prompt"` if you want to approve every call.
- **Claude.** In Claude Desktop / Cowork pick **Always allow** on the first prompt for each tool (it is remembered per tool). In Claude Code add a permission rule to `~/.claude/settings.json`: `{"permissions": {"allow": ["mcp__peerbridge__*"]}}` — for the marketplace plugin the server is named `plugin_peerbridge_peerbridge`, so use `"mcp__plugin_peerbridge_peerbridge__*"`. Claude Code does not yet act on `readOnlyHint` for MCP tools, hence the explicit rule. The wildcard also covers `task_start` when tasks are enabled; to keep a prompt for tasks, list the read-only tools instead: `"mcp__plugin_peerbridge_peerbridge__review_start"`, `…__chat_start`, `…__job_status`.

## Skills

The repository ships three skills under `skills/`, in the shared `SKILL.md` format that both Claude (plugin) and Codex (`~/.agents/skills/`) read:

- **`peer-review`** — edit → peer review → edit loop with `review_start` / `job_status`; agent-neutral (works from either host, reviewing with codex or claude).
- **`peer-chat`** — multi-round discussion with `chat_start` / `job_status`, keeping the `conversation_id`.
- **`peer-task`** — hand an implementation to the peer with `task_start` (when enabled), review the result, and apply or iterate.

The former Claude-only `codex-loop` skill has been removed: `peer-review` covers it (say "review my changes with codex" to pin the peer to codex).

The Claude marketplace plugin and `scripts/install-codex.sh` install them automatically. Trigger them by saying things like "peer review my changes", "ask claude/codex what it thinks about this", "review my changes with codex", or "have codex implement this".

---

## Usage

A typical **edit → peer review → edit** session, from either host:

1. **Open your project** in Claude (share the folder with the Cowork session) or in Codex.
2. **Edit files** with the agent's own tools.
3. **Start a review.** `review_start(folder="/Users/yourname/code/yourproject")` — the host path, not a `/sessions/…` path. The server picks the other vendor's agent and returns a `job_id`.
4. **Poll** `job_status` until `status: completed`. Each poll blocks up to ~50s.
5. **Iterate.** Fix the findings, start a new review, repeat until clean.

For a **discussion**, use `chat_start` with a focused message, read the reply from `job_status`, and continue with the returned `conversation_id`.

For **delegation** (with tasks enabled): `task_start(folder=…, message="implement X")`, review the returned diff (or `review_start` the task's worktree), then apply the patch or continue with `task_start(conversation_id=…, message="also handle Y")`. To compare approaches, start one task with `agent="codex"` and one with `agent="claude"`.

---

## Security

- **Root-directory containment.** Every path argument is resolved (including symlinks) and checked against `allowed_roots`.
- **No shell.** Commands are run as argv arrays via `os/exec`. There is no `sh -c`, no glob expansion, no shell injection.
- **Read-only reviews and chats.** Peer argv is fixed by the server. codex uses `codex exec review` / `codex exec` with `--ignore-user-config` and a read-only sandbox (OS-level). claude has no sandbox, so it gets no shell at all: `-p --strict-mcp-config --setting-sources "" --permission-mode dontAsk --tools Read,Grep,Glob --allowedTools Read,Grep,Glob --disallowedTools Edit,Write,NotebookEdit,Bash`. For claude reviews peerbridge runs `git status`/`git diff`/`git show` itself and pipes the result to the peer on stdin — read-only and hardened: refs are validated (never option-like), `--no-ext-diff --no-textconv`, `core.fsmonitor=false`, `core.hooksPath=/dev/null`, global/system git config ignored (`GIT_CONFIG_GLOBAL=/dev/null` etc.) and `GIT_OPTIONAL_LOCKS=0`, and `--ignore-submodules=dirty` (git never runs inside a submodule, whose own config could define filters; submodule pointer changes are still shown, uncommitted edits inside a submodule are not), so no diff helpers, filters or hooks run and nothing is written. Only `agents.<name>.command`, `extra_args`, and the validated `model` / `effort` come from config or the caller (models are restricted to letters, digits and `. _ : / -`, effort to a fixed list).
- **No inherited permissions, no recursion.** `--setting-sources ""` keeps the user's own Claude permission rules and hooks out of the peer (`--allowedTools` is additive to settings, so this makes it the whole list); `--strict-mcp-config` (claude) and `--ignore-user-config` (codex) start the peer with none of the user's MCP servers, so it cannot load peerbridge itself. `--bare` is deliberately not used: it disables OAuth/keychain auth and would break subscription logins. `-c mcp_servers={}` was rejected: config overrides merge, they do not replace.
- **Tasks are opt-in and confined.** `task_start` is off by default. A task peer writes only inside a server-created worktree under `state_dir` (codex: `workspace-write` sandbox with network off and a private `$TMPDIR`; claude: `--restricted`, file tools only, no shell), cannot commit, loads no MCP servers or user settings, and nothing it does reaches your checkout until you apply the patch. Worktrees are created with hooks and filter drivers disabled, and peerbridge's own git calls in a worktree use the git dir recorded at creation, never the (peer-writable) `.git` file. Writes are restricted; reads outside the repo are not.
- **No general command execution.** There is no tool that runs an arbitrary command: the only processes peerbridge starts are the peer CLIs with the fixed argv above and its own hardened read-only git. (`run_command` was removed in v0.5.0: even with an allowlist, `claude`/`codex`/`git` accept arguments that bypass the read-only design.)
- **Native host process, not Docker.** Running in a container would break access to the host CLIs and their logins.
- **Host paths only.** Any path beginning with `/sessions/` is rejected with a hint.

---

## Development

```bash
make build   # Build the binary
make test    # Run all tests
make vet     # Vet the code
```

---

## License

MIT — see [LICENSE](LICENSE).
