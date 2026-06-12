# TUI: Embed the kitsoki web UI in a VS Code extension

**Status:** Draft v2. Extension not built yet; the **full-editor demo
mechanism is PoC-validated** — a real VS Code window is driven + webview-
traversed + recorded to MP4 in `.artifacts/vscode-poc/` (see *PoC* below).
**Kind:**   tui (operator surface) — carries one **frontend-transport** seam
(a `postMessage` `DataSource`), one **packaging** concern (a new TypeScript
extension subproject under `tools/`), and one **demo-harness** concern
(tour-driven full-editor videos), all called out under Impact.
**Epic:**   — standalone

<!--
  This delivers the already-shipped runstatus web UI (docs/tui/web-ui.md) as
  a native VS Code surface. It is NOT the inverse `/ide` work
  (ide-integration.md / docs/architecture/transports.md §7), where terminal
  kitsoki dials OUT to a running editor over the Claude Code MCP lock-file
  protocol to rent the editor's capabilities. Here the editor hosts kitsoki's
  own UI. The two are complementary and share no code.
-->

## Why

kitsoki has two interactive heads on one orchestrator body: the terminal TUI
and the browser web UI (`docs/tui/web-ui.md`). Both live *outside* the editor
where the operator actually works. To drive a story, watch its trace, or edit
a room, you alt-tab to a terminal or a browser tab — chat, tracing, and the
room view sit in a different window from the code they're about. There is no
way to keep a kitsoki session, its live trace, and the repo it operates on in
one focused workspace.

A VS Code extension closes that gap: chat in the sidebar, the trace/state
diagram in the bottom panel, all themed to match the editor, all bound to the
workspace's `stories/` and `.kitsoki/`. It is the natural home for an operator
who is already a developer.

And the extension must be **demoable the way the web UI already is.** kitsoki
records deterministic, no-LLM tour videos of the browser UI today
(`kitsoki-ui-demo` skill). The editor experience — chat sidebar, trace panel,
the whole workbench in one frame — is its own selling point, so the same
tour-driven recording must capture the **full VS Code window**, not just the
embedded SPA. This proposal treats that demo capability as in-scope, not an
afterthought (see *Tour-driven demo videos of the full editor*).

## What changes

A new extension subproject `tools/vscode-kitsoki/` ships a VS Code extension
that **embeds the existing runstatus SPA in a webview** and drives it against a
`kitsoki web` backend the extension spawns and owns. One sentence: **the
runstatus SPA becomes a third head — same Vue app, same JSON-RPC/SSE protocol,
relayed into the editor instead of served to a browser tab.**

The architecture (chosen — see *Architecture decision* below): bundle the
built SPA into the extension and load it via `asWebviewUri` under a strict CSP;
the extension host spawns `kitsoki web --addr 127.0.0.1:0` as a child process
on a free port and acts as a **thin relay**, forwarding the SPA's RPC calls and
SSE subscriptions over `postMessage` to that localhost server. The Go server,
its JSON-RPC method set (`internal/runstatus/server/server.go`), and the SSE
contract are **unchanged**. The only frontend change is a third `DataSource`
implementation behind the existing factory (`tools/runstatus/src/data/source.ts:186`).

## Impact

- **Code (new subproject):** `tools/vscode-kitsoki/` — the extension
  (`extension.ts` activation, a `WebviewViewProvider` for the chat sidebar, a
  backend-lifecycle manager, the `postMessage` ↔ localhost relay). TypeScript,
  bundled with esbuild/Vite, packaged with `@vscode/vsce`.
- **Code (frontend seam):** `tools/runstatus/src/data/` — add `BridgeSource`
  (a third `DataSource`, sibling to `LiveSource`/`SnapshotSource`); extend the
  `createDataSource()` factory (`source.ts:186`) to detect the webview host
  (`acquireVsCodeApi`) and return it. No other SPA code changes — `BridgeSource`
  satisfies the same interface (`source.ts:86`), so every store
  (`stores/run.ts`, `meta.ts`, `inbox.ts`) is untouched.
- **Demo harness (new):** `tools/vscode-kitsoki/tests/` — a Playwright
  `_electron` spec that launches the *real* VS Code binary, injects the existing
  tour manifest into the chat webview, drives the editor beat-by-beat, and
  records the **whole window** via ffmpeg. Reuses the `kitsoki-ui-demo`
  post-production (MP4/GIF/contact-sheet, chapter sidecar) and the
  `kitsoki-ui-qa` vision gate unchanged; extends the `kitsoki-ui-demo` skill
  with a "full-editor" mode.
