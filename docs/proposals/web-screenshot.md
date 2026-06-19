# TUI: Web screenshot — the browser view of any state as a PNG

**Status:** Draft v1. Nothing implemented yet.
**Kind:**   tui
**Epic:**   [mcp-studio.md](mcp-studio.md) (slice 4)

## Why

The MCP studio promises to show the agent **the web view** of a state, not just
the terminal one (`render.web`, slice 7). The terminal side is covered by the
frame composer + `kitsoki shot` (slices 1/3). The web side has **no reusable
path**: the only way to rasterize the `kitsoki web` SPA today is the skills-only
Playwright harness — `tools/runstatus/snap.ts` (a one-off `page.screenshot`) and
`tests/playwright/_helpers/server.ts`/`demo.ts` (spawn `kitsoki web --flow …
--addr … --db …`, poll health, capture). That harness is TypeScript test code
invokable only from inside a Playwright spec; nothing in the engine or CLI can
say "screenshot the browser view of this state."

This slice extracts that proven, **no-LLM** harness into a reusable seam so the
studio (and a `kitsoki web-shot` CLI) can hand the agent a PNG of the real
browser rendering — typed-view elements, chrome, theme — of any session state.

## What changes

**One sentence:** a reusable `internal/webshot` seam (and a `kitsoki web-shot`
command) that boots a headless, flow-driven `kitsoki web` for a target
`(story, state, world)` — or points at the live studio session — drives a
headless browser through the maintained descendant of `snap.ts`, and returns a
PNG, with the same deterministic `--flow`/`--host-cassette` posture the
Playwright skills already rely on.

## Impact

- **Code seams:** new `internal/webshot/` (the Go seam: boot/locate a `kitsoki
  web` for the state, invoke the browser helper, return PNG bytes); a maintained
  `tools/runstatus/web-shot.ts` promoted from `snap.ts` (the browser/screenshot
  half); new `cmd/kitsoki/web_shot.go`. Reuses the web server
  (`internal/runstatus/web/web.go:26`, go:embed SPA) and the no-LLM web driving
  path (`kitsoki web --flow`).
- **Rendering:** no new layout — it photographs the **real** SPA
  (`ViewElement.vue`/`ChatTranscript.vue` rendering the TypedView), shared
  decision 4. Nothing hand-rolled.
- **Dependency posture:** the browser is a Node/Playwright (or headless-chrome)
  dependency, already present for the skills; this slice makes it a **maintained,
  documented** dependency of `web-shot` rather than ad-hoc test tooling. (See open
  Q1 — Playwright-helper-shelled-from-Go vs. a Go `chromedp` path.)
- **Docs on ship:** `docs/tui/` (web screenshot note, sibling to the `kitsoki
  shot` note); cross-link the `kitsoki-ui-demo` skill (which keeps owning *video*
  tours).

## Mental model

The skills already prove every hard part: boot `kitsoki web` no-LLM, drive it to
a state, screenshot it deterministically. This slice **captures that as a value**
instead of leaving it inside a `.spec.ts`: one `(story, state, world)` in, one
PNG out — the web twin of `kitsoki shot`.

```
(story, state, world)  ──▶  headless kitsoki web  ──▶  headless browser  ──▶  PNG
   or: live studio session     (--flow / --host-cassette,      (web-shot.ts,
       (slice 7 handle)          no LLM, --addr :ephemeral)      page.screenshot)
```

Two sources for "the state to shoot":

- **Live session (primary):** the studio's session handle (slice 7) is already
  driving a session; `render.web` screenshots *that* session's current web view.
  Needs the session reachable over a web server (bind a headless `kitsoki web` to
  the same session store/key).
- **Spec (secondary):** an explicit `(story, state, world)` with no live session —
  boot a fresh flow-driven `kitsoki web` that reaches the state deterministically,
  shoot, tear down.

## Rendering changes

- **`internal/webshot.Shot(ctx, spec) ([]byte, error)`** — the Go seam. `spec`
  is either a live session reference (store key + session id) or
  `{story_path, state, world}`. It ensures a `kitsoki web` is serving that state
  (reuse a pooled server per story, or boot+teardown), then calls the browser
  helper at the served URL and returns PNG bytes.
