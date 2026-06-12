# Plan being verified — Embed the kitsoki web UI in a VS Code extension

## What changed

The kitsoki web UI (the chat + trace SPA normally served by `kitsoki web` in a
browser) is now embedded INSIDE a VS Code extension. The extension spawns the
`kitsoki` backend as a child of the extension host, relays the SPA's JSON-RPC
over `postMessage` through a `BridgeTransport`, and serves the same single-file
SPA into VS Code WebviewViews:

- a **Chat** WebviewView in a dedicated **Kitsoki** Activity Bar container
  (sidebar), and
- a **Kitsoki Trace** WebviewView in the bottom **panel**.

Everything runs in ONE VS Code window: the operator's code and kitsoki side by
side. The embed is themed to the editor (dark VS Code chrome; the SPA inherits
`--vscode-editor-background` / `-foreground` via an additive theme shim).

The demo is recorded against a deterministic, **no-LLM** backend: the
`weather-report` story under `flows/tour.yaml`, whose `starlark_http_cassette`
replays every HTTP call (geocode + forecast). No model, no network — same input,
same frames.

## What the operator should now SEE inside one VS Code window

1. **A Kitsoki sidebar in the Activity Bar** rendering the **story library** —
   the "Weather & Climate" story card — themed to the dark editor chrome (not a
   browser tab; the embed lives in the editor).
2. **The story's source open in the editor** (`stories/weather-report/app.yaml`)
   WITH the Kitsoki chat sidebar still mounted beside it — "your code and kitsoki
   in one workspace".
3. **A session started from the sidebar** ("New session" on the story card) →
   the interactive chat view, opening in the **lobby** room with its intent
   picker (a free-text forecast / climate field).
4. **A turn driven and the state advancing** — submitting a "Tokyo" forecast
   advances the room **lobby → report**; the resolved **"Tokyo, Japan"** forecast
   report (place, coordinates, current conditions, 5-day table) renders in the
   chat. This is the no-LLM cassette replay producing real, derived output.
5. **The trace surfaces** rendering for the driven session — a **state diagram**
   (the room graph with the current station marked) and a **trace timeline** with
   a `host.starlark.run` row — the audit record that is kitsoki's whole point.
6. **The dedicated Kitsoki Trace bottom panel** opening as a first-class editor
   surface, themed to the editor chrome.

## What this is NOT

Not a browser screenshot, not a mockup, not a live-LLM run. The evidence must
show the kitsoki UI rendered **inside the VS Code chrome** (Activity Bar, editor
pane, bottom panel) — the whole point is the embed, not the UI in isolation.
