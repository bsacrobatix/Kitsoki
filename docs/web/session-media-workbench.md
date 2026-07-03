# Session Media Workbench

The session media workbench is the browser chat layout for working on a specific
artifact. When a room emits a media `ViewElement` such as an HTML mockup, Slidey
deck, video, image, or markdown-derived artifact, the operator can pin that
artifact beside the conversation instead of leaving every update embedded only in
the transcript.

## Behavior

- Each media element exposes a `Pin` affordance.
- Pinning opens a stable media pane in the live session view.
- The selected media handle is suppressed from the transcript while pinned and
  replaced with a compact receipt, so the conversation keeps chronology without
  mounting duplicate embeds.
- The chat remains live beside the media pane: composer, streaming agent
  activity, routing chips, media annotations, and follow-up turns all continue to
  use the same session and trace.
- The observability pane behaves like devtools. It can show Graph or Trace, dock
  right, dock bottom, float in the window, or pop out to a separate browser
  window through the existing `?surface=graph` / `?surface=trace` entrypoints.
- The layout can switch between vertical and horizontal arrangements and uses
  browser-resizable panes where the user needs extra room.

The workbench is browser-only. The VS Code integration keeps its existing
single-surface decomposition: chat, trace, and graph remain separate dockable
webviews selected by `window.__KITSOKI_SURFACE`.

## Implementation Notes

`ViewElement.vue` remains the one artifact renderer. The pin button dispatches a
`kitsoki:pin-media` browser event with the media handle; `InteractiveView.vue`
selects the matching media element from `store.chatEntries` and renders that same
`ViewElement` in the media pane.

The transcript suppression is intentionally handle-based. It only hides the
currently pinned artifact handle, and only in workbench mode. This preserves
other typed view elements and keeps older or alternate media visible unless the
operator selects them.

## Verification

Automated tests must stay no-LLM. The focused coverage lives in:

- `tools/runstatus/tests/unit/interactive-view-embed.test.ts`
- `tools/runstatus/tests/unit/view-element.test.ts`
- `tools/runstatus/tests/unit/chat-transcript.test.ts`

The feature demo is cataloged as `session-media-workbench` and should be recorded
with the deterministic mockup-video flow. Vision QA for the rendered MP4 should
use the feature plan and scenarios under `.context` or the feature catalog text
as the review target; do not add the vision QA gate to automated tests.
