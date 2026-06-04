# The multi-story web UI (`kitsoki web`)

`kitsoki web` serves a **browser-based, interactive** surface that hosts many
live kitsoki sessions at once. It starts **story-less**: the home screen lists
the stories it discovered on disk and every session currently running, and the
operator starts a new session from a story or opens an existing one. Each
session is its own in-process orchestrator, independently navigable and
reloadable.

It is the writable, multi-session sibling of the read-only
[run-status UI](../tracing/run-status-ui.md) (`kitsoki status serve` /
`export-status`) — same Vue SPA, same JSON-RPC + SSE contract — but backed by
**live orchestrators running in the same process** rather than a recorded trace
file. It is the multi-story evolution of the original single-story `kitsoki web
<app.yaml>`: where that bound one `app.yaml` for the life of the process, this
discovers a catalogue and routes every RPC to the right session.

```
kitsoki web
# → kitsoki: web UI (7 stories across 1 dir(s)) on http://127.0.0.1:7777
# open http://127.0.0.1:7777/  →  the home screen (stories + live sessions)
```

*Audience: operators who want to drive stories from a browser, and contributors
working on the surface. For the terminal surface see [the TUI](../tui/README.md);
for the read-only run viewer see [the run-status UI](../tracing/run-status-ui.md).*

