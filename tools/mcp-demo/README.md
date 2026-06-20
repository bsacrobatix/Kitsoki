# MCP demo — a coding agent driving kitsoki over MCP (Claude Code TUI POC)

A **tour-driven demo video** of an external coding agent using the kitsoki studio
MCP server (`kitsoki mcp`) end to end: authoring a story, checking it, testing it,
driving a live session, and *seeing* the kitsoki TUI — all over MCP. **Claude Code
(terminal) is the POC**; codex and copilot slot in by swapping a cassette.

It generalizes the VS Code demo→QA pipeline (`tools/vscode-kitsoki`) to a terminal
surface: instead of Playwright filming a real editor, it films an **xterm.js
terminal** replaying a committed **termcast** cassette, through the *same* shared
pipeline — camera (1600×900), `ChapterRecorder` sidecar, 25s duration floor, and
the `kitsoki-ui-qa` gates (blank / pacing / placeholder + the vision review).

## No-LLM by construction

The replay plays a static cassette in a terminal and **never spawns a model or the
live MCP server** — structurally impossible to incur LLM cost (enforced by
`scripts/lint-no-llm.mjs`). The authenticity comes from **record once, replay
forever**: a *single gated* live `claude` ↔ `kitsoki mcp` session is captured to a
cassette, then replayed for free, identically, on every render. The synthetic
cassette in `casts/` is the deterministic default and the QA fixture; it needs no
capture at all.

## Layout

```
casts/                      termcast cassettes (the agent's session, agent-agnostic)
  types.ts                  the termcast format + ANSI helpers
  claude-code.cast.ts       synthetic Claude-Code session (no-LLM default + QA fixture)
  index.ts                  registry + resolveCast() (MCP_DEMO_AGENT / MCP_DEMO_CAST_JSON)
player/
  index.html                xterm.js terminal "window" on a branded studio backdrop
  serve.mjs                 dep-free static server (player + xterm dist), one origin
tests/
  mcp-terminal.e2e.spec.ts  replays a cast beat-by-beat, films it through the pipeline
  _helpers/                 camera.ts + demo.ts + recorder.ts (shared demo contracts)
scripts/
  lint-no-llm.mjs           the no-LLM / camera / chapters gate for this surface
  capture-live.py           the gated PTY recorder (real claude → asciicast .cast)
  segment-cast.mjs          asciicast .cast → draft termcast (curate before committing)
mcp.capture.json            MCP config to register kitsoki mcp for the live capture
```

## Run it (no LLM)

```bash
cd tools/mcp-demo
pnpm install                                  # first time
pnpm run lint:no-llm                           # camera · chapters · replays-cast · no-spawn
pnpm run validate                              # WEB_CHAT_PACE=0 — fast assert (throwaway .fast.mp4)
pnpm run record                                # watch-speed → .artifacts/mcp-demo/claude-code.mp4 (+ .chapters.json)
```

QA the recorded video (the `kitsoki-ui-qa` skill — vision review via the local
`claude` CLI, plus the deterministic scans):

```bash
make mcp-qa            # from the repo root — blank/pacing/placeholder + grounded vision verdict
```

The QA contract lives in `.agents/skills/kitsoki-ui-qa/templates/mcp-feature.md`
and `mcp-scenarios.yaml`.

## The termcast format

A cassette is a list of narrated **beats**; each beat is one chapter in the video
(its id/label become the `chapters.json` rail) and carries the caption to show plus
the terminal chunks to play — `type` (operator keystrokes, char-by-char) or `out`
(agent / tool output, written fast). `data` may contain ANSI. See `casts/types.ts`.

## Add another agent (codex / copilot)

The harness is agent-agnostic. Author a `casts/<agent>.cast.ts` (or capture one, see
below), register it in `casts/index.ts`, then:

```bash
MCP_DEMO_AGENT=codex pnpm run record
```

## Record once (GATED — real LLM, explicit go-ahead only)

This is the **only** step that uses a real LLM. Do it deliberately, never in CI
(AGENTS.md: tests never use a real LLM; live is opt-in). It captures an authentic
Claude-Code session into a cassette that then replays for free.

```bash
# from the kitsoki repo root:
python3 tools/mcp-demo/scripts/capture-live.py \
  --out tools/mcp-demo/casts/claude-code-live.cast -- \
  claude --mcp-config tools/mcp-demo/mcp.capture.json \
         --allowedTools 'mcp__kitsoki__*' \
         "Build a tiny barista story over the kitsoki MCP server: author it, \
          validate the graph, run its flows, then drive a session and show the TUI."

# segment → curate → commit, then point the spec at it:
node tools/mcp-demo/scripts/segment-cast.mjs \
     tools/mcp-demo/casts/claude-code-live.cast > tools/mcp-demo/casts/claude-code-live.json
#   ↑ curate captions / mark the prompt chunk kind:"type" / tune holdMs
MCP_DEMO_CAST_JSON=casts/claude-code-live.json pnpm run record
```

Claude Code maps the dotted tool names to `mcp__kitsoki__story_write`,
`mcp__kitsoki__session_drive`, `mcp__kitsoki__render_tui`, … — hence the
`mcp__kitsoki__*` allowlist (no mid-run permission prompts to pollute the capture).
