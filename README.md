English | [繁體中文](README_zh.md)

# hostrunner-mcp

[![CI](https://github.com/jerryctt/hostrunner-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/jerryctt/hostrunner-mcp/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Local MCP server + plugin for **Claude** and **Codex**: from either app, ask the *other* vendor's agent for a read-only code review or a multi-turn discussion of your code, running on your machine with your own CLI logins.

---

## Purpose / Why

Coding agents live in their own apps. [Claude](https://claude.ai) (Desktop / Cowork / Code) edits code inside a sandbox that cannot run your host's `codex` CLI; the Codex app cannot ask Claude anything. Yet a second opinion from a *different* model is one of the cheapest ways to catch bugs and blind spots.

**hostrunner-mcp** bridges the gap in both directions:

- It runs as a **native process on your Mac or Linux machine**, spawned over stdio by Claude Desktop/Code **or** by Codex (CLI, macOS app, IDE extension).
- It has access to your host filesystem and to your `codex` and `claude` binaries with their existing logins.
- The agent in the GUI edits files; hostrunner lets it call the **peer** agent (`codex` from Claude, `claude` from Codex) to **review** the changes or to **discuss** a design over several rounds — always read-only.
- This gives a tight **edit → peer review → edit** loop, or a **debate** between two models, without leaving your editor.

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
│  HOST MACHINE — hostrunner (native process, one binary)           │
│                                                                   │
│  peer = the other vendor (inferred from the MCP client name)      │
│                                                                   │
│   agent=codex  → codex review …  /  codex exec --sandbox read-only│
│   agent=claude → claude -p --strict-mcp-config --permission-mode dontAsk │
│                         ▼                                         │
│                 git repo  /Users/…/proj   (read-only for peers)    │
└───────────────────────────────────────────────────────────────────┘
```

---

## Features

- **`review_start` / `job_status`** — read-only review of uncommitted changes, a base-branch diff, or a commit, by a peer agent: `codex` runs `codex review`; `claude` runs `claude -p` with a review prompt. Reviews run as **background jobs** because they outlast MCP per-call timeouts (Claude Desktop ~180s, Codex `tool_timeout_sec`); start/status calls are fast, the review itself may run up to your configured `timeout`.
- **`chat_start` / `job_status`** — send one message to the peer, which can read the repo; the result carries a `session_id` to continue the conversation (`claude -p --resume`, `codex exec resume`). Your GUI agent drives the rounds.
- **Automatic peer selection** — omit `agent` and a Claude client talks to codex, a Codex client talks to claude. Pass `agent` to pick explicitly.
- **Read-only peers** — the claude peer gets file-reading tools only (`--tools Read,Grep,Glob`, no shell; hostrunner computes the review diff with read-only git and feeds it to the peer on stdin) and loads no user settings or MCP servers (`--setting-sources "" --strict-mcp-config`); the codex peer runs in codex's own `--sandbox read-only` with `--ignore-user-config`. Neither can reach hostrunner recursively, and your normal CLI logins keep working.
- **`codex_review_start` / `codex_review_status`** — deprecated aliases of `review_start(agent=codex)` / `job_status`; existing installs keep working.
- **`run_command`** — run any allowlisted CLI (e.g. `git`) in a host folder, as an argv array (never a shell).
- **Strict security** — root-directory containment, symlink-resolved path checks, fixed read-only argv for peers, command allowlist for `run_command`, no shell ever.
- **Configurable** — allowed roots, allowed commands, timeout, max output size, per-agent command path and extra flags.
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
go install github.com/jerryctt/hostrunner-mcp/cmd/hostrunner@latest
```

The binary is placed in `$GOPATH/bin/hostrunner` (typically `~/go/bin/hostrunner`).

### Option 2: Download a release binary

Download the pre-built binary for your platform from the [Releases page](https://github.com/jerryctt/hostrunner-mcp/releases), extract it, and place `hostrunner` somewhere in your `$PATH` (e.g. `/usr/local/bin/hostrunner`).

### Option 3: Build from source

```bash
git clone https://github.com/jerryctt/hostrunner-mcp.git
cd hostrunner-mcp
make build          # produces ./hostrunner
```

### Option 4: Install as a Claude plugin (marketplace)

**In Claude Desktop:** go to Add marketplace → enter `jerryctt/hostrunner-mcp`, then install the **hostrunner** plugin.

**In Claude Code:**
```
/plugin marketplace add jerryctt/hostrunner-mcp
```
Then install the `hostrunner` plugin from the listing.

Installing via the marketplace installs the MCP server (auto-started by the bundled launcher) and the `peer-review`, `peer-chat` and `codex-loop` skills in one step.

**Prerequisite — create a config file:** the server looks for its config at `~/.config/hostrunner/config.yaml` by default. Create it from `examples/config.example.yaml` before starting Claude Desktop/Code:

```bash
mkdir -p ~/.config/hostrunner
cp examples/config.example.yaml ~/.config/hostrunner/config.yaml
# edit the file to match your paths
```

You can override the config path with the `-config` flag or the `HOSTRUNNER_CONFIG` environment variable.

> **macOS GUI apps and environment variables:** because macOS does not pass shell environment variables to GUI apps like Claude Desktop or the Codex app, setting `HOSTRUNNER_CONFIG` (or `PATH`) in your shell profile has no effect when the server is launched from a GUI. Place your config at the default path, and if `codex`/`claude` are not found set `agents.<name>.command` to an absolute path.

**macOS/Linux:** the bundled `scripts/launch.sh` launcher downloads the matching release binary on first run and verifies it against the release `checksums.txt` before executing it. No manual binary install needed.

**Windows:** the launcher is not supported on Windows. Install the binary manually (Option 2 above) and register it via `claude_desktop_config.json` as described in the "Register with Claude Desktop" section.

> **Trust note:** the launcher downloads and runs a binary from GitHub Releases, checksum-verified against the release. Review the source at `scripts/launch.sh` and `scripts/lib.sh` if you have concerns.

### Install for Codex (CLI, macOS app, IDE extension)

```bash
git clone https://github.com/jerryctt/hostrunner-mcp.git
cd hostrunner-mcp
scripts/install-codex.sh            # or: scripts/install-codex.sh --local ./hostrunner
```

This downloads and verifies the release binary (links it at `~/.local/bin/hostrunner`), writes a `[mcp_servers.hostrunner]` block to `~/.codex/config.toml` (with `tool_timeout_sec = 120`), installs the `peer-review` and `peer-chat` skills into `~/.agents/skills/`, and creates `~/.config/hostrunner/config.yaml` if missing. Edit `allowed_roots`, then restart the Codex app (the CLI, the macOS app and the IDE extension share this configuration). In Codex, say "peer review my changes" or "ask claude what it thinks about this design".

Manual equivalent:

```toml
# ~/.codex/config.toml
[mcp_servers.hostrunner]
command = "/Users/you/.local/bin/hostrunner"
tool_timeout_sec = 120
startup_timeout_sec = 20
```

---

## Configuration

The server resolves its config file in this order:
1. The path given by the `-config` flag, if provided.
2. The `HOSTRUNNER_CONFIG` environment variable, if set.
3. The default `~/.config/hostrunner/config.yaml`.

**Recommended:** place your config at `~/.config/hostrunner/config.yaml`. GUI apps do not inherit shell environment variables, so `HOSTRUNNER_CONFIG` set in your shell profile will not be visible to a server launched from Claude Desktop or the Codex app.

Create a config file from the provided example — **the paths in it are placeholders; change them to match your own setup**:

```bash
mkdir -p ~/.config/hostrunner
cp examples/config.example.yaml ~/.config/hostrunner/config.yaml
```

```yaml
# Absolute host paths that tools are allowed to access.
# The tool rejects any request that resolves outside these roots.
allowed_roots:
  - /Users/yourname/code

# CLI executables that run_command may invoke (peer agents do not use this
# list; their argv is fixed by the server).
allowed_commands:
  - codex
  - claude
  - git

# Maximum wall-clock time for a single command on the host (a review, one chat
# turn, or a run_command). Must include a unit (e.g. 600s, 10m). Jobs run in
# the background, so this can be much larger than the MCP client's per-call
# limit.
timeout: 600s

# Maximum bytes returned from a single tool invocation (output is truncated with a notice).
max_output_bytes: 200000

# Tee peer CLI output to the server's stderr in real time (default: true).
# Claude Desktop captures this in ~/Library/Logs/Claude/mcp-server-hostrunner.log.
stream_output: true

# Peer agents. Both optional; defaults shown. Use absolute paths when the GUI
# app cannot find the CLI on its PATH (e.g. the output of `which claude`).
agents:
  codex:
    command: codex
    extra_args: []              # e.g. ["-c", "model=o3"]
  claude:
    command: claude
    extra_args: []              # e.g. ["--model", "opus"]

# Legacy, still honoured and appended to agents.codex.extra_args:
# codex_extra_args: ["-c", "model=o3"]
```

> **Note:** the config file is read **once at server startup**. After editing `config.yaml`, fully restart Claude Desktop or the Codex app (they respawn the server) for changes to take effect. The effective values are logged at startup (look for the `config loaded` line — it shows the config path, timeout and agent commands actually in use).

---

## Register with Claude Desktop

Add the following to your Claude Desktop config (usually `~/Library/Application Support/Claude/claude_desktop_config.json` on macOS):

```json
{
  "mcpServers": {
    "hostrunner": {
      "command": "/usr/local/bin/hostrunner",
      "args": ["-config", "/Users/yourname/.config/hostrunner/config.yaml"]
    }
  }
}
```

Replace `/usr/local/bin/hostrunner` with the actual path to the binary (e.g. `~/go/bin/hostrunner`), and update the config path accordingly. The file `examples/claude_desktop_config.example.json` in this repo shows the same snippet.

Restart Claude Desktop after editing the config. You should see **hostrunner** appear in the MCP servers list.

---

## Available Tools

| Tool | Purpose |
|---|---|
| `review_start(folder, agent?, scope?, base?, commit?, prompt?)` | Start a read-only review by a peer agent; returns `job_id`. |
| `chat_start(folder, message, agent?, session_id?)` | Send one message to the peer; returns `job_id`. Pass the `session_id` from the previous reply to continue the same conversation. |
| `job_status(job_id)` | Long-poll (≤50s) for a job's result. |
| `codex_review_start`, `codex_review_status` | Deprecated aliases of `review_start(agent=codex)` / `job_status`. |
| `run_command(command, args, folder)` | Allowlisted argv in a host folder. |

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

`agent=codex` runs `codex review` (native, computes the diff itself). `agent=claude` runs `claude -p` with a review prompt that tells it to inspect the changes with read-only git commands.

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
| `session_id` | string | no | `session_id` from a previous `job_status` result, to continue that conversation |

### `job_status`

Blocks up to ~50s waiting for completion, then returns either the finished result or a `running` notice — call it again until it completes. Finished results stay retrievable for ~30 minutes.

| Parameter | Type | Required | Description |
|---|---|---|---|
| `job_id` | string | yes | The id returned by `review_start` / `chat_start` |

**Returns (review finished):**

```
status: completed (job 3fa4c19e02d1, agent codex, kind review, elapsed 4m12s)

Mode: uncommitted (codex exit 0)

--- Codex review ---
… findings …
```

**Returns (chat finished):**

```
status: completed (job 9b1d…, agent claude, kind chat, elapsed 1m05s)

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
job_status(job_id="…")   → session_id: abc…  --- Claude reply --- …
chat_start(folder="/Users/you/proj", session_id="abc…", message="Point 2 only applies to POST; does that change your view?")
```

### `run_command`

Run an allowlisted command (argv, no shell) in a host folder.

| Parameter | Type | Required | Description |
|---|---|---|---|
| `command` | string | yes | Executable name; must be in `allowed_commands` |
| `args` | string[] | no | Arguments as a string array |
| `folder` | string | yes | Absolute **HOST** path inside an `allowed_root` |

**Returns:** exit code, elapsed time, stdout, and stderr.

---

## Skills

The repository ships three skills under `skills/`, in the shared `SKILL.md` format that both Claude (plugin) and Codex (`~/.agents/skills/`) read:

- **`peer-review`** — edit → peer review → edit loop with `review_start` / `job_status`; agent-neutral (works from either host).
- **`peer-chat`** — multi-round discussion with `chat_start` / `job_status`, keeping the `session_id`.
- **`codex-loop`** — the original Claude-side loop, now using `review_start(agent="codex")`.

The Claude marketplace plugin and `scripts/install-codex.sh` install them automatically. Trigger them by saying things like "peer review my changes", "ask claude/codex what it thinks about this", or "review my changes with codex".

---

## Usage

A typical **edit → peer review → edit** session, from either host:

1. **Open your project** in Claude (share the folder with the Cowork session) or in Codex.
2. **Edit files** with the agent's own tools.
3. **Start a review.** `review_start(folder="/Users/yourname/code/yourproject")` — the host path, not a `/sessions/…` path. The server picks the other vendor's agent and returns a `job_id`.
4. **Poll** `job_status` until `status: completed`. Each poll blocks up to ~50s.
5. **Iterate.** Fix the findings, start a new review, repeat until clean.

For a **discussion**, use `chat_start` with a focused message, read the reply from `job_status`, and continue with the returned `session_id`.

---

## Security

- **Root-directory containment.** Every path argument is resolved (including symlinks) and checked against `allowed_roots`.
- **No shell.** Commands are run as argv arrays via `os/exec`. There is no `sh -c`, no glob expansion, no shell injection.
- **Read-only peers.** Peer argv is fixed by the server. codex uses `codex review` / `codex exec --sandbox read-only --ignore-user-config` (OS-level sandbox). claude has no sandbox, so it gets no shell at all: `-p --strict-mcp-config --setting-sources "" --permission-mode dontAsk --tools Read,Grep,Glob --allowedTools Read,Grep,Glob --disallowedTools Edit,Write,NotebookEdit,Bash`. For claude reviews hostrunner runs `git status`/`git diff`/`git show` itself (read-only, output-capped) and pipes the result to the peer on stdin (refs are validated so they can never be parsed as git options). Only `agents.<name>.command` and `extra_args` come from config; `allowed_commands` applies to `run_command`.
- **No inherited permissions, no recursion.** `--setting-sources ""` keeps the user's own Claude permission rules and hooks out of the peer (`--allowedTools` is additive to settings, so this makes it the whole list); `--strict-mcp-config` (claude) and `--ignore-user-config` (codex) start the peer with none of the user's MCP servers, so it cannot load hostrunner itself. `--bare` is deliberately not used: it disables OAuth/keychain auth and would break subscription logins. `-c mcp_servers={}` was rejected: config overrides merge, they do not replace.
- **Command allowlist** for `run_command`.
- **Native host process, not Docker.** Running in a container would break access to the host CLIs and their logins.
- **Host paths only.** Any path beginning with `/sessions/` is rejected with a hint.

---

## Development

```bash
make build   # Build the binary
make test    # Run all tests
make vet     # Vet the code
```

Design docs live in `docs/superpowers/specs/`.

---

## License

MIT — see [LICENSE](LICENSE).
