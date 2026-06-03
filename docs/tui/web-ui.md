# The interactive web UI (`kitsoki web`)

`kitsoki web <app.yaml>` serves a **browser-based, interactive** surface for a
kitsoki app: a chat-style transcript you drive, beside a live trace timeline and
state diagram. It is the writable sibling of the read-only
[run-status UI](../tracing/run-status-ui.md) (`kitsoki status serve` /
`export-status`) — same Vue SPA, same JSON-RPC + SSE contract, but backed by a
**live orchestrator running in the same process** rather than a recorded trace
file.

```
kitsoki web stories/prd/app.yaml
# → kitsoki: web UI for app "prd" (session <id>) on http://127.0.0.1:7777
# open http://127.0.0.1:7777/  →  click the session's “Drive (chat)” link
```

*Audience: operators who want to drive a story from a browser, and contributors
working on the surface. For the terminal surface see [the TUI](README.md); for
the read-only run viewer see [the run-status UI](../tracing/run-status-ui.md).*

> **Status:** PoC quality, functional over polished. Localhost / trusted-network
> only — there is **no authentication**. See [Limitations](#limitations--non-goals).

---

## What it is

One process hosts the orchestrator **and** an HTTP server. The browser:

- **observes** the session — the current room render, the live event trace, and
  the state diagram (the same `runstatus.Snapshot` the read-only UI projects); and
- **drives** the session — submits intents / free text / confirmations and reads
  the resulting room, turn by turn.

It reuses the run-status read plumbing (`internal/runstatus/server`,
`tools/runstatus/`) and adds a thin **write** layer. The orchestrator is
transport-agnostic: the TUI, the headless `kitsoki turn`, the flow-test rig, and
this web server all drive the same engine.

This is "option A" from the original proposal: **one orchestrator, one HTTP
host** — no separate server process, no write-back channel to a file tailer.

---

## Quickstart

The SPA is bundled into the `kitsoki` binary, so there is no Node step at
runtime — but the binary must have been **built with the SPA embedded** (see
[Building](#building--embedding)). Then:

### Live, against the real LLM (default)

```
kitsoki web stories/prd/app.yaml --addr 127.0.0.1:7777
```

The harness is **auto-selected** (`autoSelectHarness`, `cmd/kitsoki/main.go`),
identical to `kitsoki run`:

1. `claude` CLI on `PATH` → the **claude** harness (**no API key needed**).
2. else `ANTHROPIC_API_KEY` set → the `live` (direct-SDK) harness.
3. else → `replay` (requires `--recording`).

Override with `--harness claude|live|replay|recording` and `--claude-model`.

### Deterministic, no LLM (for development, demos, Playwright)

```
kitsoki web stories/prd/app.yaml --flow stories/prd/flows/happy_path.yaml
```

With `--flow`, the flow fixture's `host_handlers:` stub **every** `host.*`/oracle
call and **no harness is built** — you drive the session by submitting the
flow's intents from the UI, with fully reproducible responses. This is the same
fixture `kitsoki test flows` replays, so the web UI and the tests resolve a stub
identically (see [Commonality](#commonality-cassettes-flows-warps)).

### Open it

The server prints the live session id and address. Then:

- Browse to `http://<addr>/` → the **session list** → click the live session
  (or its **“Drive (chat)”** link), which routes to the interactive view at the
  hash route `#/s/<session-id>/chat`.
- Terminal sessions open the read-only observer instead.

> Remote host? `kitsoki web` binds to `127.0.0.1` by default. Either SSH-tunnel
> (`ssh -L 7777:127.0.0.1:7777 <host>`, then open `http://localhost:7777`) or
> bind openly with `--addr 0.0.0.0:7777` (no auth — trusted networks only).

---

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `--addr` | `127.0.0.1:7777` | HTTP listen address. |
| `--harness` | auto-select | `claude` \| `live` \| `replay` \| `recording`. Ignored with `--flow`. |
| `--claude-model` | engine default | Model for `--harness claude` (e.g. `opus`, `sonnet`). |
| `--recording` | — | Recording YAML for `--harness replay`. |
| `--record` | — | Output JSONL recording for `--harness recording`. |
| `--flow` | — | Drive deterministically from a flow fixture (no LLM; `host_handlers:` stub `host.*`; intents submitted explicitly). |
| `--host-cassette` | — | Host cassette backing `host.*` calls (deterministic, no LLM); combinable with `--flow`. |
| `--warp` | — | Warp-basis YAML (`state:` + `world:` overrides) applied as the first action after the session is created. |
| `--mode` | `staged` | Execution mode: `staged` \| `one-shot`. |
| `--db` | nearest `.kitsoki/sessions.db` | SQLite session store path. |

A **fresh session** is created on every launch. The session trace is written to
the standard per-session path (`store.DefaultTracePath(appID, "web", sid)`), so
`kitsoki trace`, `export-status`, etc. work against it afterward.

---

## Layout

```
┌──────────────────────────────┬───────────────────────────────────┐
│ kitsoki · <app>   <state> live│                          Observe ↗│
├──────────────────────────────┼───────────────────────────────────┤
│  AGENT                        │            STATE DIAGRAM           │
│    <rendered room view>       │   (mermaid, current node lit)      │
│                          You ▸│                                    │
│    <next room view>           ├───────────────────────────────────┤
│                               │            TRACE                   │
│                               │   (filterable timeline, live SSE)  │
├──────────────────────────────┤                                    │
│ [start] [quit] [look]         │                                    │
│ > discuss…              [Send]│                                    │
└──────────────────────────────┴───────────────────────────────────┘
   chat transcript + composer        live trace + state diagram
```

- **Chat (left)** — a transcript of the exchange. Each agent turn renders the
  room's view; user turns show what you submitted. The **composer** shows a
  button per allowed action intent, plus a text box bound to the room's
  free-text intent (see [Driving a turn](#driving-a-turn)). On a terminal state
  the composer is replaced by a “Session complete” note.
- **Trace + diagram (right)** — the same components as the read-only UI
  (`TraceTimeline`, `StateDiagram`), updated live over SSE as the session emits
  events.

Vue components: `tools/runstatus/src/views/InteractiveView.vue` (composition),
`components/ChatTranscript.vue`, `components/InputBar.vue`,
`components/ViewElement.vue`; store `stores/run.ts`.

---

## Driving a turn

The composer maps UI actions to the engine's intents:

- **Action buttons** — one per allowed intent that takes no free-text slot
  (e.g. `start`, `confirm`, `accept`). Clicking submits that intent with no
  slots.
- **Text box** — bound to the allowed intent that has a single free-text
  (`string`) slot (e.g. `discuss`'s `message`, `submit_answers`'s `answers`).
  Typing + **Send** submits that intent with the text as the slot value. If a
  room offers more than one text intent, a small selector chooses which.

The menu metadata that drives this comes from the enriched `intents` field of
the room view (`intentInfo{name, title, text_slot, has_slots}`), derived
server-side from each intent's slot schema (`OrchestratorDriver.IntentInfo`).

A menu pick is unambiguous, so the composer submits a **structured intent**
(`runstatus.session.submit` → `Orchestrator.SubmitDirect`) — deterministic and
LLM-free. Free-text → LLM **routing** (`runstatus.session.turn` →
`Orchestrator.Turn`, the semantic→cache→LLM tiers) is wired in the backend but
the composer does not use it yet; see [Limitations](#limitations--non-goals).

---

## The RPC + SSE contract

`POST /rpc` (JSON-RPC 2.0) and `GET /rpc/events?subscription_id=…`
(text/event-stream). Served by `internal/runstatus/server`. The same contract
is consumed by `tools/runstatus/src/data/live-source.ts`.

### Read methods (shared with `status serve`)

| Method | Returns |
|---|---|
| `runstatus.sessions.list` | `[]SessionHeader` (0–1 for one session) |
| `runstatus.session.get` | `SessionHeader` (id, app, current_state, turn, terminal) |
| `runstatus.session.app` | the `AppDef` |
| `runstatus.session.mermaid` | `{source, node_map}` for the diagram |
| `runstatus.session.trace` | `{events, last_turn}` (filterable by turn) |
| `runstatus.session.subscribe` / `.unsubscribe` | open / close the SSE stream |
| `runstatus.session.view` | the **current** room as a `turnResult` (no advance) — the browser's first frame |

### Write methods (live session only)

| Method | Params | → Orchestrator |
|---|---|---|
| `runstatus.session.turn` | `{input}` | `Turn` (free text, routes internally) |
| `runstatus.session.submit` | `{intent, slots}` | `SubmitDirect` (chosen intent) |
| `runstatus.session.continue` | `{slots}` | `ContinueTurn` (supply missing slots) |
| `runstatus.session.offpath` | `{input}` | `AskOffPath` (read-only side question) |

Write methods exist only when a **`Driver`** is attached
(`server.WithDriver`). `kitsoki web` attaches one; `kitsoki status serve` does
not, so there the write methods return JSON-RPC error code **`-32001`**
(read-only surface).

### `turnResult` (the write/`view` response)

```jsonc
{
  "mode": "transitioned",         // transitioned|clarify|rejected|completed|offpath|cancelled
  "state": "clarifying",          // current/landed state path
  "view": "CLARIFYING …",         // server-RENDERED room text (markdown)
  "typed_view": null,             // present only for template-free element views
  "allowed_intents": ["submit_answers","skip","quit"],
  "intents": [                    // enriched menu the composer renders
    {"name":"submit_answers","title":"Submit answers","text_slot":"answers","has_slots":true},
    {"name":"skip","has_slots":false}
  ],
  "slots_needed": [ … ],          // on mode=clarify
  "pending_intent": "",           // on mode=clarify
  "error_code": "", "error_message": "", "guard_hint": "",  // on mode=rejected
  "turn_number": 3
}
```

A guard rejection or a missing slot is **not** a transport error — it rides back
as `mode: "rejected"` / `mode: "clarify"` with the structured reason, because it
is a normal interpreted outcome of the turn. Only infrastructure failures
surface as a JSON-RPC error.

---

## Render fidelity (important)

The engine renders a room's view to **text server-side**, at its stable width,
against the live world — exactly what the operator sees in the TUI. The browser
**never evaluates pongo**; it displays the already-rendered text. `session.view`
was deliberately fixed to ship rendered text (not a raw `{{ … }}` template) so
the page never leaks template markers.

`ChatTranscript.vue` renders that text **verbatim in a monospace, pre-wrapped
card** — preserving the engine's column alignment, numbered lists, and
indentation rather than re-flowing them — and formats only inline
`**bold**` / `` `code` `` / `##` headings. This is faithful to the TUI's view.

This honours the rule in `tools/runstatus/CLAUDE.md`: the **trace/render must be
correct at the source — never patch it with a UI hack**. If a room view looks
wrong in the browser, fix the view/render, not the CSS.

---

## Architecture

```
browser ──HTTP/SSE──▶ server.Server ──▶ server.Driver ──▶ Orchestrator (live)
                          │                                     │
                          └──── server.Source ◀── LiveSession ──┘ (event sink)
```

- **`buildSessionRuntime`** (`cmd/kitsoki/runtime.go`) — the shared constructor
  for the store, journal, jobs, chats, machine, host registry, harness, oracle
  registry and orchestrator. Used by **`kitsoki run`, `kitsoki web`, and** (via
  shared mechanisms) the flow-test rig, so all three build a session the same
  way.
- **`server.LiveSession`** (`internal/runstatus/server/live.go`) — a
  mutex-wrapped `store.JSONLSink` that is **both** the orchestrator's
  `store.EventSink` (write path) **and** the server's `Source` (read path), so
  the unguarded JSONL sink is never read and written concurrently. Snapshots are
  built via `runstatus.FromSink` (the same path `export-status` uses, so live
  and exported views can't drift). SSE streams new events straight from the
  in-process sink — no file polling.
- **`server.Driver` / `OrchestratorDriver`** (`driver.go`) — the write side;
  binds the session id and forwards to `Orchestrator.{Turn,SubmitDirect,
  ContinueTurn,AskOffPath}`. `Driver.View` → `Orchestrator.CurrentView` renders
  the current room without advancing.
- **`Orchestrator.CurrentView`** (`internal/orchestrator`) and
  **`machine.LookupIntent`** (`internal/machine`) back `session.view` and the
  enriched menu.
- The snapshot header derives the current state by preferring the last
  `machine.state_entered` event (a `turn.end` carries the turn's *starting*
  state), which also corrects `status serve` / `export-status` post-transition.

---

## Commonality: cassettes, flows, warps

A core goal: the web UI is driveable by the **same** deterministic machinery as
the rest of kitsoki — not a bespoke replay mode.

- **Flows** — `--flow <fixture>` registers the fixture's `host_handlers:` via
  the **shared** `testrunner.RegisterHostStubs`, the exact path `kitsoki test
  flows` uses.
- **Host cassettes** — `--host-cassette <file>` installs cassette dispatchers
  (`testrunner.LoadCassette` / `BuildCassetteDispatcher`), as in the flow rig.
- **LLM recordings** — `--harness replay --recording <yaml>` (or `recording` to
  capture), via the shared `buildHarness`.
- **Warps** — `--warp <basis>` loads a warp basis (`tui.LoadWarpBasis`) and
  teleports the fresh session to it (`Orchestrator.Teleport`) before serving —
  same as `kitsoki run --warp`.

So any deterministic scenario that works under `run` / `test` can drive the web
UI identically.

---

## The demo + video

`tools/runstatus/tests/playwright/web-chat.spec.ts` drives the full PRD
`happy_path` chat in a headless browser at MacBook resolution (1440×900 @2×),
asserting the state badge at every scene and recording a video + per-scene
screenshots into `.artifacts/web-chat/`.

```
cd tools/runstatus && pnpm exec playwright test web-chat --project=chromium
```

It is **human-paced** by default (visible typing, a beat before each action, a
dwell on each scene) so the recording is watchable; set `WEB_CHAT_PACE=0` to
collapse the delays for a fast assertion-only CI run.

---

## Building & embedding

The SPA (`tools/runstatus/`) is built by Vite and **staged into the Go embed
dir** (`internal/runstatus/web/assets/index.html`, gitignored — only
`.gitkeep` is committed) so the binary serves it with no Node at runtime.

```
make build      # pnpm build + stage + go build (the binary embeds a fresh SPA)
make web        # just rebuild + stage the SPA (then rebuild the binary)
make install    # build + install kitsoki to $GOBIN
```

If the SPA was not built, the page reports the UI as unbuilt (HTTP 503) while
the RPC/SSE endpoints still work.

Frontend tests: `cd tools/runstatus && pnpm test` (Vitest). Backend tests:
`go test ./internal/runstatus/... ./cmd/kitsoki/`.

---

## Limitations & non-goals

- **No auth, single-tenant.** Localhost / trusted-network dev server. A fresh
  session per launch; no multi-user hosting.
- **Composer submits structured intents.** Free-text → LLM **routing** through
  the UI (`session.turn`) exists in the backend but the composer binds typing to
  the room's text-slot intent instead; richest in conversational / menu-driven
  rooms.
- **Off-path only; no full meta-mode.** `session.offpath` is wired; the
  persistent meta-mode sidebar from the TUI is not yet ported.
- **PoC UI.** Functional over designed. The agent room view is rendered
  faithfully (monospace) rather than re-styled as bespoke HTML; a richer
  typed-element HTML render (server-rendered `typed_view`) is a possible future
  enhancement.

---

## Pointers

- Command: `cmd/kitsoki/web.go`, shared runtime `cmd/kitsoki/runtime.go`
- Server / RPC / SSE: `internal/runstatus/server/{server,live,driver}.go`
- Current-view + menu: `internal/orchestrator` (`CurrentView`),
  `internal/machine` (`LookupIntent`)
- SPA: `tools/runstatus/src/` (`views/InteractiveView.vue`,
  `components/{ChatTranscript,InputBar,ViewElement}.vue`, `stores/run.ts`,
  `data/{source,live-source}.ts`)
- Read-only sibling: [run-status UI](../tracing/run-status-ui.md) ·
  Terminal sibling: [the TUI](README.md)
