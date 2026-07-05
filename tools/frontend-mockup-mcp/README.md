# Frontend Mockup MCP

`tools/frontend-mockup-mcp` is a focused stdio MCP server for browser-based
mockup, wireframe, and visual QA work. It deliberately avoids frontend
debugging surfaces such as console logs, network traces, source maps, and
backend tracing.

The server exposes:

- `mockup_navigate` to open a URL at a review viewport.
- `mockup_visual_qa` to return a screenshot plus compact design context.
- `mockup_dom` to return a token-efficient DOM/layout/accessibility summary.
- `mockup_stagehand_observe`, `mockup_stagehand_extract`, and
  `mockup_stagehand_act` for Stagehand-assisted page understanding and actions.
- `mockup_tour_start`, `mockup_tour_step`, and `mockup_tour_export` to
  interactively author a deterministic, source-first tour that can be replayed
  later to render a demo video.
- `mockup_close` to end the local browser session.

Stagehand LLM calls are routed through `kitsoki agent ask` using
`KITSOKI_MOCKUP_AGENT` and are only used by the explicit Stagehand tools. The
visual QA and DOM tools are deterministic browser reads.

Tour export writes a JSON source file, a Playwright replay spec, and a small
HTML storyboard preview. Commit the reviewed source JSON/spec only when the tour
is intended to become durable; keep intermediate renders under `.artifacts/`.
