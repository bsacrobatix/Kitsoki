# TUI: State diagram — path & horizon (runstatus web)

**Status:** Draft v1. Nothing implemented yet. Three design directions mocked up in `.artifacts/diagram-options/index.html` (not committed).
**Kind:**   tui
**Epic:**   — standalone

<!--
  This is the runstatus *web* viewer (Vue 3 + Pinia, tools/runstatus/), not
  the Go TUI. So the template's Go-specific hard rules adapt:
  - "typed elements + pongo2" → keep rendering data-driven in the Vue SFC,
    derived from the store; no string-built markup. The diagram is already
    a parsed model (src/diagram/parse.ts) — extend the model, don't bolt on
    ad-hoc DOM.
  - "CapturedIO rendering test" → Vitest component/derivation tests plus a
    deterministic SVG text-containment gate (see Rendering tests).
  The viewer's prime directive (tools/runstatus/CLAUDE.md): the trace is the
  source of truth; NEVER use a UI adjustment to cover a trace defect.
-->

## Why

Drive the kitsoki-dev dog-food story, type **"work on a proposal"**, then give a
proposal (e.g. *"add tomogachi pets to the web UI"*). The right-hand state
diagram **stays parked on the `main` room**, rendering the full static graph with
its full fan-out of intents. It shows neither **where we came from** nor **where
we can go from here** — exactly the two things an operator watching a run wants.
The full-graph-on-one-node view is noise: it answers "what is the entire machine"
when the question is "where am I in it."

`StateDiagram.vue` structurally can't answer that today. It tracks a single
`currentStatePath` prefix-matched to a room label (`src/components/StateDiagram.vue:116`)
with **no concept of a previous room or a traveled path**, and it renders every
phase/room of the parsed mermaid source regardless of reachability from the
current node.

## What changes

Replace "render the whole static graph, highlight one node" with a **path +
horizon** view: a compact rendering centred on the current room that shows the
**traveled path** that led here (provenance) and the **immediate outgoing arcs**
we can take next (horizon), with the rest of the graph demoted or collapsed.

All of this is derivable from data **already in the store** — no new trace
fields are required for the render itself:

- **Traveled path** — the ordered sequence of `machine.state_entered` events
  (`src/stores/run.ts:131`), each carrying the landed `state_path`.
- **Current room** — `currentStatePath` (`src/stores/run.ts:28`).
- **Horizon (next arcs)** — the live allowed intents `currentView.intents`
  (`src/stores/run.ts:43`) plus the parsed outgoing edges for the current room
  (`outgoingByRoom`, `src/components/StateDiagram.vue:139`).

## §0 — Precondition: is "parked on `main`" a trace defect?

**This gate runs before any rendering work.** Per `tools/runstatus/CLAUDE.md`, the
viewer exists to validate traces and **must never paper over a trace problem with
a UI fix**. "Parked on `main`" has two candidate causes and they have opposite
fixes:

1. **The trace is correct, the render is poor.** The descent into `proposal`
   *does* emit a `machine.state_entered` whose path the diagram could centre on,
   but the component renders the whole graph parked on the matched node. → This
   proposal (a render change) is the fix.
