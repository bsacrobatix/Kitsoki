# Pellicule

Deterministic spec-driven video generator. A JSON spec describes a video as an
ordered list of scenes; pellicule drives a headless Chrome via Puppeteer to
screenshot each frame, synthesises narration with `edge-tts`, and muxes
everything into an MP4 with ffmpeg.

Same spec + same template → byte-identical frames. No LLM in the rendering
loop.

```
spec.json ──► renderer.js (Puppeteer ➜ PNGs) ──┐
                                               ├──► assembler.js (ffmpeg) ──► out.mp4
              narration.js (edge-tts ➜ MP3s) ──┘
```

For the iteration workflow (when to use `--estimate`, `--scenes`,
`--skip-render`, narration budgeting, common gotchas), see
[`docs/skills/pellicule-pitch-authoring/SKILL.md`](../../docs/skills/pellicule-pitch-authoring/SKILL.md).
This document is the **reference**: pipeline architecture and JSON schema.

## CLI

```
node index.js <input.json> <output.mp4> [options]
```

| Flag | Effect |
|---|---|
| `--fps N` | Frames per second (default 30) |
| `--context key=value` | Override a template variable; repeatable; takes precedence over `meta.context` |
| `--scenes 0,3-5` | Render only the given scene indices (still combined into one MP4) |
| `--list` | Print scene index + duration table; no render |
| `--estimate` | Like `--list` plus narration audio-length estimates and overrun warnings |
| `--frames-dir PATH` | Use this directory for frames instead of a temp dir |
| `--keep-frames` | Keep frame directory after render |
| `--skip-render` | Skip PNG generation, reuse cached frames; regenerate narration + mux only |
| `--capture-log FILE` | Write live HTTP responses to JSON (for later playback freeze) |

## Pipeline

| File | Role |
|---|---|
| `index.js` | CLI; argv parsing; orchestration |
| `renderer.js` | Per-scene dispatch; tracks scene start frames; drives Puppeteer |
| `template.html` | The single HTML page Puppeteer drives — all CSS + the `window.pellicule.*` JS API every scene module calls |
| `scenes/<type>.js` | Per-scene-type render module: `{ render(page, scene, ctx) }` |
| `timing.js` | Frame counts per reveal state name; `estimateScene` / `estimateBoundaries` |
| `narration.js` | Calls `edge-tts` per scene; bundles audio segments aligned to scene start frames |
| `assembler.js` | ffmpeg invocation; muxes audio segments if supplied |
| `runner.js` | API-scene HTTP runner (cyber-repo demo flow; not used by pitches) |

Determinism comes from: viewport pinned at 1920×1080, timing measured in frame
counts not seconds, and the spec being pure data — no LLM is consulted during
rendering.

## Spec schema

A spec is a JSON object with two top-level keys: `meta` (optional) and
`scenes` (required, non-empty array).

```json
{
  "meta": {
    "title": "My Feature",
    "resolution": { "width": 1920, "height": 1080 },
    "narration": { "voice": "en-AU-NatashaNeural", "rate": "+0%" },
    "context": { "host": "stand.example.com", "token": "abc" }
  },
  "scenes": [
    { "type": "title", "title": "My Feature" },
    { "type": "narrative", "eyebrow": "The setting", "body": "..." }
  ]
}
```

### `meta` fields

| Field | Purpose |
|---|---|
| `meta.title` | Pitch label shown on debug overlays; not rendered into the video |
| `meta.resolution` | `{ width, height }`. Default 1920×1080. Changing this is unusual |
| `meta.narration.voice` | edge-tts voice id. Default `en-AU-NatashaNeural` |
| `meta.narration.rate` | edge-tts speech rate, e.g. `"+0%"`, `"-10%"` |
| `meta.context` | Key/value template variables interpolated into scene fields. Overridden by `--context` CLI flags |

A scene also takes a top-level `narration: "..."` string (any scene type). If
**any** scene has `narration`, edge-tts is invoked and the resulting audio is
muxed onto the video, with each segment starting at that scene's start frame.

Scenes also accept `hold: <frames>` to extend the post-reveal dwell. `30
frames = 1s` at default fps. The default hold per scene type lives in
`timing.js` (`narrative_hold`, `diagram_hold`, etc.).

### Scene types

Each scene is an object with a `type` discriminator. Render handlers live in
`scenes/<type>.js`; the per-type fields below are passed through verbatim.

#### `title` — cold open or chapter break

```json
{ "type": "title", "title": "Kitsoki",
  "subtitle": "A conversational workflow engine",
  "eyebrow":  "Demo" }
```

Fixed 3s card. No reveal animation.

#### `narrative` — eyebrow + body + optional lede

```json
{ "type": "narrative",
  "eyebrow": "The problem",
  "body":    "Building this chatbot is harder than it looks.",
  "lede":    "Pure LLM agents drift; hand-coded state machines can't evolve.",
  "hold":    165 }
```

#### `diagram` — ASCII / code-panel comparison (legacy)

```json
{ "type": "diagram",
  "title":   "Rigid CLI vs chat agent",
  "panels":  [{ "label": "CLI", "ascii": "$ git push\n..." }, { ... }],
  "caption": "The CLI knows one phrasing; the agent reads intent." }
```

Up to three panels (`diagram_panel_0..2` reveal slots).

#### `diagram-svg` — proper SVG diagrams (the workhorse)

