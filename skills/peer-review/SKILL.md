---
name: peer-review
description: >
  Ask the other vendor's coding agent (Codex when you are Claude, Claude when
  you are Codex) for a read-only code review of the current changes via the
  hostrunner MCP tools, then fix what it finds and re-review until clean. Use
  when the user says "peer review", "get a second opinion on my changes",
  "have codex/claude review this", "review loop", or "cross-review".
---

# peer-review

## What this skill does

1. Calls `review_start` with the project's **host path**. The server picks the
   peer agent automatically (the other vendor); only pass `agent` when the user
   names one explicitly ("have codex review it").
2. Polls `job_status` with the returned `job_id` until it completes.
3. Summarizes the findings, applies fixes with your own edit tools.
4. Starts a new review to verify, and repeats until the review is clean.

The peer is strictly read-only: it can read files in the repo (and codex can
run commands inside its own read-only sandbox), but it cannot edit. For a
claude peer, hostrunner computes the diff with read-only git and embeds it in
the prompt. All edits are yours.

Reviews run as background jobs because they often take several minutes —
longer than an MCP client's per-tool-call limit. Each start/status call
returns quickly, so the loop never hits that limit.

## Critical: always use the HOST path

`folder` must be the absolute path on the user's machine, e.g.
`/Users/yourname/code/myproject`. Never a sandbox path such as
`/sessions/.../mnt/myproject` (the server rejects those). If unsure, ask:
"What is the absolute path to this project on your machine?"

## Steps

### 1. Confirm there is something to review (optional)

```
run_command(command="git", args=["status", "--short"], folder="/Users/you/proj")
```

Empty output means nothing to review yet.

### 2. Start the review

```
review_start(folder="/Users/you/proj")
```

Options:
- `scope="base", base="main"` — review the branch against `main`.
- `scope="commit", commit="<SHA>"` — review one commit.
- `agent="codex"` / `agent="claude"` — only when the user asked for a specific peer.
- `prompt="focus on error handling"` — only when the user asked to narrow the review. Never restate the scope in the prompt.

The result contains `job_id` and `agent`.

### 3. Poll

```
job_status(job_id="<job_id>")
```

Each call blocks up to ~50s and returns either the finished result or
`status: running`. Keep polling with the same `job_id`; reviews take 1–10
minutes. Do not start a second review for the same folder while one is running
(the server refuses and tells you the running job_id).

### 4. Read the verdict

The verdict is the text under `--- Codex review ---` or `--- Claude review ---`.
Decide from that text, **not** the exit code (`codex review` exits 0 even when
it lists problems).

- Concrete issues or risks → summarize as a numbered list, fix them with your
  edit tools, then go to step 5.
- "No issues" / clean / approved → STOP and tell the user the review is clean.

A non-zero `(<agent> exit N)` with a `--- <agent> stderr ---` section means the
peer CLI itself failed (not installed, not logged in, bad flag): report it as a
tool error. A `[<agent> was killed after the server-side timeout ...]` marker
means the server's `timeout` config is too low for this repo.

### 5. Re-review

After fixing, call `review_start` again with the same parameters (a fresh job)
and poll as in step 3. Repeat from step 4 until clean.

## Tips

- The old tool names `codex_review_start` / `codex_review_status` still exist
  but are deprecated; prefer `review_start` / `job_status`.
- `[output truncated]` means the diff is very large; suggest reviewing a single
  commit or a narrower scope.
- Finished results stay retrievable via `job_status` for ~30 minutes.
