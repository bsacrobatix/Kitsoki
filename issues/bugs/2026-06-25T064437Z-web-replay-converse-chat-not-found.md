---
id: 2026-06-25T064437Z-web-replay-converse-chat-not-found
title: "host.agent.converse fails 'chat not found' under `kitsoki web --harness replay` — prior-art room shows a bogus 'scan failed' banner"
target: kitsoki
filed_at: 2026-06-25T06:44:37Z
status: open
severity: P2
component: host
kitsoki_rev: 1162ac96
trace_ref: ""
external: {}
assignee: ""
url: "issues/bugs/2026-06-25T064437Z-web-replay-converse-chat-not-found.md"
---

## Body

Under `kitsoki web --harness replay` (with a host cassette), `host.agent.converse`
fails with `get chat <id>: chats: chat not found`. In the slidey-dev / dev-story
PRD walk this sets `world.last_error` during PRD discovery, and the downstream
prior-art **search** room then renders a bogus banner:

    ⚠ Prior-art scan failed — host.agent.converse: get chat chat-1: chats: chat not found

The overlap scan itself succeeded ("No overlapping work found"); the banner is
pure collateral from the failed converse call. Any conversation-driven demo or
flow that runs `host.agent.converse` through the web replay harness hits this.

### Root cause

`internal/host/agent_converse.go` runs a **real** chat-store lookup
`cs.Get(ctx, chatID)` — only the agent *dispatch* (the model call) is mocked
under replay. `chatID` (e.g. `chat-1`) is the id returned by `host.chat.resolve`.
But under the **web replay harness**, `host.chat.resolve` is satisfied from the
host cassette episode (returns `chat_id: chat-1`) **without running the real
resolve handler**, so the chat is never created in the store. converse's real
`cs.Get(chat-1)` then fails (`internal/chats/store.go` → `ErrChatNotFound`).

### Why it passes under `kitsoki test flows` but fails under `kitsoki web`

The flow `stories/slidey-dev/flows/pm_idea.yaml` host_handlers and the web cassette
`stories/slidey-dev/assets/pm_idea-host.cassette.yaml` are **identical** for
`chat.resolve` / `converse` (both return `chat-1`), yet `kitsoki test flows …
pm_idea.yaml` passes 3/3. So the two replay harnesses intercept host calls
**differently**:

- `test flows` — the stub fully REPLACES `host.agent.converse` → no real
  `cs.Get`, no chat needed.
- `kitsoki web --harness replay` — runs the REAL converse handler with only the
  agent dispatch mocked → real `cs.Get` → needs `chat-1` to exist → fails.

That asymmetry is the bug.

### Steps to reproduce

1. `make build-bin`
2. `./bin/kitsoki web --stories-dir stories/slidey-dev --harness replay \
      --recording stories/slidey-dev/assets/pm_idea-recording.yaml \
      --host-cassette stories/slidey-dev/assets/pm_idea-host.cassette.yaml \
      --addr 127.0.0.1:7799 --db /tmp/x.db`
3. Drive the PRD discovery turn (type the idea), then advance toward the
   prior-art search room.
4. The search room shows "⚠ Prior-art scan failed — host.agent.converse: get
   chat chat-1: chats: chat not found".

### Expected vs actual

**Expected:** under the web replay harness, a cassette-stubbed `host.chat.resolve`
followed by `host.agent.converse` succeeds — converse returns the stubbed answer
without a "chat not found" error, and `world.last_error` stays empty.

**Actual:** converse's real `cs.Get` fails because the stubbed resolve never
created the chat; `world.last_error` is set and surfaced as a "scan failed"
banner.

### Proposed fix sketch

1. **Preferred** — unify the two replay-harness host-call interception paths so
   `test flows` and `kitsoki web --harness replay` can't diverge (removes the
   whole class of "passes in flows, fails in web").
2. Or — when a cassette episode matches a side-effecting handler
   (`host.chat.resolve`), still run the real handler's side effect (create the
   chat) and only override the returned data — i.e. cassette = response override,
   not handler bypass, for chat-store-mutating handlers.
3. Or (narrowest) — under replay, have `host.agent.converse` create-on-miss for
   the chat, mirroring what resolve would have done.

Add a regression test that drives `host.agent.converse` through the web replay
harness with a cassette-stubbed `host.chat.resolve` and asserts no "chat not
found" error (RED before the fix, GREEN after).

### Severity rationale

P2: a real error surfaced on a user-visible surface in every web-replay
conversation demo, but with a working alternate path (`test flows`) and no
state-machine correctness loss. The story is correctly EXPOSING the runtime issue
(per `stories/AGENTS.md`) — fix the runtime, not the story.

### Files involved

- `internal/host/agent_converse.go` — the real `cs.Get` on the converse path.
- `internal/host/chat_handlers.go` — `host.chat.resolve` create vs stub.
- the web replay-harness host-call interception (vs the `test flows` path) —
  where the two diverge.
- `internal/chats/store.go` — `ErrChatNotFound`.
</content>