```json
{ "type": "diagram-svg",
  "title":   "Control inversion",
  "panels": [
    {
      "label":   "Typical agent",
      "viewBox": "0 0 400 360",
      "nodes": [
        { "id":"llm","label":"LLM","sub":"plan · decide",
          "x":100,"y":40,"w":200,"h":80,"style":"primary" },
        { "id":"rt", "label":"runtime","sub":"execute",
          "x":100,"y":240,"w":200,"h":80 }
      ],
      "edges": [
        { "from":"llm","to":"rt","label":"calls" }
      ],
      "caption": "LLM holds the plan."
    }
  ],
  "caption": "The arrow that matters is the LLM's return arrow.",
  "hold":    210 }
```

- **Nodes** support `label` (big), `sub` (medium), and `lines: ["...", ...]`
  (smaller multi-line content). `style: "primary"` (blue) or `"secondary"`
  (purple) highlights the focal box.
- **Edges** auto-anchor to whichever sides of the two nodes face each other
  (horizontal vs vertical chosen by which delta dominates). `side: "left" |
  "right"` offsets the line perpendicular to its direction — used for parallel
  bidirectional arrows.
- **Gates**: an edge with `gate: "test fails"` renders as a dashed orange
  checkpoint bar instead of an arrow, with the label uppercased.

Single-panel vs two-panel layouts differ in scale (single = larger fonts,
fixed 680px SVG height, no panel chrome; two = side-by-side, smaller fonts,
1/0.7 aspect ratio). See the authoring skill for sizing rules.

#### `trace` — routing cascade with HIT/MISS badges

```json
{ "type": "trace",
  "title": "Routing cascade",
  "turns": [
    { "user": "deploy the api", "layers": ["exact", "alias", "ner", "llm"],
      "intent": "deploy", "no_llm": true }
  ],
  "caption": "Three layers resolve before the LLM is touched." }
```

Up to three turns (`trace_turn_0..2`). Each turn shows the user utterance, the
cascade of routing layers tried, the resolved intent, and a no-LLM badge.

#### `thread` — mocked Jira / Bitbucket comment threads

```json
{ "type": "thread",
  "title": "How a fix lands",
  "panels": [
    {
      "system": "jira",
      "ref":    "KIT-123",
      "stage":  "in progress",
      "messages": [
        { "author": "alice", "body": "/kitsoki bugfix" },
        { "author": "kitsoki", "body": "Reproduced. Patch below." }
      ]
    }
  ],
  "caption": "Same flow drives a Jira ticket as drives the TUI." }
```

Up to three panels (`thread_panel_0..2`).

#### `stat` — big gradient number

```json
{ "type": "stat",
  "value":  "78%",
  "label":  "of calls answered without the LLM",
  "detail": "(routing cascade hits before LLM fallback)" }
```

#### `cta` — end card

```json
{ "type": "cta",
  "wordmark": "Kitsoki",
  "tagline":  "Conversational workflows with deterministic flow.",
  "url":      "github.com/acronis/kitsoki" }
```

#### `terminal-gif` — embed a VHS-recorded gif in a terminal chrome

```json
{ "type": "terminal-gif",
  "gif":     "assets/run.gif",
  "title":   "kitsoki run",
  "caption": "Demo of the TUI driving a bugfix flow." }
```

The default `termgif_hold` is 12s — one loop of a typical VHS recording.

#### `request` — API request/response (cyber-repo demo flow)

```json
{ "type": "request",
  "request":  { "method": "POST", "url": "https://{{host}}/api/v1/...",
                "headers": {...}, "body": {...} },
  "response": { "status": 200, "headers": {...}, "body": {...} },
  "mock":     true
}
```

Three modes:

- **Live** (neither `mock` nor `playback`): real HTTP request is made; the live
  response is rendered. Pair with `--capture-log` to save the response for
  later playback.
- **Mock** (`mock: true`): the embedded response is rendered with a MOCK badge.
- **Playback** (`playback: true`): a previously-captured response is rendered
  with a PLAYBACK badge.

Used by the cyber-repo demo flow, not by pitch videos.

## Frame timing

Every reveal step has a frame budget in `timing.js`. The per-type defaults
add up to the scene's total duration when `--list` / `--estimate` is run.
Override the final dwell with a scene-level `hold: <frames>`; per-step
revealing budgets aren't user-overridable.

A scene's estimated duration is computed by `estimateScene(scene)` and is the
sum of the type's reveal-step budgets plus `inter_scene` (24 frames).
`estimateBoundaries(spec)` returns `[{ sceneIndex, startFrame, type, narration,
durationFrames }]` — this is what `--estimate`, `--list`, `--skip-render`, and
narration alignment all use.

Whenever a scene module's reveal sequence changes, the matching branch in
`estimateScene` must be updated, or `--estimate` will lie about durations and
narration alignment will drift.

## Adding a new scene type

1. Add `scenes/<name>.js` exporting `{ render(page, scene, ctx) }`.
2. Register it in `renderer.js`'s `SCENE_MODULES`.
3. Add the HTML region and CSS in `template.html` (`<div id="<name>-region">…</div>`).
4. Add `window.pellicule.show<Name>(scene)` / `hide<Name>()` to the inline JS
   in `template.html`.
5. Add per-reveal frame budgets in `timing.js`, plus a branch in
   `estimateScene()` so `--estimate` knows the duration.
6. Add the reveal state names to the `_PITCH_REVEALS` table in
   `template.html` so the renderer knows when each reveal step has settled.