> **Status:** PoC quality, functional over polished. Localhost / trusted-network
> only — there is **no authentication**. See [Limitations](#limitations--non-goals).

---

## What it is

One process hosts a **`SessionRegistry`** (`cmd/kitsoki/registry.go`) and an
HTTP server. The registry discovers stories, owns one live session per
operator-started session, and fulfils the server's `SessionProvider` seam; the
server (`internal/runstatus/server`) routes every RPC to the right session by
its `session_id` and exposes the story-browser / session-lifecycle methods the
home screen needs. The browser:

- **observes** a session — the current room render, the live event trace, and
  the state diagram (the same `runstatus.Snapshot` the read-only UI projects);
- **drives** a session — submits intents / free text / confirmations and reads
  the resulting room, turn by turn; and
- **manages** sessions — browses the discovered stories, starts new sessions,
  reloads a story in place.

The orchestrator is transport-agnostic: the TUI, the headless `kitsoki turn`,
the flow-test rig, and this web server all drive the same engine.

---

## Story discovery & configuration

`kitsoki web` discovers stories by walking one or more directories for files
named exactly `app.yaml`; each one found becomes a story in the catalogue. The
loader is `internal/webconfig`.

### Resolution order (first non-empty wins)

1. **`--stories-dir <dir>` flags** (repeatable) — ad-hoc override, no config edit.
2. **`story_dirs` in `.kitsoki.yaml`** in the working directory.
3. **`./stories`** — the default when neither is given, so the common single-repo
   case needs no configuration.

The config file is **optional** and carries only `story_dirs` for now (the
struct is the stable extension point for future keys — mode/addr/db are *not*
config keys):

```yaml
# .kitsoki.yaml
story_dirs:
  - stories
  - testdata/apps
```

A malformed `app.yaml` under a scanned directory is **logged and skipped**, not
fatal — valid siblings still appear in the catalogue. Discovery is **explicit**:
the operator triggers a re-walk with the home screen's **Rescan** button
(`stories.rescan`); there is no `fsnotify` watch (that is future work).

### Story identity is the path

A story's canonical key is the **absolute path to its `app.yaml`**, not its
`app.id`. `app.id`/title are display-only, so two stories that share an `app.id`
never collide. `session.new` takes a `story_path`; `StoryHeader` carries
`{path, app_id, title, active_sessions}`.

---

## The home screen

The SPA `/` route (`tools/runstatus/src/views/HomeView.vue`) is the story
browser + live-session list.

- **Stories** — a card per discovered story: title, relative path, an
  active-session-count badge, and a **New session** button. New session calls
  `session.new {story_path}` then opens the new session on its **drive** surface
  (`/s/<returned_id>/chat`) — a fresh session is live and meant to be driven, so
  the operator lands on the conversation (opening prompt + composer), not the
  read-only observer.
- **Active sessions** — a row per live session: story title + path, a truncated
  session id, current state, last activity, and an **Open** link to the observer
  (`/s/<id>`), which itself offers a **Drive (chat) ↗** link for live sessions.
  The list refreshes on a short **poll interval** (consistent with explicit
  rescan — no new SSE event type).
- **Rescan** — re-walks the configured dirs and refreshes the catalogue,
  leaving live sessions untouched.
- **Auto-navigation** — on first load, if there is exactly one session and no
  others, the SPA replaces `/` with that session — a still-live one on its drive
  surface (`/s/<id>/chat`), a finished one on the observer (`/s/<id>`);
  otherwise it stays on the home screen.

A single session view lives at `/s/:sessionId`
(`tools/runstatus/src/views/RunView.vue`); the conversational view is at
`/s/:sessionId/chat`. The live-source client (`data/live-source.ts`) is
initialised with the route's `session_id` and sends it on every RPC.

---

## The session view

A session has two surfaces over the same live trace:

- **Observe** (`/s/:sessionId`, `RunView.vue`) — the read-only state diagram and
  filterable trace timeline, identical to the [run-status UI](../tracing/run-status-ui.md),
  plus the **Reload** button (see [Reload](#reload-parity-with-the-tui-reload))
  and, while the session is live, a **Drive (chat) ↗** link onto the drive surface.
- **Drive** (`/s/:sessionId/chat`, `InteractiveView.vue`) — the conversational
  surface: a chat transcript you drive on the left, the live diagram + trace on
  the right, and an **Observe ↗** link back to the read-only view.

The two surfaces link to each other both ways, so neither is a dead-end: a live
session always offers the path to driving it, a driven session the path to its
trace.

### Layout (Drive)

```
┌──────────────────────────────┬───────────────────────────────────┐
│ ← Sessions · <app>  <state> live                         Observe ↗│
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
  the composer is replaced by a "Session complete — no further input accepted."
  note.
- **Trace + diagram (right)** — the same `StateDiagram` / `TraceTimeline`
  components as the read-only UI, updated live over SSE as the session emits
  events.

Vue components: `views/InteractiveView.vue` (composition),
`components/{ChatTranscript,InputBar,ViewElement}.vue`; store `stores/run.ts`.

### Driving a turn

The composer maps UI controls to the engine's intents:

- **Action buttons** — one per allowed intent that takes no free-text slot
  (e.g. `start`, `confirm`, `accept`). Clicking submits that intent with no slots.
- **Text box** — bound to the allowed intent that has a single free-text
  (`string`) slot (e.g. `discuss`'s `message`, `submit_answers`'s `answers`).
  Typing + **Send** submits that intent with the text as the slot value. If a
  room offers more than one text intent, a small selector chooses which.

The menu metadata that drives this comes from the enriched `intents` field of
the room view (`intentInfo{name, title, text_slot, has_slots}`), derived
server-side from each intent's slot schema (`OrchestratorDriver.IntentInfo`).

Every composer action submits a **structured intent**
(`runstatus.session.submit` → `Orchestrator.SubmitDirect`) — the operator picked
a concrete intent and the slot value is already bound, so it is deterministic and
LLM-free, and works under the no-harness `--flow` posture. Free-text → LLM
**routing** (`runstatus.session.turn` → `Orchestrator.Turn`, the
semantic→cache→LLM tiers) is wired in the backend but the composer does not use
it yet; see [Limitations](#limitations--non-goals).

### Render fidelity

The engine renders a room's view to **text server-side**, at its stable width,
against the live world — exactly what the operator sees in the TUI. The browser
**never evaluates pongo**; `session.view` ships already-rendered text (not a raw
`{{ … }}` template), so the page never leaks template markers. `ChatTranscript.vue`
renders that text **verbatim in a monospace, pre-wrapped card** — preserving the
engine's column alignment, numbered lists, and indentation rather than re-flowing
them — and formats only inline `**bold**` / `` `code` `` / `##` headings.

This honours the rule in `tools/runstatus/CLAUDE.md`: the **trace/render must be
correct at the source — never patch it with a UI hack**. If a room view looks
wrong in the browser, fix the view/render, not the CSS.

---

## Session lifecycle

- **Creation.** `session.new {story_path}` runs `buildSessionRuntime` over the
  registry's session-invariant `runtimeBase` (so the deterministic `--flow` /
  `--host-cassette` no-LLM posture, the harness, mode, db path, and recording
  options apply to **every** session), starts the orchestrator, and returns a
  fresh **UUID** id that routes subsequent calls. An **invalid story YAML fails
  fast** with a structured error, so the UI can surface it before navigating.
- **In-memory only.** Sessions live in the registry's maps and **die with the
  process** — they are *not* persisted across restarts. The per-session JSONL
  trace on disk survives (`kitsoki trace` / `export-status` / `kitsoki status
  serve` work against it afterward), but re-attaching a live session to an old
  trace is out of scope.
- **No cap.** A long-lived server accumulates orchestrators; there is no session
  limit in the PoC.
- **No kill action.** There is no "close session" affordance; sessions end when
  the process exits.

---

## Reload (parity with the TUI `/reload`)

A session view's **Reload** button hot-reloads the story's `app.yaml` in place,
**mirroring the TUI's `/reload` command exactly** — no new reload mechanism is
introduced in the orchestrator. `session.reload {session_id}` runs the same
sequence the TUI does (`SessionRegistry.Reload`, `cmd/kitsoki/registry.go`):

1. read the session's current state from its snapshot;
2. `Orchestrator.Reload(storyPath, currentState)` — re-validate and swap in the
   new `AppDef`, rebinding the current state if it still exists;
3. `RecordEffectiveStory` so the trace stays self-contained across the reload;
4. when the prior state still exists, `RerunOnEnter` to re-fire the room's
   `on_enter` chain.

The response is `{ok, prev_state_exists}`. When **`prev_state_exists` is
false** — the edit removed the session's current state — the engine cannot
re-enter it, so the session stays put and the UI shows the warning **"current
state removed; staying put"**, matching the TUI's "re-render only" notice. When
true, the normal SSE-driven refresh repaints.

The reload mechanics themselves are documented once, canonically, in the engine:
see the **Hot reload** bullet under [the turn loop in
`docs/stories/state-machine.md`](../stories/state-machine.md#8-the-turn-loop-state-machine-of-the-orchestrator)
(and `on_enter` idempotence under
[`docs/stories/state-machine.md`](../stories/state-machine.md#on_enter-must-be-idempotent)).

---

## Flags

`kitsoki web` takes **no positional argument**. The story directories come from
`--stories-dir` / `--config` / `.kitsoki.yaml`; the remaining flags set the
**per-session defaults** every new session inherits.

| Flag | Default | Meaning |
|---|---|---|
| `--config` | `.kitsoki.yaml` | Path to the web config file (`story_dirs`). |
| `--stories-dir` | — | Story directory to walk for `app.yaml` (repeatable; overrides `.kitsoki.yaml`). |
| `--addr` | `127.0.0.1:7777` | HTTP listen address. |
| `--mode` | `staged` | Execution mode applied to every session: `staged` \| `one-shot`. |
| `--db` | nearest `.kitsoki/sessions.db` | SQLite session store path. |
| `--harness` | auto-select | `claude` \| `live` \| `replay` \| `recording`. Ignored with `--flow`. |
| `--claude-model` | engine default | Model for `--harness claude` (e.g. `opus`, `sonnet`). |
| `--recording` | — | Recording YAML for `--harness replay`. |
| `--record` | — | Output JSONL recording for `--harness recording`. |
| `--flow` | — | Drive every session deterministically from a flow fixture (no LLM; `host_handlers:` stub `host.*`; intents submitted explicitly). |
| `--host-cassette` | — | Host cassette backing `host.*` calls (deterministic, no LLM); combinable with `--flow`. |

### Deterministic, no LLM (for development, demos, Playwright)

```
kitsoki web --stories-dir stories/prd --flow stories/prd/flows/happy_path.yaml
```

With `--flow`, the fixture's `host_handlers:` stub **every** `host.*`/oracle
call and **no harness is built**; the posture is threaded into `runtimeBase`, so
**every** session the home screen starts is fully reproducible. This is the same
fixture `kitsoki test flows` replays, so the web UI and the tests resolve a stub
identically.

The determinism is not a bespoke replay mode — the web UI reuses the **same**
machinery as the rest of kitsoki: `--flow` registers `host_handlers:` via the
shared `testrunner.RegisterHostStubs`, and `--host-cassette` installs cassette
dispatchers (`testrunner.LoadCassette` / `BuildCassetteDispatcher`), the exact
paths the flow rig uses. So any deterministic scenario that runs under `kitsoki
run` / `kitsoki test` drives the web UI identically.

---

## The RPC + SSE contract

`POST /rpc` (JSON-RPC 2.0) and `GET /rpc/events` (text/event-stream), served by
`internal/runstatus/server` and consumed by
`tools/runstatus/src/data/live-source.ts`. Every session-routed method takes a
`session_id` param; the server resolves the live session via the provider and a
**missing or unknown id returns a structured not-found error**. The
`runstatus.event` SSE wire format is unchanged from the read-only UI — each
subscription captures its `session_id` and the poller resolves that session's
source per tick.

### Lifecycle methods (the home screen)

| Method | Params | Returns |
|---|---|---|
| `runstatus.stories.list` | `{}` | `[]StoryHeader` (`{path, app_id, title, active_sessions}`) |
| `runstatus.stories.rescan` | `{}` | `[]StoryHeader` (refreshed catalogue) |
| `runstatus.session.new` | `{story_path}` | `{session_id}` (structured error on invalid story) |
| `runstatus.session.reload` | `{session_id}` | `{ok, prev_state_exists}` |
| `runstatus.sessions.list` | `{}` | `[]SessionHeader` (one per live session) |

### Session-routed read / write methods

The read methods (`session.get/app/mermaid/trace/view`,
`session.subscribe/unsubscribe`) and write methods
(`session.turn/submit/continue/offpath`) are unchanged in shape from the
single-story surface; they are now resolved per `session_id`. See [the contract
section of the run-status UI](../tracing/run-status-ui.md) for the read shapes.

Write methods exist only when a session has a **`Driver`** attached (every
`kitsoki web` session does; `kitsoki status serve` does not — see [Read-only
surface unaffected](#read-only-surface-unaffected)):

| Method | Params | → Orchestrator |
|---|---|---|
| `runstatus.session.turn` | `{input}` | `Turn` (free text, routes internally) |
| `runstatus.session.submit` | `{intent, slots}` | `SubmitDirect` (chosen intent) |
| `runstatus.session.continue` | `{slots}` | `ContinueTurn` (supply missing slots) |
| `runstatus.session.offpath` | `{input}` | `AskOffPath` (read-only side question) |

### `turnResult` (the write / `view` response)

The write methods and `session.view` return the room as a `turnResult`
(`internal/runstatus/server/driver.go`):

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
is a normal interpreted outcome of the turn. Only infrastructure failures surface
as a JSON-RPC error.

### Read-only surface unaffected

`kitsoki status serve` still uses the single-entry `server.New(tracePath, def,
…)` path. It satisfies the **full** `SessionProvider` via a one-session adapter
(`singleEntryProvider`): the lifecycle methods (`session.new` / `session.reload`
/ `stories.rescan`) return a structured **read-only** error (JSON-RPC code
`-32001`) rather than nil-derefing; `stories.list` is a tolerant empty read.

---

## Architecture

```
browser ──HTTP/SSE──▶ server.Server ──▶ SessionProvider (registry)
                          │                    │   ├─ Entry{Source, Driver} per session
                          │                    │   └─ StoryHeader catalogue (webconfig)
                          └── per-session Source/Driver resolved by session_id
```

- **`internal/webconfig`** — config load (`.kitsoki.yaml`), directory resolution
  (`Resolve`), and `DiscoverStories` (walk for `app.yaml`, one `StoryMeta` each).
- **`SessionRegistry`** (`cmd/kitsoki/registry.go`, package main) — the concrete
  `SessionProvider`. It lives in `main` because it must call `buildSessionRuntime`
  (`cmd/kitsoki/runtime.go`) and clone the session-invariant `runtimeBase`;
  `internal/` packages cannot import `main`, which is why the server *defines* the
  `SessionProvider` seam and the registry *depends on* it.
- **`server.SessionProvider` / `server.Entry` / `server.StoryHeader`**
  (`internal/runstatus/server/provider.go`) — the multi-session seam, an entry's
  read `Source` + write `Driver`, and the story-browser shape. `server.NewMulti`
  serves a provider; `server.New` keeps the read-only single-entry path.
- **`server.LiveSession` / `OrchestratorDriver`** — per the shared web plumbing;
  the registry wires one of each per session over `buildSessionRuntime`'s output.

The shared per-session machinery (`buildSessionRuntime`, `LiveSession`, the
`Driver`, render fidelity, cassettes/flows/warps commonality, and SPA build &
embedding) is documented with the read-only viewer and the SPA itself; see the
[Pointers](#pointers) below rather than restating it here.

---

## Building & embedding

The SPA (`tools/runstatus/`) is built by Vite and **staged into the Go embed
dir** (`internal/runstatus/web/assets/index.html`, gitignored — only `.gitkeep`
is committed) so the binary serves it with no Node at runtime.

```
make build      # pnpm build + stage + go build (the binary embeds a fresh SPA)
make web        # just rebuild + stage the SPA (incremental)
make install    # build + install kitsoki to $GOBIN
```

If the SPA was not built, the page reports the UI as unbuilt (HTTP 503) while
the RPC/SSE endpoints still work.

## Demo, video & testing

The end-to-end Playwright spec
`tools/runstatus/tests/playwright/multi-story.spec.ts` drives the full flow —
home discovery → new session → reload → driving the PRD `happy_path` chat to
completion → back to the active-sessions list — in a headless browser at MacBook
resolution (1440×900 @2× retina), asserting the state badge at every scene.

```
cd tools/runstatus && pnpm exec playwright test multi-story --project=chromium
```

It records a stable video and per-scene screenshots into `.artifacts/multi-story/`
(`multi-story-demo.webm` plus numbered `NN-*.png` frames). It is **human-paced**
by default (visible typing, a beat before each action, a dwell on each scene) so
the recording is watchable; set `WEB_CHAT_PACE=0` to collapse the delays for a
fast assertion-only CI run.

Other tests: `cd tools/runstatus && pnpm test` (Vitest, frontend);
`go test ./internal/runstatus/... ./cmd/kitsoki/` (backend).

---

## Limitations & non-goals

- **No auth, single-tenant.** Localhost / trusted-network dev server.
- **Sessions are ephemeral.** In-memory only; not persisted across restarts (a
  database-backed registry is explicit future work). Re-attaching to a trace
  after a restart is `kitsoki status serve`'s job.
- **Explicit rescan only.** No `fsnotify` watch for auto-discovery (future work).
- **No session cap, no kill action.** Sessions accumulate and die with the process.
- **Composer submits structured intents.** Free-text → LLM routing through the UI
  (`session.turn`) exists in the backend but the composer binds typing to the
  room's text-slot intent instead (see [Driving a turn](#driving-a-turn)).
- **Off-path only; no full meta-mode.** `session.offpath` is wired; the
  persistent meta-mode sidebar from the TUI is not yet ported.
- **PoC UI.** Functional over designed; the agent room view is rendered
  faithfully (monospace) rather than re-styled as bespoke HTML. A richer
  server-rendered `typed_view` HTML render is possible future work.

---

## Pointers

- Entrypoint: `cmd/kitsoki/web.go`; registry: `cmd/kitsoki/registry.go`; shared
  runtime + `runtimeBase`: `cmd/kitsoki/runtime.go`
- Discovery / config: `internal/webconfig/`
- Server / provider / RPC / SSE: `internal/runstatus/server/{server,provider,live,driver}.go`
- SPA: `tools/runstatus/src/` (`views/{HomeView,RunView,InteractiveView}.vue`,
  `data/live-source.ts`, `router.ts`)
- Reload mechanics (canonical): [`docs/stories/state-machine.md`](../stories/state-machine.md#8-the-turn-loop-state-machine-of-the-orchestrator)
- Read-only sibling: [run-status UI](../tracing/run-status-ui.md) ·
  Terminal sibling: [the TUI](../tui/README.md)
