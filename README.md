English | [繁體中文](README_zh.md)

# peerbridge

[![CI](https://github.com/jerryctt/peerbridge/actions/workflows/ci.yml/badge.svg)](https://github.com/jerryctt/peerbridge/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Local MCP server + plugin for **Claude** and **Codex**: from either app, ask the *other* vendor's agent for a read-only code review or a multi-turn discussion of your code, running on your machine with your own CLI logins.

> **Renamed from `hostrunner-mcp` (v0.3.2).** The old GitHub URL redirects. If you installed the previous version: the binary, MCP server, plugin and config directory are now called `peerbridge`. Your existing `~/.config/hostrunner/config.yaml` and `HOSTRUNNER_CONFIG` are still read until you move them; re-running `scripts/install-codex.sh` / `scripts/install.ps1` replaces the old `[mcp_servers.hostrunner]` block with `[mcp_servers.peerbridge]`; in Claude Desktop re-install the plugin as **peerbridge** (and remove the old **hostrunner** plugin), or update `claude_desktop_config.json` if you registered the binary by hand. Permission rules change accordingly (`mcp__plugin_peerbridge_peerbridge__*`). The MCP tool names (`review_start`, `chat_start`, `job_status`, `run_command`) are unchanged.

---

## Purpose / Why

Coding agents live in their own apps. [Claude](https://claude.ai) (Desktop / Cowork / Code) edits code inside a sandbox that cannot run your host's `codex` CLI; the Codex app cannot ask Claude anything. Yet a second opinion from a *different* model is one of the cheapest ways to catch bugs and blind spots.

**peerbridge** bridges the gap in both directions:

- It runs as a **native process on your Mac or Linux machine**, spawned over stdio by Claude Desktop/Code **or** by Codex (CLI, macOS app, IDE extension).
- It has access to your host filesystem and to your `codex` and `claude` binaries with their existing logins.
- The agent in the GUI edits files; peerbridge lets it call the **peer** agent (`codex` from Claude, `claude` from Codex) to **review** the changes or to **discuss** a design over several rounds — always read-only.
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
│  HOST MACHINE — peerbridge (native process, one binary)           │
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
- **Read-only peers** — the claude peer gets file-reading tools only (`--tools Read,Grep,Glob`, no shell; peerbridge computes the review diff with read-only git and feeds it to the peer on stdin) and loads no user settings or MCP servers (`--setting-sources "" --strict-mcp-config`); the codex peer runs in codex's own `--sandbox read-only` with `--ignore-user-config`. Neither can reach peerbridge recursively, and your normal CLI logins keep working.
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

Installing via the marketplace installs the MCP server (auto-started by the bundled launcher) and the `peer-review` and `peer-chat` skills in one step.

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

This downloads and verifies the release binary (links it at `~/.local/bin/peerbridge`), writes a `[mcp_servers.peerbridge]` block to `~/.codex/config.toml` (with `tool_timeout_sec = 120`), installs the `peer-review` and `peer-chat` skills into `~/.agents/skills/`, and creates `~/.config/peerbridge/config.yaml` if missing. Edit `allowed_roots`, then restart the Codex app (the CLI, the macOS app and the IDE extension share this configuration). In Codex, say "peer review my changes" or "ask claude what it thinks about this design".

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

## Permission prompts

Every host asks before running an MCP tool unless you tell it not to. A review is one `review_start` plus several `job_status` polls, so with the defaults you get a prompt every ~50 seconds. Three knobs:

- **Fewer polls.** `status_poll` in `config.yaml` is how long `job_status` waits before answering "running". The example sets `100s` (safe with the `tool_timeout_sec = 120` our installers write and with Claude Desktop's ~180s); the built-in default is `50s`. It must stay below the client's per-call timeout.
- **Codex.** The installers write `default_tools_approval_mode = "writes"` into the `[mcp_servers.peerbridge]` block: Codex then only asks for tools that are *not* marked read-only. `review_start`, `chat_start` and `job_status` carry the MCP `readOnlyHint` annotation (peers never modify anything), so they run without prompts; `run_command` still asks. Set it to `"approve"` to never ask, or override one tool with `[mcp_servers.peerbridge.tools.run_command] approval_mode = "approve"`.
- **Claude.** In Claude Desktop / Cowork pick **Always allow** on the first prompt for each tool (it is remembered per tool). In Claude Code add a permission rule to `~/.claude/settings.json`: `{"permissions": {"allow": ["mcp__peerbridge__*"]}}` — for the marketplace plugin the server is named `plugin_peerbridge_peerbridge`, so use `"mcp__plugin_peerbridge_peerbridge__*"`. Claude Code does not yet act on `readOnlyHint` for MCP tools, hence the explicit rule.

## Skills

The repository ships two skills under `skills/`, in the shared `SKILL.md` format that both Claude (plugin) and Codex (`~/.agents/skills/`) read:

- **`peer-review`** — edit → peer review → edit loop with `review_start` / `job_status`; agent-neutral (works from either host, reviewing with codex or claude).
- **`peer-chat`** — multi-round discussion with `chat_start` / `job_status`, keeping the `session_id`.

The former Claude-only `codex-loop` skill has been removed: `peer-review` covers it (say "review my changes with codex" to pin the peer to codex).

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
- **Read-only peers.** Peer argv is fixed by the server. codex uses `codex review` / `codex exec --sandbox read-only --ignore-user-config` (OS-level sandbox). claude has no sandbox, so it gets no shell at all: `-p --strict-mcp-config --setting-sources "" --permission-mode dontAsk --tools Read,Grep,Glob --allowedTools Read,Grep,Glob --disallowedTools Edit,Write,NotebookEdit,Bash`. For claude reviews peerbridge runs `git status`/`git diff`/`git show` itself and pipes the result to the peer on stdin — read-only and hardened: refs are validated (never option-like), `--no-ext-diff --no-textconv`, `core.fsmonitor=false`, `core.hooksPath=/dev/null`, global/system git config ignored (`GIT_CONFIG_GLOBAL=/dev/null` etc.) and `GIT_OPTIONAL_LOCKS=0`, so no diff helpers, filters or hooks run and nothing is written. Only `agents.<name>.command` and `extra_args` come from config; `allowed_commands` applies to `run_command`.
- **No inherited permissions, no recursion.** `--setting-sources ""` keeps the user's own Claude permission rules and hooks out of the peer (`--allowedTools` is additive to settings, so this makes it the whole list); `--strict-mcp-config` (claude) and `--ignore-user-config` (codex) start the peer with none of the user's MCP servers, so it cannot load peerbridge itself. `--bare` is deliberately not used: it disables OAuth/keychain auth and would break subscription logins. `-c mcp_servers={}` was rejected: config overrides merge, they do not replace.
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

---

## License

MIT — see [LICENSE](LICENSE).
