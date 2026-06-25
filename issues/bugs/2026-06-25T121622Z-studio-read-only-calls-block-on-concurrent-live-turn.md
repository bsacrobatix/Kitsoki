---
id: 2026-06-25T121622Z-studio-read-only-calls-block-on-concurrent-live-turn
title: "studio MCP read-only calls (studio.handles / session.status / studio.ping) hang while another connection's live session.drive turn is in flight — cheap introspection should never serialize behind a long LLM turn"
target: kitsoki
filed_at: 2026-06-25T12:16:22Z
status: open
severity: P2
component: mcp
kitsoki_rev: 9c9eac6b
trace_ref: ""
external: {}
assignee: ""
url: "issues/bugs/2026-06-25T121622Z-studio-read-only-calls-block-on-concurrent-live-turn.md"
---

## Body

`studio.handles` (a read-only "list the open handles" call) **hung / was very
slow** when called on the studio server while a SEPARATE connection was in the
middle of a long-running live `session.drive` turn (a gpt-5.5 bugfix dogfood
maker phase, which can run for minutes). The handles list should be effectively
instant — it just enumerates open handles — so blocking it behind another
connection's in-flight LLM turn is a real concurrency defect.

This almost certainly affects all the cheap read-only studio calls the
kitsoki-mcp-driver agent is told to "lead with" — `studio.ping`,
`studio.handles`, `session.status`, `session.world` — which is exactly when
they're most needed (checking on a long autonomous run from a second client).
The whole value of those overflow-proof reads is that they answer immediately; if
a single global lock is held for the duration of a `session.drive` turn, they
don't.

### Likely cause

A coarse, server-wide mutex (or single-threaded request loop) held across the
entire `session.drive` turn — including the synchronous host.agent.task / LLM
subprocess wait — so every other MCP request (even pure reads on a different
handle) queues behind it. Read-only introspection (`studio.handles`,
`session.status`, `session.world` snapshot, `studio.ping`) should take a
read-lock / not contend with an in-flight turn on a *different* handle, or the
per-session work should not hold a server-global lock while waiting on the model.

### Steps to reproduce

1. From connection A: `session.new {harness: live, profile: codex-native}` and
   `session.drive`/`session.submit` a turn that triggers a long LLM phase (a
   bugfix maker run — minutes).
2. From connection B (or the same client, concurrently): call `studio.handles`
   (or `session.status`/`studio.ping`).
3. The call B hangs / is very slow until the live turn on A completes, instead of
   returning immediately.

### Expected vs actual

**Expected:** read-only studio calls return promptly regardless of an in-flight
live turn on another handle/connection — they are the supported way to monitor a
long autonomous run.

**Actual:** they block until the concurrent `session.drive` turn finishes.

### Severity rationale

P2: no incorrect results, but it defeats the entire "cheap reads to monitor a
long run" workflow (and the lean-driver guidance that depends on it), and makes a
second client effectively unusable while any live turn is running. Concurrency /
locking defect in the studio server.

### Files involved

- `internal/mcp/studio/` — the request dispatch + any server-global lock held
  across a `session.drive` turn; the read-only handlers (`studio.handles`,
  `session.status`, `session.world`, `studio.ping`) should not contend with it.
- the session-runtime turn path (`session_runtime.go`) — where the long
  host.agent.task / LLM wait happens under whatever lock is held.
</content>
