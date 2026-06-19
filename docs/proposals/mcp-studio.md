# Epic: MCP studio — author, drive & see kitsoki from an external agent

**Status:** Draft v1. No slices implemented yet.
**Kind:**   epic
**Slices:** 7 (0/7 shipped)

<!-- "MCP studio" = a single, broad MCP server (`kitsoki mcp`) that an
     EXTERNAL LLM client (Claude Code, Claude Desktop, an IDE agent) connects
     to, giving that agent the three things it needs to build a kitsoki story:
     author it, drive a live session of it, and SEE the rendered result —
     terminal and browser — as the agent works. Distinct from the existing
     narrow `kitsoki serve` (one `transition` tool that drives a single app),
     this is the authoring/introspection *control plane*. -->

## Why

A kitsoki story is built by an AI agent and used by a human, and **every bug
only the human sees is one the AI wrote blind** — the framing that motivates
[`ai-collaboration-proposal.md`](ai-collaboration-proposal.md) and the
[`story-qa-agent`](story-qa-agent.md) epic. Today an external coding agent
(the thing actually editing `stories/*/app.yaml`) works through a keyhole: it
can shell out to `kitsoki turn`/`inspect`/`test flows` and read JSON, but it
**cannot** hold an authoring workspace, drive a live session turn-by-turn from
what it just saw, or — the sharp gap — *look at the screen a human would see*.
The richest view it can get, the trace's `view_rendered`, is a width-80,
ANSI-stripped, body-only projection (`machine.go:2228`, `journal_write.go:347`)
that no operator ever sees, and there is no web equivalent at all.

kitsoki already **is** an MCP server in the narrow: `kitsoki serve` exposes one
`transition` tool over the official `modelcontextprotocol/go-sdk`
(`internal/mcp/server.go:86`), and the operator-ask bridge proves the broader
machinery — per-server stdio subprocesses, `mcp__<server>__<tool>` naming,
`writeMCPConfigTempfile` config emission (`internal/host/oracle_helpers.go:49`),
client attach (`attachOperatorAsk`, `operator_ask_bridge.go:205`). The substrate
exists; what's missing is a server whose tools cover the **author → drive →
see** loop and a renderer that returns the *real* screen.

This epic builds that server. An external agent connects to `kitsoki mcp`,
holds a story workspace and one or more live driving sessions as **handles**,
and works the loop natively: edit YAML → `story.validate`/`story.test` (no-LLM)
→ `session.drive "go back to the debug room"` → `render.tui`/`render.web` to see
the assembled terminal frame *and* a screenshot of the browser view of that
exact state. The same machinery the QA agent needs to *use* a story is the
machinery any author needs to *see their work*.

## What changes

Once every slice ships, an external LLM connected to `kitsoki mcp` can:

- **Author directly.** `story.read` / `story.write` / `story.validate`
  (`app.Load` → file:line:col `ValidationError`s, `loader.go:733`) /
  `story.graph` (`graph.RoomList`/`Detail`/`OracleContracts`) / `story.test`
  (`testrunner.RunFlows`, no-LLM) — the agent edits the YAML and gets the
  load-time invariants and flow gate back as structured results, with **zero
  LLM cost**.
- **Drive and introspect a live session.** `session.new`/`attach` opens a
  keyed, trace-backed session; `session.drive` submits **free text** routed
  through `orch.Turn` (live or replay harness); `session.submit`/`continue`
  drive direct intents and slot-fills; `session.inspect`/`trace` return the
  current state, world, allowed intents, and recorded events.
- **See any state.** `render.tui` returns the assembled human screen
  (`{text, ansi, metadata}` Frame, slice 1) at any width; `render.tui_png` and
  `render.web` return **PNG image content** of the terminal and browser views
  for the agent's own vision — so "show me what this room looks like" is one
  tool call, not a screenshot pipeline.

The frame composer, `kitsoki drive`, and `kitsoki shot` — first sketched for
the QA agent — **move here** as this epic's substrate (slices 1–3); the
[`story-qa-agent`](story-qa-agent.md) epic is re-scoped to its one remaining
slice (the QA agent skill), now a **consumer** of this server.

