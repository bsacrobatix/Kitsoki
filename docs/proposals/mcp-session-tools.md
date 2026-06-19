# Runtime: MCP session + render tools — drive, introspect, see

**Status:** Draft v1. Nothing implemented yet.
**Kind:**   runtime
**Epic:**   [mcp-studio.md](mcp-studio.md) (slice 7)

## Why

This is the payoff slice: it lets the external agent **drive a live session and
see the result**. The driving substrate exists piecemeal — `orch.Turn` routes
free text (`orchestrator.go:848`), `SubmitDirect`/`ContinueTurn` handle direct
intents and slot-fills, `buildInspectOutput` reports state/world/intents
(`inspect.go:155`), and the runstatus `OrchestratorDriver` already binds them to
a session id (`driver.go:79`). The *seeing* substrate is this epic's slices 1–4
(frame composer, `kitsoki drive`, `kitsoki shot`, web screenshot). What's missing
is the MCP facade that hands all of it to the agent as tools — so "type *go back
to the debug room*, then show me what that room looks like" is two tool calls,
not a bespoke harness.

## What changes

**One sentence:** register `session.*` tools (new / attach / drive / submit /
continue / inspect / trace) wrapping the slice-2 `drive` loop over
`orch.Turn`/`SubmitDirect`/`ContinueTurn`, and `render.*` tools (tui / tui_png /
web) returning the slice-1 `Frame` and slice-3/4 PNGs — including MCP **image
content blocks** so a vision-capable client sees the screen.

## Command / tool surface

| Kind | Name | Shape | Wraps |
|---|---|---|---|
| tool | `session.new` | `{story_path, harness?: replay\|live, cassette?, record?} → {handle, state}` | slice-2 `drive` setup; default `replay`/`none` (shared decision 3) |
| tool | `session.attach` | `{story_path, key} → {handle, state}` | `AttachExternal` (`server.go:640`) — co-drive an existing keyed session |
| tool | `session.drive` | `{handle, input} → {outcome, frame}` | **free text** → `orch.Turn` (`orchestrator.go:848`); the interpretive route |
| tool | `session.submit` | `{handle, intent, slots?} → {outcome, frame}` | `SubmitDirect` (`orchestrator.go:1541`) — pick a menu intent |
| tool | `session.continue` | `{handle, slots} → {outcome, frame}` | `ContinueTurn` (`orchestrator.go:2039`) — supply missing slots |
| tool | `session.inspect` | `{handle} → {state, world, allowed_intents, last_view, last_turns[]}` | `buildInspectOutput` (`inspect.go:155`) |
| tool | `session.trace` | `{handle, since?, until?, limit?} → {events[], last_turn}` | the session's JSONL trace (`runstatus.session.trace` shape, `server.go`) |
| tool | `render.tui` | `{handle \| spec, cols?, rows?} → {frame}` | slice-1 `ComposeFrame` → `{text, ansi, metadata}` |
| tool | `render.tui_png` | `{handle \| spec, cols?, rows?} → image` | slice-3 `kitsoki shot` (ANSI→PNG) |
| tool | `render.web` | `{handle \| spec} → image` | slice-4 headless web→PNG |

A `spec` is an explicit `{story_path, state, world?}` for rendering a state the
agent hasn't driven to (e.g. "show me the `reviewing` room with `bug_filed:true`")
— rendered headlessly without mutating any session.

## Impact

- **Code seams:** new `session.*`/`render.*` tools in `internal/mcp/studio/`
  against the session handle (slice 5). `session.drive`/`submit`/`continue` call
  the slice-2 `drive` loop (or the `OrchestratorDriver` directly,
  `driver.go:79`); `inspect` reuses `buildInspectOutput`; `render.*` call
  `ComposeFrame` (slice 1) / `shot` (slice 3) / web-render (slice 4).
- **Vocabulary:** tool namespace only.
- **Stories affected:** none change behavior; any story is drivable.
- **Backward compat:** additive. Cassettes/traces unchanged.
- **Docs on ship:** `docs/architecture/mcp-studio.md`,
  `docs/architecture/developer-guide.md` §6.

## The model

Deterministic by construction except at the one interpretive seam — `session.drive`
routing free text — which is exactly where the moat says an interpretive decision
lives and is recorded (slice 2):

```
session.drive(handle, "go back to the debug room")
   └─▶ orch.Turn ──▶ [routing harness: replay default | live opt-in] ──▶ intent
                          recorded to trace + cassette ──┘
                     ──▶ machine transition (deterministic) ──▶ TurnOutcome
   ◀── { outcome: {mode, routed_intent, confidence, allowed_intents, slots_needed},
         frame: ComposeFrame(...) }            // slice-1 Frame, every turn

render.web(handle)
   └─▶ headless kitsoki web of this session's state (slice 4) ──▶ PNG ──▶ MCP image block
```

