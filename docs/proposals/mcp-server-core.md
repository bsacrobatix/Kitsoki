# Runtime: `kitsoki mcp` — the studio server core

**Status:** Draft v1. Nothing implemented yet.
**Kind:**   runtime
**Epic:**   [mcp-studio.md](mcp-studio.md) (slice 5)

## Why

kitsoki can already *be* an MCP server — `kitsoki serve` exposes one
`transition` tool over `modelcontextprotocol/go-sdk` (`internal/mcp/server.go:86`),
and the operator-ask bridge proves the attach machinery
(`writeMCPConfigTempfile`, `internal/host/oracle_helpers.go:49`;
`mcp__<server>__<tool>` naming, `operator_ask_bridge.go:40`). But `serve` drives
*one app's transitions* — it has no concept of an authoring workspace, multiple
live sessions, or rendering. An external coding agent that wants to **author a
story, drive a session, and see the result** needs a server whose state is those
things, not a single machine.

This slice is the keystone of the facade: the `kitsoki mcp` server, the **handle
model** every tool operates on, the tool registry, and the no-LLM default — but
**no domain tools yet**. It ships with a trivial `studio.ping`/`studio.handles`
pair so the server, transport, attach config, and handle lifecycle are
verifiable end-to-end before slices 6/7 add the real tools.

## What changes

**One sentence:** a new `kitsoki mcp` stdio subcommand starts a
`modelcontextprotocol/go-sdk` server (sibling to `internal/mcp/server.go`) that
holds a **studio session** of named handles — driving-session handles and one
authoring-workspace handle — and registers tools against them, defaulting every
session to the no-LLM replay harness.

## Impact

- **Code seams:** new `cmd/kitsoki/mcp.go` (the `mcpCmd()` cobra command,
  registered in `newRootCmd`, `cmd/kitsoki/main.go:81`); new
  `internal/mcp/studio/` (the server, mirroring `internal/mcp/server.go`'s
  `NewServer` + `AddTool` + `StdioTransport` shape, `server.go:95-115`); the
  handle store. No engine changes — this is a host of existing APIs.
- **Vocabulary:** no story/world vocabulary, no new effects or host calls. CLI
  flags + a tool namespace only (tables below).
- **Stories affected:** none.
- **Backward compat:** purely additive. `kitsoki serve`, the operator-ask MCP
  server, and `mcp-bash`/`mcp-validator` are untouched; this is one more sibling
  subprocess server.
- **Docs on ship:** `docs/architecture/mcp-studio.md` (new, sibling to
  `operator-ask.md`).

## Vocabulary changes

CLI flags + tool namespace only — no engine vocabulary:

| Kind | Name | Shape | Notes |
|---|---|---|---|
| flag | `--stories-dir` | path | root for `story.*` workspace resolution (mirrors `kitsoki web`) |
| flag | `--db` | path | session store for driving handles (defaults to a temp DB) |
| flag | `--harness` | `replay \| live` | **default `replay`** (no LLM); per-session override on `session.new` |
| flag | `--workspace` | path | optional initial authoring workspace handle |
| tool | `studio.ping` | `{} → {ok, version}` | liveness; proves transport + attach |
| tool | `studio.handles` | `{} → {sessions[], workspace?}` | lists open handles + their modes (harness, state) |

The real `story.*` / `session.*` / `render.*` tools land in slices 6/7; this
slice defines the **namespace + registry** they plug into.

## The model

A connecting client owns one **studio session** = the server process's
in-memory state. It holds:

```
StudioSession
├── workspace: *WorkspaceHandle        // a story dir under authoring (≤1)
│     └── dir, last app.Load result (cached AppDef + ValidationErrors)
└── sessions: map[handle]*SessionHandle // keyed driving sessions (0..n)
      └── sid app.SessionID, driver OrchestratorDriver (driver.go:79),
          harness mode (replay|live + VCR), trace path
```

Everything interpretive (routing free text, any live harness call) happens
**inside** a session handle's driver and is recorded in that session's trace
exactly as `kitsoki turn --trace` records it (`turn.go:330`) — the server core
adds no new decision point. The server is pure plumbing: it owns handle
lifecycle and dispatch, and **defers every interpretive act to the existing
orchestrator + harness** (shared decision 3: replay by default).

```
client ──(stdio MCP)──▶ kitsoki mcp ──▶ tool dispatch ──▶ handle (workspace | session)
                                                              │
                              authoring → app.Load / graph / RunFlows (no LLM, slice 6)
                              driving   → orch.Turn / SubmitDirect    (slice 7, replay default)
                              render    → ComposeFrame / shot / web   (slices 1/3/4 via slice 7)
```