## Impact

- **Spans:** runtime (the server, handle model, session + authoring tools),
  tui (frame composer, terminal + web screenshots), tracing (consumes existing
  trace/recording substrate — no new events).
- **Net surface:** a new `kitsoki mcp` subcommand (`cmd/kitsoki/mcp.go`) +
  `internal/mcp/studio/` server built on the same `modelcontextprotocol/go-sdk`
  as `internal/mcp/server.go`; a new `internal/tui/frame.go` composer; new
  `kitsoki shot` + a reusable headless web→PNG seam extracted from the
  skills-only Playwright harness (`tools/runstatus/tests/playwright/`). Reuses
  `orch.Turn`/`SubmitDirect`/`ContinueTurn`, `buildInspectOutput`,
  `app.Load`/`graph.*`/`testrunner.RunFlows`, and the cassette/recording stack
  verbatim.
- **Docs on ship:** `docs/architecture/` (a new `mcp-studio.md` describing the
  server + tool surface, sibling to `operator-ask.md`),
  `docs/architecture/developer-guide.md` §6, `docs/tui/` (frame composition +
  screenshots), `docs/testing.md` (the no-LLM default).

## Slices

The substrate (1–4) produces the capabilities; the facade (5–7) exposes them as
MCP tools. Slices 1–3 are **absorbed** from `story-qa-agent` (their `**Epic:**`
line re-points here); 4–7 are new.

| # | Slice | Kind | Scope (one line) | Depends on | Status | File |
|---|---|---|---|---|---|---|
| 1 | Frame composer | tui | One composer → the full human screen (body + chrome) as `{text, ansi, metadata}` at any width; the live TUI renders through it too | — | Draft (absorbed) | [`qa-frame-seam.md`](qa-frame-seam.md) |
| 2 | `kitsoki drive` | runtime | Interactive headless driver: persistent trace session, free-text input, `--harness live\|replay`, VCR modes; emits the Frame per turn | 1 | Draft (absorbed) | [`qa-drive-command.md`](qa-drive-command.md) |
| 3 | `kitsoki shot` | tui | ANSI→PNG of a Frame (monospace + color) | 1 | Draft (absorbed) | [`qa-screenshot.md`](qa-screenshot.md) |
| 4 | Web screenshot | tui | Headless render of the **web** view of any state → PNG; reusable seam extracted from the skills-only Playwright harness | — | Draft | [`web-screenshot.md`](web-screenshot.md) |
| 5 | MCP server core | runtime | `kitsoki mcp` stdio server, the handle/workspace model, tool registry, no-LLM default, external-client attach config | — | Draft | [`mcp-server-core.md`](mcp-server-core.md) |
| 6 | Authoring tools | runtime | `story.read/write/validate/graph/test` — direct file primitives over `app.Load`/`graph.*`/`testrunner.RunFlows` | 5 | Draft | [`mcp-authoring-tools.md`](mcp-authoring-tools.md) |
| 7 | Session + render tools | runtime | `session.new/attach/drive/submit/continue/inspect/trace` + `render.tui/tui_png/web` | 5, 1, 2, 3, 4 | Draft | [`mcp-session-tools.md`](mcp-session-tools.md) |

## Sequencing

The substrate and the server core are independent and can start in parallel;
the two tool slices land on top once their substrate exists. Slice 5 is the
keystone of the facade (it defines the handle model and tool namespace every
tool uses).

```
#1 frame ──┬─▶ #3 shot ─────────────┐
           └─▶ #2 drive ────────────┤
#4 web-shot ────────────────────────┤
                                     ▼
#5 mcp-core ──┬─▶ #6 authoring       │
              └─▶ #7 session+render ◀┘  (needs #1 frame, #2 drive, #3 shot, #4 web-shot)
```

Recommended order: **#5 + #1 in parallel** (unblock the most), then #2/#3/#4,
then #6 and #7. #6 (authoring) has no substrate dependency and can land right
after #5 to deliver value early — an agent that can author + validate + test
with no LLM is useful before it can drive or see.

## Shared decisions

These span slices; each child defers here.

