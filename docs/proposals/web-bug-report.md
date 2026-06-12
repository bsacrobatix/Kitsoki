# TUI: One-Click "Report bug" from the Meta menu

**Status:** Draft v1. Nothing implemented yet.
**Kind:**   tui (web operator surface) — carries one **runtime** seam (the
capture/anonymize/write RPC + the artifacts-folder convention), called out
under Impact. Single focused proposal; if the anonymizer grows its own
ruleset surface, split it into a `runtime` child.
**Epic:**   — standalone

## Why

Filing a useful bug against a running web session today is all friction. The
operator either (a) opens the existing agentic `kitsoki.bug` / `story.bug`
meta modes (`internal/app/builtin_meta_modes.go:71-109`) and *describes* the
problem in prose, or (b) drops to a shell and runs `kitsoki bug create`
(`cmd/kitsoki/bug.go:86`). Neither captures **what was actually on screen or
on the wire** — the screenshot and the network trace that turn "it broke" into
a reproducible report. By the time anyone looks, the session is gone.

The web UI is the one surface where that evidence is cheap to grab: the page
is rendered in a browser, and the runstatus server mediates every RPC/SSE call
the app makes. We should let the operator file a complete, evidence-backed bug
in one click.

## What changes

Add a **"Report bug"** item to the Meta dropdown
(`tools/runstatus/src/components/meta/MetaButton.vue:44-63`). Clicking it
captures a screenshot of the current page and the recent network trace (HAR),
anonymizes both, and posts them to a new backend RPC that files a bug under
`issues/bugs/` with the artifacts colocated in a per-ticket artifacts folder.
One sentence: **the Meta menu can now file a screenshot + anonymized HAR bug
without leaving the page.**

This is distinct from the existing `*.bug` *agentic* modes — those open a chat
overlay for a conversation. "Report bug" is a deterministic capture-and-file
action, not a conversation: it files a raw evidence packet and returns. Triage
(dedup, severity, enrichment) is handled **async** afterward by the existing
dogfood pipeline — no agent hand-off in the click path.

## Impact

- **Code (web):** `tools/runstatus/src/components/meta/MetaButton.vue` (new
  menu item, capture orchestration); `tools/runstatus/src/data/live-source.ts`
  (new `reportBug` RPC client, alongside `metaStream`); a small capture helper
  module for screenshot + HAR assembly.
- **Code (backend, runtime seam):** new RPC `runstatus.bug.report` in
  `internal/runstatus/server/server.go` (registered near the `runstatus.meta.*`
  handlers, ~line 845); a HAR ring-buffer recorder wrapping the RPC/SSE
  transport; a deterministic anonymizer package; the file-writer that reuses /
  extends `kitsoki bug create` (`cmd/kitsoki/bug.go`).
- **Issues convention:** a new **per-ticket artifacts folder** alongside the
  flat `issues/bugs/<id>.md` — documented in `issues/README.md` and
  `docs/stories/bugs.md`.
- **Docs on ship:** `docs/tui/web-ui.md` (the Meta-menu surface) and
  `issues/README.md` (artifacts-folder layout).

## Mental model

The Meta menu is the operator's "I want to do something *about* this session"
drawer. "Report bug" is the camera shutter: it freezes the page and the recent
wire traffic, scrubs anything sensitive, and drops a labelled evidence packet
into the shared backlog — no typing required, no context lost.

## Layout

```
Meta ▾ (bottom-right)
┌──────────────────────────┐
│ ✦ Edit story             │
│ ✦ Story Q&A              │
│ ✦ Kitsoki help           │
│ ──────────────────────── │  ← divider
│ 🐞 Report bug            │  ← NEW
└──────────────────────────┘

After click (inline, non-modal toast in the launcher):
┌──────────────────────────┐
│ Capturing… screenshot ✓  │
│            HAR ✓ scrub ✓  │
│ Filed issues/bugs/        │
│   2026-…-web-….md  [open] │
└──────────────────────────┘
```

The item is hidden in snapshot/artifact (read-only) mode, exactly like the
existing Meta button hide logic (`MetaButton.vue`).

## Rendering changes

The new menu item is a list entry rendered by the same `v-for` over menu items
in `MetaButton.vue:44-63` — no hand-rolled markup, it follows the existing item
shape. The progress/result toast is a small typed status block driven by the
capture state machine (`idle → capturing → scrubbing → filed | error`), not ad
hoc strings. Everything else in the launcher is unchanged.

## Input & commands

| Command / key | Does | Notes |
|---|---|---|
| Meta ▾ → **Report bug** | Capture screenshot + HAR, anonymize, file ticket | Hidden in read-only/snapshot mode |
| `[open]` on result toast | Reveal the filed `issues/bugs/<id>.md` | Best-effort; copies path if no reveal host |

Capture pipeline (client → server):

1. **Screenshot** — client-side. *Lean:* `html2canvas` over the app root,
   producing a PNG of the rendered DOM (no extra browser permission prompt).
   See Open question §2 for the `getDisplayMedia` alternative.
2. **HAR** — *Lean:* server-side ring buffer. The runstatus server already
   mediates every `/rpc` and `/rpc/meta-stream` call
   (`internal/runstatus/server/server.go`); a bounded recorder keeps the last N
   request/response pairs and serializes them as a HAR 1.2 archive on demand.
   This is far more faithful than page-JS Resource-Timing reconstruction, which
   cannot see request/response bodies. The client sends only a capture token;
   the server attaches the HAR.
3. **Anonymize** — deterministic, server-side, before anything is written:
   strip `Authorization`/`Cookie`/`Set-Cookie` headers and known session-token
   query params; redact absolute paths under `$HOME`; redact configured
   secret-shaped values. Ruleset lives in one package and is unit-tested
   against fixture HARs.