- **Code (build):** the SPA already builds to a single inlined `index.html`
  via `vite-plugin-singlefile`; the extension build copies that artifact in
  (the same `make web` staging the Go embed uses). Set Vite `base: './'` for
  webview-relative asset resolution.
- **Backend:** none. `kitsoki web` already binds an arbitrary addr
  (`--addr`, `cmd/kitsoki/web.go:223`) and assumes trusted localhost. `--addr
  127.0.0.1:0` for an OS-assigned free port; parse the chosen port from stdout.
- **Docs on ship:** `docs/tui/vscode-extension.md` (operator + architecture
  reference); cross-link from `docs/tui/web-ui.md` and `docs/tui/README.md`.

## Architecture decision

Two real options; research grounds the choice (VS Code Webview API; how
Continue / Cline / Roo Code ship; the localhost-iframe constraints).

**(A) Bundle SPA + extension relays to a spawned backend — CHOSEN.** Load the
built SPA via `asWebviewUri` under `default-src 'none'` + per-load nonce. The
SPA never makes a cross-origin request; its `BridgeSource` posts RPC/SSE
intents to the extension host, which holds the only HTTP/SSE connection to the
spawned `kitsoki web`. This is the universal production pattern across
comparable AI extensions (Continue, Cline, Roo Code all bundle + `postMessage`;
none ship a localhost iframe outside dev HMR). It gives the tightest CSP,
native theming (no cross-origin barrier), and reuses kitsoki's DI seam — the
relay is a fourth `DataSource` transport in spirit.

**(B) iframe at `asExternalUri(localhost)`.** Embed `<iframe
src="http://127.0.0.1:PORT">` (widening webview CSP `frame-src`). The SPA runs
**entirely unchanged** — SSE/HTTP work natively because they're same-origin
*to the iframe page*. Tempting, but: weaker editor integration (outer-webview
↔ inner-iframe `postMessage` is cross-origin and awkward — theming sync and
command wiring get hard), a wider CSP (larger attack surface), and a child you
still must lifecycle-manage. Rejected for v1; recorded as the fallback if the
relay's per-call overhead proves material.

Both options spawn the same child process and are **desktop-only** — the web
host (vscode.dev/github.dev) runs the extension in a browser WebWorker with no
`child_process`, so neither can spawn a Go binary there. The web host is a
non-goal (a hosted `kitsoki web` could serve vscode.dev later; out of scope).

## Mental model

The editor is a third head on the orchestrator body — exactly the "second head"
framing from `web-ui.md`, moved inside VS Code. The orchestrator doesn't know
or care which head drives it; the TUI, the browser, and the extension all call
`Turn`/`SubmitDirect` and render the `TurnOutcome`. The extension host is a
**dumb postal relay** between the webview and a localhost `kitsoki web`: it
adds no business logic, holds no session state, and could be deleted without the
backend noticing. Every interpretive decision still lands in the trace, byte
for byte, because the backend is the same process the browser talks to.

## Layout

```
VS Code window
┌──────────┬───────────────────────────────────────────────┐
│ Activity │  editor: your story YAML / code                │
│   bar    │                                                │
│  [≈]     ├───────────────────────────────────────────────┤
│ kitsoki  │  ╭ Kitsoki (sidebar WebviewView) ────────────╮ │
│          │  │  chat transcript + room view (typed elems) │ │
│          │  │  > input / ▸ choices                        │ │
│          │  ╰────────────────────────────────────────────╯ │
├──────────┴───────────────────────────────────────────────┤
│ Panel  [ Kitsoki Trace ]  trace events · state diagram    │
│        (bottom WebviewView — wide, good for the timeline)  │
└───────────────────────────────────────────────────────────┘
```

Chat → a tall sidebar `WebviewView` in a custom Activity Bar container
(`contributes.viewsContainers` → `activitybar`). Trace + state diagram → a wide
bottom-panel `WebviewView` (`contributes.viewsContainers` → `panel`). The SPA
already routes these surfaces (`InteractiveView` chat, `RunView` trace —
`tools/runstatus/src/router.ts`); each webview loads the same bundle at the
appropriate hash route.

## Transport: the postMessage relay