1. **The handle model.** An MCP connection is one *studio session*. It owns
   named **handles**: zero or more **driving-session handles** (each a keyed,
   trace-backed kitsoki session over the slice-2 `drive` loop / the runstatus
   `OrchestratorDriver`, `driver.go:79`) and at most one **workspace handle** (a
   story directory under authoring). `session.*` tools take a session handle;
   `story.*` tools take the workspace; `render.*` tools take a session handle or
   an explicit `(story, state, world)` spec. Defined in slice 5; consumed by 6/7.
2. **The `Frame` is the unit of fidelity** (inherited from the QA epic). Defined
   once in slice 1: `text` (ANSI-stripped, agent-readable), `ansi` (styled,
   screenshot-ready), `metadata` (`state`, `allowed_intents`, `mode`,
   `world_digest`, `width`/`height`). Slices 3, 4, and 7 consume it; no consumer
   re-derives the screen.
3. **No-LLM by default; live is opt-in.** The server defaults every driving
   session to `--harness replay --record none` (CLAUDE.md: tools never hit a
   real LLM by default). `story.validate`/`story.test` are always deterministic.
   A session opts into `harness: live` (or VCR `record: new`) explicitly, and
   the server surfaces that mode in the session handle's metadata so the agent —
   and the human watching it — knows when an LLM is in the loop. A replay miss is
   a hard error, never a silent live fallthrough (slice 2 invariant).
4. **Don't fork the renderers.** The frame composer renders the canonical typed
   tree + chrome (slice 1); `render.web` drives the **real** `kitsoki web` SPA
   headlessly (slice 4) — neither invents a second layout path. Hand-rolled Go
   view strings stay forbidden (CLAUDE.md / `rendering-tests`). Fidelity rides on
   [`view-rendering-readability`](view-rendering-readability.md) for free.
5. **Reuse the MCP substrate, don't reinvent it.** Same `modelcontextprotocol/go-sdk`,
   same `StdioTransport`, same `mcp__<server>__<tool>` convention and
   `writeMCPConfigTempfile` attach path as `internal/mcp/server.go` and the
   operator-ask bridge. The new server is a sibling of those, not a new framework.

## Cross-cutting open questions

1. **Render images vs. text-only?** Vision-capable clients (Claude Desktop /
   Code) accept MCP **image content blocks**; a text-only client cannot use a
   PNG. *Lean: every `render.*` tool returns the `Frame.text` (always usable)
   **and** an image block when the client advertises image support; `render.web`
   degrades to "web rendering needs an image-capable client" text otherwise.*
2. **Operator-ask from a driven sub-agent.** When the external agent drives a
   session that dispatches kitsoki's own oracle agents, those agents may call
   `mcp__operator__ask` — but there's no human operator attached to a studio
   session, so the prompter is absent and they proceed solo (the headless path,
   `operator-ask.md`). *Lean: ship that (consistent, safe). Forwarding a driven
   sub-agent's question back to the **external** LLM as a tool result is a
   tempting future tool but out of scope here.*
3. **One server process per client, or a shared daemon?** *Lean: one stdio
   subprocess per connecting client (like `kitsoki serve` / `mcp-operator-ask`),
   holding its handles in-process; cross-process session sharing is the separate
   [`hybrid-session-driving`](hybrid-session-driving.md) concern, not this epic.*

## Non-goals

- **A second view renderer or a hand-rolled web view.** Slices 1/4 capture the
  existing TUI and SPA renderers; making them clean is
  [`view-rendering-readability`](view-rendering-readability.md).
- **Oracle-output cassette fidelity** (converse/decide/task bodies) — the
  parallel cassette-quality work
  ([`oracle-contract-eval.md`](oracle-contract-eval.md)); consumed, not designed.
- **Cross-process / durable session sharing** — [`hybrid-session-driving`](hybrid-session-driving.md)
  and continue-mode's journal own that.
- **Replacing `kitsoki serve`** (the narrow per-app `transition` server) or the
  TUI/web operator surfaces. This is an additive, agent-facing control plane.
- **Bridging operator-ask back to the external client** (cross-cutting Q2).