4. **File** — `runstatus.bug.report` writes `issues/bugs/<id>.md` (reusing the
   `kitsoki bug create` body/frontmatter logic, `cmd/kitsoki/bug.go:154-190`)
   plus a sibling artifacts folder containing `screenshot.png` and `har.json`,
   and links them from the ticket body. Returns `{id, path}`.

### Per-ticket artifacts folder

Tickets are flat `issues/bugs/<id>.md` today; `host.local_files.ticket` and the
dogfood reader glob `*.md` (`issues/README.md`). To keep that discovery intact,
artifacts go in a **sibling** folder, not a folder-form ticket:

```
issues/bugs/
├── 2026-06-12T…Z-web-….md          ← still a flat .md, still globbed
└── 2026-06-12T…Z-web-….artifacts/  ← NEW, ignored by the *.md glob
    ├── screenshot.png
    └── har.json
```

The ticket body gets a `## Artifacts` section linking the two files relatively.
*Lean:* sibling `<id>.artifacts/` over a `<id>/` folder so the existing `*.md`
ticket reader needs zero changes (verify against `host.local_files.ticket`).

**The ticket and its artifacts are committed to the repo** for now — same as the
hand-filed seeds already in `issues/bugs/`. This is the deliberate v1 stance
(evidence is in-repo, immediately searchable by the dogfood app); it raises the
anonymization stakes (§ "What we lose" + Open question §3), to be revisited
before kitsoki is released widely.

## Rendering tests

The web surface is Vue, not the Go TUI, so the concurrent-I/O rendering-test
rule doesn't bind here. Coverage instead:

- **Component test** (`tools/runstatus/`, mirroring the
  `OperatorQuestionModal` test from commit d035b42) — Report-bug item renders,
  is hidden in read-only mode, and drives the capture state machine to a filed
  result against a mocked `reportBug` RPC.
- **Backend test** — `runstatus.bug.report` writes the `.md` + `.artifacts/`
  pair and returns the id; **anonymizer test** asserts auth headers, cookies,
  and `$HOME` paths are scrubbed from a fixture HAR (deterministic, no LLM, no
  network — per CLAUDE.md).
- **Playwright** — extend an existing spec (e.g. `meta-mode.spec.ts`) to click
  Report bug against a throwaway repo and assert the ticket + artifacts land.

## Tasks

```
## 1. Backend seam
- [ ] 1.1 HAR ring-buffer recorder over the RPC/SSE transport (bounded)
- [ ] 1.2 Anonymizer package + fixture-HAR unit tests (auth/cookie/$HOME/secrets)
- [ ] 1.3 `runstatus.bug.report` RPC: write <id>.md (reuse bug.go) + <id>.artifacts/
- [ ] 1.4 issues/README.md + docs/stories/bugs.md: artifacts-folder convention

## 2. Web surface
- [ ] 2.1 "Report bug" menu item in MetaButton.vue (hidden in read-only)
- [ ] 2.2 Client screenshot (html2canvas) + capture state machine + result toast
- [ ] 2.3 reportBug RPC client in live-source.ts

## 3. Prove + document
- [ ] 3.1 Component test (renders/hidden/files) + backend + anonymizer tests
- [ ] 3.2 Playwright: click → ticket + artifacts land in a throwaway repo
- [ ] 3.3 Manual run; screenshot the new surface
- [ ] 3.4 Migrate the Meta-menu surface into docs/tui/web-ui.md; delete this proposal
```

## What we lose, honestly

- **Screenshot fidelity.** `html2canvas` rasterizes the DOM, not the real
  compositor output — CSS it doesn't support (some filters, cross-origin
  images, canvas/video) renders imperfectly. It's "good enough to see what the
  operator saw," not a pixel-perfect capture. `getDisplayMedia` would be exact
  but prompts the user and can capture the wrong window.
- **HAR completeness.** A bounded ring buffer only holds recent traffic; a bug
  whose cause scrolled out of the window won't be in the HAR. We log the buffer
  depth in the archive so reviewers know the horizon.
- **Anonymization is best-effort.** A redaction ruleset catches known shapes; a
  novel secret format can leak. The artifacts are committed to the repo, so
  this is a real risk — §3 below.

## Open questions

1. **Screenshot mechanism:** `html2canvas` (no prompt, approximate) vs.
   `getDisplayMedia` (exact, prompts, can grab wrong surface). *Lean:*
   html2canvas for v1; revisit if fidelity complaints arise.
2. **HAR source.** Server ring buffer (faithful, needs transport wrapper) vs.
   client Resource-Timing reconstruction (no bodies, but zero backend work).
   *Lean:* server ring buffer.
3. **Anonymization hardening (pre-wide-release, not a v1 blocker).** Since
   artifacts are committed (decided: in-repo for now), the redaction ruleset is
   the only thing standing between a missed secret and git history. Before wide
   release we likely want either an operator review-before-file step or a
   stricter allowlist-shaped scrub. v1 ships the deterministic ruleset + a
   "review HAR" link in the result toast; the hardening lands before release.

**Resolved:** triage is async with no agent hand-off in the click path; tickets
and artifacts are committed to the repo for now (to evolve before wide release).

## Non-goals

- The agentic `story.bug` / `kitsoki.bug` conversation modes — unchanged; this
  sits beside them.
- Bug triage, dedup, or resolution workflow (the dogfood pipeline already owns
  that via `stories/kitsoki-dev/`).
- Capturing artifacts from the Go TUI or from `kitsoki run` — web-surface only.
- A general client-side network-capture/devtools panel — we only assemble a HAR
  for the report.