## Decision recording

The server core records **nothing new**. Each driving handle writes through the
same JSONL event sink as `turn --trace` (`store.OpenJSONL` + `WithEventSink`,
`turn.go:330`), so routed intents, `oracle.call.*`, and transitions land in that
session's trace and replay unchanged. The studio session itself is ephemeral
process state; its handles point at durable traces. (If a future slice wants a
"what did this agent do across handles" audit, that's a tracing concern — out of
scope here.)

## Engine seams & invariants

- **Reuse the SDK + transport verbatim:** `mcpsdk.NewServer(&Implementation{...},
  nil)` + `mcpsdk.AddTool(...)` + `srv.Run(ctx, &mcpsdk.StdioTransport{})`,
  exactly as `internal/mcp/server.go:95-115`. Tools are named bare (`story.read`,
  `session.drive`); the SDK exposes them to the client as
  `mcp__kitsoki__story.read` per the `mcp__<server>__<tool>` convention.
- **Handle resolution is fail-fast:** a tool naming an unknown session handle, or
  a `story.*` call with no workspace bound, returns a structured tool error
  (mirroring `TransitionError`, `server.go:61`) — never a panic, never a silent
  no-op (principle of least surprise).
- **No-LLM invariant (shared decision 3):** the server constructs every driving
  handle's harness as `ReplayHarness` (`replay.go:101`) unless the call
  explicitly opts into `live`. A unit test injects a failing live harness behind
  the default and asserts it is never called from a default-mode session.
- **Attach config for external clients:** a `kitsoki mcp install` helper (or
  documented snippet) emits the `{"mcpServers": {"kitsoki": {"command": "...",
  "args": ["mcp", "--stories-dir", "..."]}}}` entry via the existing
  `writeMCPConfigTempfile` shape (`oracle_helpers.go:49`) so the server drops
  into a client's `.mcp.json` the same way the bash/operator servers attach.

## Backward compatibility / migration

Additive — a new subcommand and a new `internal/mcp/studio/` package. Nothing
existing changes. The server holds no durable state of its own beyond the
session traces it opens (which are the standard JSONL traces any `kitsoki turn
--trace` produces).

## Tasks

```
## 1. Engine
- [ ] 1.1 cmd/kitsoki/mcp.go: mcpCmd() + flags; register in newRootCmd
- [ ] 1.2 internal/mcp/studio: server (NewServer/AddTool/StdioTransport, mirror server.go)
- [ ] 1.3 Handle model: StudioSession + WorkspaceHandle + SessionHandle store + lifecycle
- [ ] 1.4 studio.ping + studio.handles tools; structured tool-error helper (mirror TransitionError)
- [ ] 1.5 No-LLM default: build ReplayHarness per driving handle unless harness:live

## 2. Verification
- [ ] 2.1 Stateless: spawn the server in-process, call studio.ping → {ok, version}
- [ ] 2.2 Handle lifecycle: open/list/close handles via studio.handles; unknown handle → tool error
- [ ] 2.3 No-live-fallthrough: a default-mode handle never invokes an injected failing live harness
- [ ] 2.4 Attach config: emitted .mcp.json entry round-trips (command/args resolve to this binary)

## 3. Adopt + document
- [ ] 3.1 docs/architecture/mcp-studio.md: server + handle model + attach (sibling to operator-ask.md)
- [ ] 3.2 Update the epic slice row; unblock slices 6/7
```

## Verification

No LLM: the server boots, `studio.ping` returns, handles open/close, and a
default-mode session refuses to call a live harness (test injects a failing one).
All deterministic — the only interpretive paths (routing, live harness) belong to
slices 6/7 and are replay-gated there.

## Open questions

1. **Bare tool names vs. dotted (`story.read`).** The SDK exposes them as
   `mcp__kitsoki__<name>`; dots in `<name>` are cosmetic grouping. *Lean: keep
   the `family.verb` dotted convention for readability; confirm the SDK + clients
   accept a `.` in the tool name (fall back to `story_read` if not).*
2. **Where does the workspace handle's git boundary sit?** Authoring writes files
   (slice 6). *Lean: the workspace is just a directory the agent already has
   filesystem access to; the server validates/tests it but doesn't own VCS —
   commits stay the agent's (or the human's) call. Defer any sandboxing to the
   [`oracle-capability-model`](oracle-capability-model.md) effort.*

## Non-goals

- The domain tools themselves (`story.*` slice 6, `session.*`/`render.*` slice 7).
- Cross-process or multi-client shared handles (epic open Q3 →
  [`hybrid-session-driving`](hybrid-session-driving.md)).
- Replacing `kitsoki serve` — that narrow per-app `transition` server stays.