2. **The trace is wrong.** The proposal descent emits **no** `state_entered`
   that prefix-matches a declared `proposal` room label (e.g. an import/descend
   hop that doesn't surface as an entered state), so `currentStatePath` legitimately
   stays `main`. → The diagram is *correctly* showing `main`; the bug is in the
   trace and **must be fixed there**, not in the view.

**Task 0** is to drive the dev-story "work on a proposal" path and inspect the
emitted events. If cause (2), file/fix the trace gap first (likely a `tracing`-kind
follow-up) and only then apply the render redesign on top of a correct trace.

## Impact

- **Code:** `tools/runstatus/src/components/StateDiagram.vue` — new derived
  state (traveled path, horizon set, demoted/collapsed remainder) and the
  render branch for the new mode. Possibly `src/diagram/parse.ts` if reachability
  ranking is computed in the model.
- **Rendering:** new path/horizon layout; the existing full-graph render stays
  available (it's still the right view for the static "whole machine" question —
  see Non-goals).
- **Data:** none new on the trace side for the render (uses `events`,
  `currentStatePath`, `currentView.intents`). §0 may surface a separate trace fix.
- **Docs on ship:** `docs/runstatus/` (viewer docs) — describe the path/horizon
  mode and when each view is shown.

## Mental model

A **route on a transit map**, not the whole network. You see the stop you're at,
the few stops you just came through (bright, behind you), and the lines you can
board next (the live intents). The full network is still available on demand, but
the default answer to "where am I" is the route, not the atlas.

## Layout

```
Before (parked on main):                 After (path & horizon):

┌──────────────────────────┐             ┌──────────────────────────┐
│ main  ┌───┐ ┌───┐ ┌───┐   │             │  main ──go_idea──▶        │
│  ┌──┐ │ + │ │ + │ │ + │   │             │            proposal       │
│  │  │ │   │ │   │ │   │   │   ──▶       │            ▸ INTAKE        │
│  └──┘ └───┘ └───┘ └───┘   │             │   ┌─ discuss → SEARCH      │
│  …entire static graph,    │             │   └─ quit    → main        │
│   every intent, one node  │             │  (rest of graph collapsed) │
│   highlighted             │             └──────────────────────────┘
└──────────────────────────┘
```

Three concrete directions were prototyped and overflow-checked in
`.artifacts/diagram-options/index.html` (3-tab mockup):

1. **Path & Horizon** — breadcrumb of the traveled path + a hero card for the
   current room + live exit chips. Lightest weight, no SVG.
2. **Ego-graph** — 1-hop neighbourhood as a small SVG: came-from node → current
   node → next nodes, with elbow connectors. Shows topology/direction.
3. **Metro stepper** *(recommended)* — a vertical interchange line: the traveled
   leg bright, the road ahead muted, the current station carrying its live exits
   as pills; collapses the hub's fan-out to a count. Best balances provenance +
   horizon + the multi-stage pipeline shape.

*Lean: ship direction 3 (metro stepper) as the default; keep the full static
graph as a toggle.*

## Where each tier's data comes from

The metro stepper looks like one ribbon, but its three tiers have **different
sources and different epistemic weight** — and the design hinges on not blurring
them (`tools/runstatus/CLAUDE.md`):

| Station tier | Shows | Source | Truth status |
|---|---|---|---|
| **Traveled leg** (bright) | stations behind you | **trace** — ordered `machine.state_entered` (`src/stores/run.ts:131`) → visited phases | ground truth: the run went there |
| **Current station** (amber) + exit pills | where you are + the live next arcs | **live view** — `currentView.intents` (`src/stores/run.ts:43`) × the room's parsed outgoing edges (`StateDiagram.vue:139`) | ground truth: what you can do now |
| **Road ahead** (muted) | forward stages not yet reached | **static graph** — parsed `mermaidSource`: forward-reachable phases ordered by `Phase.phaseNumber` / `Room.distance` (`src/diagram/parse.ts:12,32`) | **projection** — what the machine declares, not what ran |

Two rules fall out and are non-negotiable:

1. **The line is a phase spine, not a room sequence.** The room graph is cyclic;
   you can't draw it as a line. Phases *are* the authored ordered stages
   (`Phase.phaseNumber`), so stations are **phases** — one per phase on the
   forward path. The current phase expands to its actual room + live exits;
   branches/loops off the spine (`quit → main`) render as **spurs/pills on the
   current station**, never as main-line stops. (dev-story's proposal pipeline is
   one-room-per-phase, so it reads 1:1; the rule is phase-keyed for the general
   multi-room-per-phase case, e.g. bugfix.)
2. **Projection must never look traveled.** Tiers 1–2 are bright/solid because the
   trace proves them; tier 3 stays muted/dashed because it is structural
   projection. Styling an unreached station as traveled is precisely the
   "view implies something the trace doesn't" the viewer forbids.

This also means **tier 3 depends on the proposal pipeline actually being present
in `mermaidSource`.** If the `proposal_*` rooms are an imported sub-graph the viz
doesn't unify with `main`, there are no forward stations to draw — the same
question as §0. So §0 gates the road-ahead, not just the current-room match.

## Rendering changes

- Derive `traveledPath: RoomId[]` from the ordered `state_entered` events
  (dedupe consecutive repeats; map each landed `state_path` to a room via the
  existing longest-prefix match).
- Derive `horizon: {intent, targetRoom}[]` from `currentView.intents` joined to
  the current room's outgoing edges; mark intents with no forward target
  (self-loops / `quit`) distinctly.
- Derive `spineAhead: Phase[]` — the forward-reachable phases from the current
  phase, ordered by `phaseNumber` (fallback `Room.distance`), rendered muted.
  When the forward graph has **no unique spine** (≥2 phases at equal distance,
  i.e. a genuine branch), **do not fabricate a line**: drop to traveled + current
  + 1-hop horizon and surface a "+N branches ahead" affordance instead.
- Render the path/horizon layout from these derived structures in the Vue SFC —
  **data-driven, no string-built markup**; the SVG variants compose `<polyline>`
  /`<text>` from coordinates, never concatenated strings.
- Collapse rooms not on the path and not in the horizon into a single
  "+N elsewhere" affordance; full graph remains reachable via toggle.

## Input & commands

| Command / key | Does | Notes |
|---|---|---|
| view toggle | switch path/horizon ↔ full static graph | default = path/horizon when a current room is known; full graph when not |
| click a horizon arc | highlight its target room (existing `select` emit) | reuses `highlightedStatePaths` (`src/stores/run.ts:327`) |

## Rendering tests

Web equivalent of the combined-I/O rule — deterministic, no LLM:

- **`diagram-path-horizon.test.ts`** (Vitest) — given a fixture event stream
  (`main` → `proposal` `state_entered`s) + a `currentView.intents`, assert the
  derived `traveledPath` and `horizon` are exactly the expected rooms/arcs.
  Verify it **fails** before the derivation exists.
- **SVG text-containment gate** — for the SVG directions, a Playwright check that
  calls `getBBox()` on every `<text>` and asserts it fits inside its group's
  `<rect>` (2-unit inset), exiting non-zero on any overflow. This caught the real
  banner overflow during mockup review; it should gate the shipped component, not
  eyeballing. (Prototype lived as `tools/runstatus/_svgcheck.mjs` during design.)
- **`diagram-parse.test.ts`** — extend if reachability/ranking moves into
  `parse.ts`.

## Migration plan

The full static-graph render stays; the path/horizon mode is added alongside and
becomes the default when a current room is resolvable. No data migration. If §0
finds a trace gap, that fix lands first and independently.

## Tasks

```
## 0. Diagnose (gate)
- [ ] 0.1 Drive dev-story "work on a proposal"; capture the emitted events
- [ ] 0.2 Decide: render-only (cause 1) or trace gap (cause 2). If (2), file a
          tracing follow-up and fix the trace BEFORE proceeding.

## 1. Derive
- [ ] 1.1 traveledPath from state_entered sequence (Vitest, fails-first)
- [ ] 1.2 horizon from currentView.intents × outgoing edges

## 2. Render
- [ ] 2.1 Path/horizon layout in StateDiagram.vue (metro stepper default)
- [ ] 2.2 Collapse off-path/off-horizon rooms; full-graph toggle

## 3. Prove + document
- [ ] 3.1 Vitest derivation tests (verified to fail without the change)
- [ ] 3.2 SVG text-containment gate wired in for SVG directions
- [ ] 3.3 Manual run of the dev-story case; screenshot the new diagram
- [ ] 3.4 Write docs/runstatus/ section; delete this proposal
```

## What we lose, honestly

The full-graph view answered "what is the entire machine at a glance," and some
operators read a run by scanning the whole topology. Demoting it to a toggle costs
that at-a-glance overview. Mitigation: keep the toggle one click away and show the
collapsed "+N elsewhere" count so the full size is never hidden, only deferred.

## Open questions

1. **Default when no current room resolves** — fall back to the full static graph,
   or render an empty path with just the entry node? *Lean: full graph (current
   behaviour) so nothing regresses for traces that open mid-stream.*
2. **How far back does the path go** — full history, or last N rooms with a
   "…" elision? *Lean: last 4–5, elide the rest, since the hub is usually the root.*
3. **Where does reachability ranking live** — derived in the SFC, or promoted into
   `parse.ts`'s model? *Lean: SFC first; promote only if a second consumer needs it.*
4. **Branching forward graph** — when there's no unique phase spine (multiple
   phases at equal forward distance), is the "+N branches ahead" fallback enough,
   or do we want a small fan (current → each next phase) instead of a line?
   *Lean: fallback for v1; a fan is a later refinement.*

## Non-goals

- The Go TUI's diagram (this is the web viewer only).
- Replacing or restyling the full static-graph render — it stays as a toggle.
- Any change that adjusts the *view* to compensate for a missing/incorrect trace
  event (forbidden by `tools/runstatus/CLAUDE.md`); §0 routes that to a trace fix.
- The broader view-mode framework: the `trace-introspection` epic's
  [`trace-view-modes.md`](trace-view-modes.md) proposes co-equal Tree / Timeline /
  **Graph** modes over the event stream. This proposal is the narrower, shippable
  fix for the *current* diagram's parked-on-`main` defect; if `trace-view-modes`
  lands first, this folds into its Graph mode rather than standing alone.