- **`tools/runstatus/web-shot.ts`** — the maintained browser helper (promoted
  `snap.ts`): launch headless Chromium, set a fixed viewport (the
  `DEMO_VIEWPORT` 1600×900 the skills use), `goto` the URL, wait for the SPA to
  settle (the existing `_helpers` settle logic), `page.screenshot({path})`. A
  thin CLI: `web-shot.ts --url … --out … [--viewport WxH]`.
- **Determinism:** the served `kitsoki web` runs `--flow`/`--host-cassette` so no
  LLM is hit; the screenshot is a function of (story, state, world, viewport).
  capture viewport == render viewport (memory: the rrweb invariant).

## Rendering tests

Per CLAUDE.md / `rendering-tests`, each verified to **fail before** the change
(no `webshot` seam exists today):

- **Seam returns a PNG of a known state** — `webshot.Shot` for `stories/bugfix`
  at a known state produces a decodable, non-empty PNG of the expected viewport
  size. No LLM (flow/cassette-driven server).
- **Deterministic** — two shots of the same `(story, state, world, viewport)` are
  byte-stable (or stable within a documented tolerance if AA/font rendering
  varies) — guards against flaky captures.
- **No-LLM** — the booted server uses `--flow`/`--host-cassette`; a test asserts
  no live harness/oracle call occurs during a shot.
- **Live-session path** — a session driven via slice 7 to state X, then
  `render.web`, screenshots that session's view (state badge / current room
  matches `session.inspect`).

## Migration plan

`snap.ts` becomes `web-shot.ts` (the skills' one-off becomes the maintained
helper); the Playwright `_helpers/server.ts` server-lifecycle logic is the
reference for the Go seam's boot/health/teardown. The `kitsoki-ui-demo` skill is
unchanged — it keeps owning *video* production; this slice owns *single-frame
state* capture.

## Tasks

```
## 1. Render
- [ ] 1.1 tools/runstatus/web-shot.ts: maintained CLI (url → png), promoted from snap.ts
- [ ] 1.2 internal/webshot.Shot(ctx, spec): ensure kitsoki web for the state, invoke helper, return PNG
- [ ] 1.3 Spec path: boot a fresh --flow-driven web to reach (story, state, world); teardown
- [ ] 1.4 Live path: bind a headless web to an existing session store/key (for slice-7 render.web)

## 2. Drive
- [ ] 2.1 cmd/kitsoki/web_shot.go: `kitsoki web-shot <story> --state … [--world @w.json] -o out.png`

## 3. Prove + document
- [ ] 3.1 Rendering tests above (no-LLM; each verified to fail without the seam)
- [ ] 3.2 docs/tui/ web-screenshot note; update the epic slice row
```

## What we lose, honestly

A real browser dependency in a CLI path. `kitsoki shot` (slice 3) is pure Go;
`web-shot` needs Chromium + the Node helper (or `chromedp`). The trade is that
the agent sees the *actual* SPA a human sees, not a re-implementation — and the
dependency already exists for the skills. We make it honest and maintained rather
than pretend the web view can be rasterized in-process.

## Open questions

1. **Playwright-via-Node vs. Go `chromedp`.** Shelling the maintained
   `web-shot.ts` reuses the skills' proven settle/curtain/viewport logic but
   keeps a Node dependency; `chromedp` is pure Go but re-implements page-settle
   and loses parity with the skills. *Lean: shell the Playwright helper first
   (parity + least surprise vs. the skills), revisit `chromedp` only if the Node
   dependency proves painful in distribution.*
2. **Pool a web server per story, or boot per shot?** Boot-per-shot is simplest
   but slow (server spin-up + browser launch each call). *Lean: a small per-story
   server pool keyed by `(story, harness)` with idle teardown; `render.web`
   (slice 7) calls into it. Defer the pool if boot-per-shot is fast enough in
   practice.*
3. **Headless web for an arbitrary `(state, world)`** — can `kitsoki web` start a
   session *at* a state, or must a flow drive it there? *Lean: drive there via a
   short flow/replay (the deterministic path the specs already use); a "start at
   state X" web entrypoint is a larger ask, deferred.*

## Non-goals

- Video / tour production — the `kitsoki-ui-demo` skill owns that; this is a
  single still.
- A new web layout or component — it photographs the shipped SPA (shared
  decision 4).
- Terminal rasterization — slice 3 (`kitsoki shot`).
- Cross-process live SSE of the shot session — [`hybrid-session-driving`](hybrid-session-driving.md).
