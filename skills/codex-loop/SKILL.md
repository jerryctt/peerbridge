---
name: codex-loop
description: >
  Run a codex review on my latest edits. Use when the user says things like
  "review my changes with codex", "run codex on my edits", "start an edit-review loop",
  "check my code with codex", or "do a codex review". After each round of file edits,
  calls the review_start MCP tool (agent=codex) with the connected folder's HOST path,
  polls job_status until the result is ready, summarizes the findings, and iterates
  until the review is clean.
---

# codex-loop skill

## What this skill does

After you (or the user) have edited files in the connected project folder, this skill:

1. Calls the `review_start` MCP tool with `agent = "codex"` and the project's **host path** — it returns a `job_id` immediately while the review runs in the background on the host.
2. Polls `job_status` with that `job_id` until the review completes.
3. Summarizes the issues and suggests fixes.
4. Applies fixes using Read/Write/Edit tools.
5. Starts a new review to verify the fixes.
6. Repeats until the review reports no issues.

Reviews run as background jobs because they often take several minutes — longer than the MCP client's ~180s per-tool-call limit. Each start/status call itself returns quickly, so the loop never hits that limit.

## Critical: always use the HOST path

The `folder` parameter to `review_start` must be the **host machine path** — the path you would use in a terminal on the user's Mac or Linux machine.

**Correct:**
```
review_start(
  agent  = "codex",
  folder = "/Users/yourname/code/myproject"
)
```

**WRONG — never pass a sandbox path:**
```
review_start(
  agent  = "codex",
  folder = "/sessions/abc123/mnt/myproject"   <- REJECTED
)
```

Determine the folder's HOST path from the user (e.g. the path they shared when connecting the folder, such as `/Users/yourname/code/myproject`). Never derive it from the sandbox mount path. If you are unsure, ask the user: "What is the absolute path to this project on your machine?"

## Step-by-step instructions

### Step 1 — Identify the host path

Ask the user for the absolute host path if it is not already known:

> "What is the absolute path to the project on your host machine? (e.g. /Users/yourname/code/myproject)"

### Step 2 — Confirm there are uncommitted changes

You can optionally run:

```
run_command(
  command = "git",
  args    = ["status", "--short"],
  folder  = "/Users/yourname/code/myproject"
)
```

If the output is empty there is nothing to review yet. Tell the user and wait for more edits.

### Step 3 — Start the review

```
review_start(
  agent  = "codex",
  folder = "/Users/yourname/code/myproject"
)
```

`scope` defaults to `uncommitted` (staged + unstaged + untracked) — do not pass it unless you need a different scope:

For a branch diff against a base ref (e.g. `main`):
```
review_start(
  agent  = "codex",
  folder = "/Users/yourname/code/myproject",
  scope  = "base",
  base   = "main"
)
```

For a specific commit:
```
review_start(
  agent  = "codex",
  folder = "/Users/yourname/code/myproject",
  scope  = "commit",
  commit = "<SHA>"
)
```

The result contains a `job_id` — note it for the next step.

#### `prompt`: focused reviews only — do NOT pass it by default

**Only pass `prompt` when the user explicitly asks to narrow or focus the review** (e.g. "focus on security", "only review the concurrency changes"). For a normal review, omit it entirely. Do not restate the scope in the prompt (e.g. never `prompt = "review my uncommitted changes"`) — the scope parameter already covers that.

```
review_start(
  agent  = "codex",
  folder = "/Users/yourname/code/myproject",
  prompt = "focus on error handling and resource cleanup"
)
```

It is safe to combine `prompt` with any `scope`: the codex CLI forbids scope flags alongside a prompt, so the server folds the scope into the prompt text for you.

### Step 4 — Poll for the result

```
job_status(
  job_id = "<job_id from step 3>"
)
```

Each call blocks up to ~50s waiting for completion, then returns either the finished review or `status: running`. If it is still running, call `job_status` again with the same `job_id` — keep polling until it completes. Reviews typically take 1–10 minutes; do not give up or restart the review just because a few polls returned `running`.

### Step 5 — Summarize and fix

The verdict is the text under `--- Codex review ---` in the completed status result. **Make the next-step decision by reading that text — not by the exit code.** `codex review` always exits 0, even when it lists problems.

- **If the verdict lists concrete issues or risks** → summarize them as a numbered list, apply fixes using the Edit tool (or Read then Write for larger changes), then re-review (Step 6).
- **If the verdict says it found no issues, looks clean, or approves the changes** → STOP. Report to the user that the review is clean. Do not loop again.

> **Note on the tool result:** the result contains only codex's verdict, not its progress/exec trace. The full live trace is in the desktop log `~/Library/Logs/Claude/mcp-server-hostrunner.log` (when `stream_output` is enabled, which is the default). If the result header shows a non-zero `codex exit N` (e.g. `Mode: uncommitted (codex exit 1)`), codex itself failed — a `--- codex stderr ---` section will be included. Treat that as a tool error to report to the user, not as review findings. A `[codex was killed after the server-side timeout ...]` marker means the review hit the server's `timeout` config; suggest the user raise it (and restart Claude Desktop afterwards).

### Step 6 — Re-review

After applying all fixes, call `review_start` again with the same parameters (a fresh review needs a fresh job), and poll as in Step 4. If the review returns no issues or only informational notes, the loop is complete. Tell the user the review is clean.

### Step 7 — Repeat if needed

If new issues appear in the re-review, repeat from Step 5.

## Tips

- The old names `codex_review_start` / `codex_review_status` still work but are deprecated aliases of `review_start(agent="codex")` / `job_status`. For an agent-neutral version of this loop (works in Codex too, reviewing with Claude) use the `peer-review` skill.

- The default scope `uncommitted` covers staged, unstaged, and untracked changes — `codex review` computes the diff itself.
- One review = one `review_start` + repeated `job_status` polls. Never call `review_start` again while a job for the same folder is still running.
- If the tool returns an error like `"got a sandbox path"`, you passed a `/sessions/...` path. Stop and ask the user for the correct host path.
- If the output is marked `[output truncated]`, the diff is very large. Consider asking the user to scope the review to fewer files or review a specific commit instead.
- Finished job results stay retrievable via `job_status` for ~30 minutes.
