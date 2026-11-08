---
name: peer-task
description: >
  Delegate an implementation to the other vendor's coding agent (Codex when you
  are Claude, Claude when you are Codex) with the peerbridge `task_start` tool:
  the peer edits files in a separate git worktree, you review its diff and
  apply it or ask for changes. Use when the user says "have codex/claude
  implement this", "let the other agent write it", "delegate this to codex",
  "ask claude to fix finding 2", or "have both agents try it and compare".
---

# peer-task

## What this skill does

1. `task_start(folder, message)` — the peer implements `message` in a new git
   worktree that peerbridge creates. Returns `job_id`.
2. `job_status(job_id)` — poll until done. The result has the peer's summary,
   `--- changes ---` (diffstat), `--- diff ---`, and these lines:
   `conversation_id`, `worktree`, `branch`, `patch`.
3. You review the diff critically (it is unverified work), then:
   - accept → apply it to the user's checkout (see "Applying");
   - change it → `task_start(folder, message="...", conversation_id="<id>")`
     continues in the **same worktree** with the same conversation.
4. Tell the user what changed and where it is. Never claim the change is in
   their checkout until it has been applied.

`task_start` exists only when the user enabled it (`tasks: {enabled: true}` in
peerbridge's config.yaml). If the tool is missing, say so and offer
`chat_start` (discuss) or doing the change yourself instead. Do not ask the
user to enable it unless they want delegation.

## Critical: always use the HOST path

`folder` must be the absolute path on the user's machine
(`/Users/yourname/code/myproject`), never a `/sessions/...` sandbox path.

## What the peer can and cannot do

- It edits files only inside the task worktree. The user's checkout is never
  touched and nothing is committed.
- codex runs in a `workspace-write` sandbox with no network; it can run
  commands (tests, builds) inside the worktree.
- claude has file tools only (no shell): it **cannot run tests**. The result
  says so; run or ask for verification yourself.
- The worktree starts from `HEAD` (or `base`). The user's **uncommitted
  changes are not in it**; the result warns when the checkout is dirty.

## Writing the message

The peer sees only the repo and this conversation. State the goal, the files
or area, constraints, and how to verify:

```
task_start(
  folder="/Users/you/proj",
  message="Add exponential backoff with jitter to internal/http/client.go: max 5 attempts, retry only on 5xx and connection errors, never on POST. Add table tests in client_test.go and run `go test ./internal/http/`. Do not change public APIs."
)
```

From a review or chat you already had, pass its `conversation_id` so the peer
keeps that context (it gets a new worktree):

```
task_start(folder="/Users/you/proj", conversation_id="<from the review>",
           message="Fix finding 2 from your review, nothing else.")
```

## Applying

The result prints ready-made commands. Prefer, in order:

1. **Patch** (exact, includes new and binary files):
   `git -C <repo> apply --index <patch>`
2. **Branch**: commit on the task branch with the printed command (it names
   the git dir explicitly, so a `.git` file the peer tampered with is not
   followed), then `git -C <repo> merge <branch>`. If the result warns that the
   worktree's `.git` file changed, use only the printed commands.

Only apply when the user asked you to or agreed; otherwise report the patch
path and branch. After applying (or abandoning), clean up with the printed
`git worktree remove ... && git branch -D ...`.

## Two agents side by side

To compare approaches, start one task with `agent="codex"` and one with
`agent="claude"` using the same message; each gets its own worktree. When both
finish, compare the diffs, optionally `review_start(folder=<worktree>)` each
other's result, and recommend one to the user.

## When it fails

- `Task FAILED: ...` still lists the worktree, patch and conversation_id:
  partial work is kept. Inspect it, then continue with `conversation_id` or
  start over.
- `task_start is disabled` → tasks are off in config (see above).
- `already has a running ... turn` / `already working in` / `max_running` →
  wait for the running job (poll its job_id) instead of starting another.
- `COULD NOT COLLECT CHANGES` → look in the worktree directly with git.

## Tips

- One task = one coherent change. Split big work into several turns on the
  same conversation_id rather than one huge message.
- Pass `model` / `effort` only when the user asks for them.
- Tasks can take many minutes; keep polling `job_status` with the same job_id.
