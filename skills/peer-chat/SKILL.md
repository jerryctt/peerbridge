---
name: peer-chat
description: >
  Hold a multi-turn discussion with the other vendor's coding agent (Codex when
  you are Claude, Claude when you are Codex) about a design, a bug, or a plan,
  using the peerbridge `chat_start` / `job_status` tools. Use when the user says
  "ask codex/claude what it thinks", "discuss this with the other agent",
  "debate this design", "get a second opinion", or "what would codex/claude do".
---

# peer-chat

## What this skill does

Runs a conversation between you (the agent in the GUI) and a peer agent that
can read the same repository read-only. You drive the rounds; the server only
relays messages and keeps the peer's session alive.

1. `chat_start(folder, message)` — opens a new conversation. Returns `job_id`.
2. `job_status(job_id)` — poll until done. The result starts with
   `conversation_id: <id>` followed by `--- <Agent> reply ---`.
3. To continue, call `chat_start(folder, message, conversation_id="<id>")`. The
   peer remembers the earlier turns. Always use `conversation_id`, not the old
   `session_id` name: some hosts (Claude Desktop / Cowork) drop an argument
   called `session_id`, and the turn silently starts a new conversation. If the
   start message says "a new conversation" although you passed an id, the id
   was lost — re-state the context.
4. Stop when the user's question is answered, both sides agree, or the user
   asked for a fixed number of rounds. Then summarize both positions and your
   recommendation.

The peer is read-only: it can inspect files (codex can also run commands in
its own read-only sandbox), nothing else. If an edit is needed, you make it.
Tell it which files or diff to look at — a claude peer has no shell, so say
"see internal/http/client.go" rather than "run git diff".

## Critical: always use the HOST path

`folder` must be the absolute path on the user's machine
(`/Users/yourname/code/myproject`), never a `/sessions/...` sandbox path.

## Writing good messages

The peer sees only the repo and this conversation, not your chat with the user.
In the first message include: what the user is trying to do, what you changed
or propose (file paths help), and the specific question. Example:

```
chat_start(
  folder="/Users/you/proj",
  message="We're adding retry logic to internal/http/client.go (see the uncommitted diff). I propose exponential backoff with jitter, max 5 attempts, retrying on 5xx and connection errors only. Do you see correctness or production risks? Be specific and cite lines."
)
```

Follow-ups should respond to the peer's points, not restart:

```
chat_start(
  folder="/Users/you/proj",
  conversation_id="<from job_status>",
  message="On your point 2: the idempotency concern applies only to POST /orders; GETs are safe. Does that change your recommendation? Anything else blocking?"
)
```

## Polling

`job_status` blocks up to ~50s and returns either the reply or
`status: running`; keep calling it with the same `job_id`. A reply typically
takes 30s–3min because the peer reads the repo first. Do not start another
`chat_start` for the same folder and agent while one is running.

## When it fails

- `<Agent> chat failed (<agent> exit N)` with a stderr section: the peer CLI is
  not installed, not logged in, or rejected a flag — report that to the user.
- An unknown or expired `conversation_id` surfaces as a CLI error; start a new
  conversation and re-state the context.
- `cannot infer which agent to use`: pass `agent="codex"` or `agent="claude"`.

## Tips

- Pass `model` / `effort` (low, medium, high, xhigh, max) only when the user
  asks for a specific model or depth. They apply to that turn only; repeat them
  on each `chat_start` if the whole discussion should use them.
- Pass `agent` only when the user names a specific peer; otherwise the server
  picks the other vendor automatically.
- Keep each message focused on one or two points; long monologues get
  shallow answers.
- A `conversation_id` from a review result works here too: use it to ask the
  reviewer about its findings ("what exactly is wrong in finding 2?").
- To have the peer implement what you agreed on, pass the same
  `conversation_id` to `task_start` (if enabled; see the peer-task skill).
- Finished results stay retrievable via `job_status` for ~30 minutes.