Every drive/submit/continue returns **both** the structured `TurnOutcome`
(`outcome.go:59`: mode, new state, allowed intents, slots needed) **and** the
`Frame` — so the agent reasons on metadata and *sees* the screen in one call.
`render.*` are read-only re-renders of a state the agent already reached, or of
an explicit `spec`.

## Decision recording

Reuses the recording substrate wholesale. `session.drive` routes through the
slice-2 `drive` loop, which writes the same JSONL events as `turn --trace`
(`oracle.call.*`, routing, transition; `turn.go:330`) and grows the routing
cassette per the `--record` mode. `render.*` record nothing (re-renders). No new
event types — the trace `session.trace` returns is the canonical one.

## Engine seams & invariants

- **`session.drive` is the only interpretive tool.** It submits free text via
  `orch.Turn` — the orchestrator API the TUI uses (`tui.go:1819`), not TUI-bound.
  `submit`/`continue` are deterministic direct paths.
- **No-LLM invariant (shared decision 3):** `session.new` defaults to
  `harness: replay`, `record: none`. A replay miss is a hard error
  (`ErrRecordingMiss`, `replay.go`), never a silent live fallthrough; the
  no-fallthrough test from slice 2 covers this and is re-asserted here for the
  MCP path. `harness: live` is opt-in and reflected in the handle's metadata.
- **`render.*` never mutate a session.** They call `ComposeFrame`/`shot`/web on a
  read of the handle's current journey (or a `spec`), so "look at this" can't
  advance the machine — principle of least surprise.
- **Image content gating (epic open Q1):** `render.tui_png`/`render.web` return an
  MCP image block when the client advertises image support, and always include
  the textual frame/`Frame.text`; a text-only client still gets *something*.

## Backward compatibility / migration

Additive. Existing cassettes replay unchanged under `session.new {harness:
replay}`; `session.attach` reuses the shipped `AttachExternal` external-key bind
(`server.go:640`), so an MCP-driven session and a `loop.py`/`session continue`
process can co-drive one keyed session under the writer lock (the
[`hybrid-session-driving`](hybrid-session-driving.md) guarantee).

## Tasks

```
## 1. Engine
- [ ] 1.1 session.new / attach: open a driving handle (slice-2 drive setup; replay default)
- [ ] 1.2 session.drive (orch.Turn) / submit (SubmitDirect) / continue (ContinueTurn) → {outcome, frame}
- [ ] 1.3 session.inspect (buildInspectOutput) / session.trace (JSONL events)
- [ ] 1.4 render.tui (ComposeFrame) / render.tui_png (shot) / render.web (slice 4)
- [ ] 1.5 spec-render: {story_path, state, world} → Frame/PNG without a live session
- [ ] 1.6 MCP image content blocks (gated on client image support); text frame always present

## 2. Verification
- [ ] 2.1 Drive a known cassette under replay → identical TurnOutcome + Frame as a golden
- [ ] 2.2 No-live-fallthrough: replay session never calls an injected failing live harness
- [ ] 2.3 inspect after a drive matches buildInspectOutput for that state (state/world/intents)
- [ ] 2.4 render.tui frame equals the slice-1 composer; render.web PNG renders a known state (no LLM)
- [ ] 2.5 render.* on a handle leave the session's state/turn unchanged

## 3. Adopt + document
- [ ] 3.1 Drive stories/bugfix end-to-end over MCP against its cassette; render each state
- [ ] 3.2 developer-guide.md §6 + mcp-studio.md session/render sections; update the epic slice row
```

## Verification

No-LLM: drive `stories/bugfix` over its `recording.yaml` under `harness:
replay`, diff the per-turn `{outcome, frame}` against a golden, and confirm
`render.tui`/`render.web` rasterize a known state. The no-fallthrough and
render-is-read-only tests are the teeth. The one path that *needs* live — novel
free text under `harness: live` — is opt-in and never in the default suite
(memory: no-LLM tests).

## Open questions

1. **`render` blocking on web spin-up.** `render.web` needs a headless `kitsoki
   web` (slice 4). *Lean: slice 4 owns a short-lived/pooled server keyed by
   story; `render.web` is one call against it with a `--timeout`, same blocking
   contract as a slow `host.run` in `drive` (slice-2 open Q2).*
2. **Async `on_enter` waits.** Some driven turns kick slow host work. *Lean:
   inherit slice 2's "block per turn until the orchestrator settles, emit the
   settled frame; `--timeout` guards a hang."*

## Non-goals

- The Frame composer / `drive` loop / `shot` / web-render themselves — slices
  1–4; this slice *calls* them.
- Forwarding a driven sub-agent's operator-ask question back to the external LLM
  (epic open Q2).
- Cross-process live SSE of a co-driven session — [`hybrid-session-driving`](hybrid-session-driving.md).