The SPA's `DataSource` (`source.ts:86`) already abstracts every backend
interaction behind ~20 typed methods (`listSessions`, `sendTurn`, `subscribe`,
`getTranscript`, …). `BridgeSource` implements that interface by serialising
each call to a `postMessage` envelope; the extension host translates envelopes
back into the **identical JSON-RPC calls** `LiveSource` makes today.

```
 webview (BridgeSource)            extension host (relay)        kitsoki web
 ───────────────────────          ──────────────────────        ────────────
 sendTurn(sid, "go")  ──post──▶  POST /rpc session.turn  ──────▶  Orchestrator
                      ◀─post───  ◀──────── TurnResult ───────────
 subscribe(sid, cb)   ──post──▶  GET /rpc/events (EventSource)
   onEvent(e) ◀─post────────────  ◀═══════ SSE frames ═══════════  (live trace)
```

- **Request/response:** each `DataSource` method → one `{id, method, params}`
  envelope; the relay awaits the JSON-RPC reply and posts `{id, result|error}`
  back. A shared `id` map resolves the promise in the webview.
- **Streaming (SSE):** `subscribe()` / `turnStream()` open a relay-side
  `EventSource` (the extension host is Node — `fetch`/SSE to localhost work
  natively); each frame is posted to the webview as `{stream, event}`. This is
  why bundle+relay beats iframe-less direct `fetch`: the sandbox forbids the
  webview document from opening cross-origin SSE, but the **host** has no such
  limit, and the existing reconnect/backfill logic (`transport/jsonrpc.ts`,
  `LiveSource.turnStream` at `live-source.ts:262`) is preserved by reusing the
  same code paths on the host side.
- **Artifacts:** `artifactUrl(handle)` (`source.ts:166`) returns
  `/artifact/<handle>`; under the bridge it resolves through the relay (post →
  host fetch → `data:`/blob URL, or a registered `localResourceRoots` path) so
  media still renders under CSP.

## Theming

The webview inherits VS Code's theme automatically via the `--vscode-*` CSS
custom properties and the `vscode-light`/`vscode-dark`/`vscode-high-contrast`
body classes. The SPA gains a thin theme shim: map its existing color tokens to
`var(--vscode-editor-background)`, `var(--vscode-editor-foreground)`,
`var(--vscode-focusBorder)`, etc., so a theme switch reflows instantly with no
extension round-trip. This is additive — the browser build keeps its own
palette; the shim only activates under the webview host. (Do **not** adopt the
archived `@vscode/webview-ui-toolkit`; use the raw variables.)

## Backend lifecycle

The extension owns one `kitsoki web` child per workspace:

- **Spawn** on first webview resolve: `kitsoki web --addr 127.0.0.1:0 --db
  <workspace>/.kitsoki/sessions.db`, story dir resolved from `.kitsoki.yaml` /
  `./stories` exactly as the CLI does (`cmd/kitsoki/web.go:156`). Parse the
  bound port from the server's startup line.
- **Pass-through posture flags** (`--flow`, `--host-cassette`, `--stories-dir`)
  read from extension settings / env at spawn, so the **same no-LLM determinism
  the web tour relies on** is reachable in the editor — this is what makes a
  reproducible full-editor demo possible (the recorder drives the extension; the
  extension spawns the backend in the flow/cassette posture). No backend change:
  `kitsoki web` already accepts these (`cmd/kitsoki/web.go`).
- **Locate the binary:** prefer a `kitsoki.binaryPath` setting, else `kitsoki`
  on `PATH`, else surface a clear "build kitsoki / set the path" error.
- **Forward** stdout/stderr to an `OutputChannel` ("Kitsoki").
- **Reuse** the one child across both webviews (chat + trace share a session).
- **Dispose:** kill the child in `deactivate()` and on the last webview close;
  register on `context.subscriptions`. Sessions are in-memory in `kitsoki web`
  (`web.go`), so the `--db` store is what survives a respawn.

## Tour-driven demo videos of the full editor

kitsoki records deterministic, no-LLM tour videos of the **web** UI by driving
`kitsoki web` (in the `--flow`/`--host-cassette` posture) through
Playwright/Chromium, reusing one tour manifest for both the live overlay and the
recording (`kitsoki-ui-demo` skill; golden example
`tools/runstatus/tests/playwright/agent-actions-video.spec.ts` +
`src/tour/agent-actions-manifest.ts`). The extension must be demoable the same
way — but the frame is the **whole editor** (activity bar, chat sidebar, trace
panel, status bar), not just the embedded SPA.

The pipeline has four layers; **two carry over unchanged, two swap**:

| Layer | Web tour today | Full-editor tour |
|---|---|---|
| **Driver** | Playwright Chromium `page` on `kitsoki web` | **swap** → Playwright **`_electron`** launches the real VS Code binary (`@vscode/test-electron`'s `downloadAndUnzipVSCode` → `_electron.launch`); poll `app.windows()` for `.monaco-workbench` |
| **Tour content** | `src/tour/*-manifest.ts` injected via `window.__startTourWithSteps(…)`, asserted per step | **reuse** → same manifests, injected into the webview frame (`frameLocator('iframe.webview.ready').frameLocator('iframe[title]')` → `evaluate`) |
| **Determinism** | backend spawned `--flow`/`--host-cassette` | **reuse** → same backend, same flags — the extension forwards the no-LLM posture (Backend lifecycle, above) |
| **Recorder** | Playwright `recordVideo` → `saveVideoAsMp4` | Playwright **`recordVideo`** of the workbench window → the *same* MP4/GIF/contact-sheet + chapter-sidecar + QA post-production; ffmpeg screen-grab is an optional fidelity upgrade |

**Recorder choice — `recordVideo` is the deterministic default; ffmpeg is a
fidelity upgrade (PoC-validated, corrects the earlier lean).** The PoC below
showed `recordVideo` is **window-isolated** — it captures the VS Code window's
own render surface regardless of z-order or what else is on the desktop — which
is exactly the determinism the tour pipeline needs, and it captures the whole
workbench (activity bar, sidebar, editor, panels, webviews); it misses only the
OS title bar. ffmpeg `avfoundation` (macOS) / `x11grab`+Xvfb (Linux) buys that
title bar and true native chrome, but captures the **screen**, not the window —
on a shared desktop the PoC's ffmpeg pass caught an overlapping window. So
ffmpeg is reserved for a dedicated Space / clean CI box where the window is
guaranteed frontmost and unobstructed (and, on macOS, Screen-Recording
permission is granted). Both feed the same
`docs/skills/kitsoki-ui-demo/scripts/{webm-to-mp4,webm-to-gif,contact-sheet}.sh`
and `ChapterRecorder` sidecar unchanged. Record **headed** (macOS native; Xvfb
on Linux), VS Code **pinned**, throwaway `--user-data-dir`/`--extensions-dir`,
fixed window size — the same "deterministic recording" discipline the skill
documents (pacing/no-broad-kill), and the `app.close()`-to-flush step the PoC
needed before transcoding.

**The reach-into-the-webview detail that makes manifest reuse work (PoC-proven):**
VS Code nests a webview in two iframes; Playwright descends both with
`frameLocator` (auto-waiting, more reliable than Selenium's manual frame switch)
and `evaluate`s the existing `window.__startTourWithSteps(...)` injection inside
it. The PoC asserted `<h1>Kitsoki Demo Workspace</h1>` from inside a built-in
Markdown-Preview webview via `iframe.webview.ready >>> iframe[title]`. So the
chat tour runs identically; the *editor* beats around it (open the kitsoki
activity-bar view, reveal the trace panel) are driven on the outer workbench
`Page`, and can be staged deterministically via `app.evaluate(() =>
vscode.commands.executeCommand(…))` to remove command-palette timing flakiness.

The chapter sidecar (`<video>.chapters.json`) and the vision QA gate
(`kitsoki-ui-qa`) operate on the MP4 + per-step frames, so both apply unchanged
— the same tour can be QA'd against the same scenarios whether it's the web
build or the full editor. Net: this is the existing demo pipeline with the
launcher and recorder swapped, captured as a **"full-editor" mode** of the
`kitsoki-ui-demo` skill rather than a parallel system.

### PoC — proven end-to-end (the only genuinely new mechanism, validated)

The frontend seam and the relay are conventional; the one unproven claim was
"drive + record the *whole* VS Code window deterministically." A standalone
harness now proves it on this machine — **`.artifacts/vscode-poc/`**
(gitignored; re-run: `cd .artifacts/vscode-poc && node record-vscode-poc.mjs`):

- **Launched real VS Code** (pinned **1.96.4** via `downloadAndUnzipVSCode`) with
  Playwright **`_electron`** and drove deterministic beats — open a file, Command
  Palette → "View: Toggle Word Wrap", open a second file, Markdown Preview to the
  side, toggle the bottom panel — at ~1.5–3 s dwells.
- **Reached into a webview** and asserted `<h1>Kitsoki Demo Workspace</h1>` from
  inside the Markdown-Preview iframe (`iframe.webview.ready >>> iframe[title]`).
- **Recorded the full workbench** via `recordVideo` → ffmpeg transcode:
  **`vscode-poc.mp4`**, h264/yuv420p **1280×800 · 30 fps · 24.8 s · ~530 KB**,
  valid/playable, with six `frame-*.png` thumbnails + `webview-proof.png` showing
  the activity bar, Explorer, editor, and the live Markdown-Preview webview.

Validated stack: macOS 26.5.1 arm64 · Node 22.20 · `playwright` 1.60.0 ·
`@vscode/test-electron` 3.0.0 · ffmpeg 8.1.1.

Gotchas the harness pins down (fold into the skill's "full-editor" mode):

1. **Strip `VSCODE_*` env before launch** — inherited vars (from a parent VS
   Code / integrated terminal) hang the launch or break webviews.
2. **`app.firstWindow()` is flaky** — poll `app.windows()` for the one whose body
   has `.monaco-workbench`.
3. **`app.close()` is required to flush** the `recordVideo` `.webm` before
   transcoding (mirrors the web pipeline's "capture `video` before `context.close`").
4. **`resolveCliArgsFromVSCodeExecutablePath` was not needed** — passing the
   Electron binary + our own flags worked.
5. **ffmpeg `avfoundation` captures the screen, not the window** — needs a
   dedicated Space / frontmost window (and macOS uses physical pixels, so crop
   geometry is `logical × deviceScaleFactor`); this is why `recordVideo` is the
   default. (See *Recorder choice* above.)

Launch args that worked: `--no-sandbox --disable-gpu-sandbox --disable-updates
--skip-welcome --skip-release-notes --disable-workspace-trust --disable-telemetry
--user-data-dir=<tmp> --extensions-dir=<tmp> <workspace>`, `recordVideo: { dir,
size: {1280,800} }`, env stripped of `VSCODE_*`.

## Testing

No real LLM, no real editor, no cost (CLAUDE.md; memory: no-llm-tests):

- **Vitest (frontend)** — `BridgeSource` against a fake `postMessage` peer that
  scripts envelopes ↔ replies; assert it satisfies the `DataSource` contract
  and that streaming frames fan out to `onEvent`. The factory's host-detection
  branch (`source.ts:186`) gets a unit test.
- **Extension unit tests** — the relay against a stub HTTP/SSE server standing
  in for `kitsoki web` (mirrors how the SPA's `live-source` is exercised);
  assert envelope→JSON-RPC translation, SSE fan-out, id correlation, and child
  lifecycle (spawn → port parse → dispose) with a fake spawn.
- **End-to-end (gated, optional)** Playwright `_electron` driving the real
  extension against `kitsoki web --flow <fixture> --host-cassette <…>` — the
  same deterministic, no-LLM machinery the Playwright web-chat e2e uses
  (`tools/runstatus/tests/playwright/web-chat.spec.ts`, `web-ui.md` §4). Drive
  idle→…→terminal **in the webview frame** (descend the two iframes), assert the
  state badge and trace rows. Gated because it needs a (pinned) VS Code download;
  the Vitest + relay-unit layers are the always-on contract. **This e2e harness
  is the same launcher the demo recorder uses** — the assertion run and the
  recorded run share one spec, exactly as the web tour's fast/record modes do.

## Tasks

```
## 1. Frontend transport seam
- [ ] 1.1 BridgeSource implements DataSource over a postMessage envelope codec
- [ ] 1.2 createDataSource() detects the webview host (acquireVsCodeApi) → BridgeSource
- [ ] 1.3 Vite base:'./' for the webview build; theme shim (--vscode-* tokens)
- [ ] 1.4 Vitest: BridgeSource contract + streaming fan-out + factory branch

## 2. Extension subproject (tools/vscode-kitsoki/)
- [ ] 2.1 Scaffold: package.json (contributes.viewsContainers/views), esbuild bundle, vsce
- [ ] 2.2 WebviewViewProvider(s): chat sidebar + trace panel; strict CSP + nonce; asWebviewUri
- [ ] 2.3 Backend lifecycle manager: spawn `kitsoki web --addr 127.0.0.1:0`, port parse, OutputChannel, dispose
- [ ] 2.4 postMessage ↔ JSON-RPC/SSE relay (reuse LiveSource call shapes on the host)
- [ ] 2.5 Commands: open chat / open trace / restart backend; binaryPath setting

## 3. Full-editor demo capability  (mechanism proven — see PoC, .artifacts/vscode-poc/)
- [x] 3.0 PoC: _electron launches real VS Code, webview frame-traversal asserted,
          full window recorded to a valid MP4 (record-vscode-poc.mjs)
- [ ] 3.1 Productionize the launcher into tools/vscode-kitsoki/tests/: pinned
          downloadAndUnzipVSCode + _electron.launch (clean user-data/extensions dir,
          fixed size, VSCODE_* env stripped); one spec shared by the e2e + record runs
- [ ] 3.2 Reach into the kitsoki chat webview (frameLocator x2) and inject the existing
          src/tour/*-manifest.ts via window.__startTourWithSteps; drive editor beats
          (activity-bar view, trace panel) on the workbench Page
- [ ] 3.3 Wire the recording into the kitsoki-ui-demo post-production
          (MP4/GIF/contact-sheet) + ChapterRecorder sidecar; recordVideo default,
          ffmpeg screen-grab as the dedicated-display fidelity option
- [ ] 3.4 Extend the kitsoki-ui-demo skill with a "full-editor" mode; QA via kitsoki-ui-qa

## 4. Prove + document
- [ ] 4.1 Extension unit tests (relay + lifecycle against a stub server)
- [ ] 4.2 Gated _electron e2e (fast/assert mode) over a flow + host-cassette fixture
- [ ] 4.3 Record the full-editor tour at watch-speed; verify frames; QA gate
- [ ] 4.4 Manual run; screenshot the sidebar + panel in light/dark
- [ ] 4.5 Write docs/tui/vscode-extension.md; cross-link; trim/delete this proposal
```

## What we lose, honestly

- **Desktop-only.** No vscode.dev/github.dev (WebWorker host can't spawn the Go
  child). A hosted backend could fix this later; not in v1.
- **A relay hop on every call.** `postMessage` adds a serialise/await per RPC vs
  the browser's direct `fetch`. Expected to be imperceptible against
  oracle-bound turn latency; if a hot read path (trace backfill) shows it,
  option B (iframe) is the recorded escape hatch.
- **No deep editor wiring in v1.** Selection-aware prompts, open-file actions,
  and diagnostics belong to the **inverse** `/ide` substrate
  (`ide-integration.md`); this proposal only *embeds* the UI. Composing the two
  (an embedded UI that also rents editor capabilities) is a deliberate
  follow-up, not v1 scope.

## Open questions

1. **Single child per workspace, or per webview?** *Lean: one child shared by
   both webviews* — chat and trace are the same session; two backends would
   fork the session store. Multi-root workspaces → one child per root, keyed by
   workspace folder.
2. **Bundle the SPA at extension-build time, or fetch from a `kitsoki`
   subcommand?** *Lean: bundle* — the SPA is already a single inlined
   `index.html`; copying it in (the `make web` staging path) keeps the
   extension self-contained and versioned with the binary it expects.
3. **Marketplace packaging vs. internal `.vsix` only.** *Lean: `.vsix` /
   internal for v1*; Marketplace is a release decision once the UX is proven.
4. **Where does the full-editor demo run?** macOS has no true headless, so the
   recorded run is **headed** there; Linux/CI uses Xvfb. *Lean: record headed on
   a dev box (as the web tour is run today), keep the fast/assert e2e mode the
   only CI gate.* Open: whether to also wire an Xvfb recording job in CI.
5. **`@mshanemc/vscode-test-playwright` (a ready `_electron`+VS Code fixture) vs.
   hand-rolling the launch from `@vscode/test-electron` helpers.** *Lean:
   hand-roll the thin launcher* — the harness is beta; we only need
   `downloadAndUnzipVSCode` + `_electron.launch` + a frame-traversal helper.
   Revisit if it stabilises.

## Non-goals

- **Not** the `/ide` editor-awareness work (`ide-integration.md`) — that is
  kitsoki dialing *out* to an editor; this is the editor hosting kitsoki's UI.
  Complementary, separate code.
- **Not** a web-host (vscode.dev) extension — desktop only; see *What we lose*.
- **Not** a fork of or change to the runstatus SPA's surfaces — it embeds the
  existing app behind the existing `DataSource` seam; no new views, no new RPCs.
- **Not** a backend change — `kitsoki web` is reused as-is over `--addr`.
- **Not** a hosted/multi-user deployment — same trusted-localhost model as
  `web-ui.md`; the child binds `127.0.0.1` only.
