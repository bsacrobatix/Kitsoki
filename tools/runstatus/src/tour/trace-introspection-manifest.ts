/**
 * Trace-introspection feature-spotlight tour manifest.
 *
 * A self-contained step array for the dedicated trace-introspection video demo.
 * UNLIKE the agent-actions / diagram-showcase tours, this one runs against a
 * FROZEN bugfix-trace SNAPSHOT artifact (no live server, no home story library,
 * no new-session) — so there is NO home intro and NO route-match navigation.
 * The whole tour lives on the loaded trace (route "any"): it opens with a
 * centered welcome on the staged trace, then spotlights each introspection
 * surface AS IMPLEMENTED:
 *   1. Observation kinds — the colored category filter chips + obs-dots.
 *   2. Decision-first detail — the decide verdict block, confidence bar, the
 *      collapsible evidence drawer, and the deterministic-replay affordance.
 *   3. View modes — tree (default), the latency waterfall, and the state graph.
 *
 * Two surfaces from the old scripted walk are deliberately OMITTED as tour
 * steps because their testids do not exist in the static snapshot:
 *   - the home triage table (needs a live home view the snapshot artifact
 *     never mounts), and
 *   - annotation (AnnotateButton is `v-if="isLive"` — absent off a live server).
 *
 * SINGLE SOURCE OF TRUTH: the same array drives both
 *   1. the trace-introspection overlay (window.__startTourWithSteps), and
 *   2. the Playwright video spec
 *      (tests/playwright/trace-introspection-demo.spec.ts).
 * The video asserts each step's `title` against the live popover, so the two
 * cannot silently drift.
 *
 * Like manifest.ts this file MUST stay free of Vue / Pinia / DOM-runtime
 * imports — plain types and data only. Every `target` / `waitForTarget` here is
 * a testid the trace observer actually ships; see TraceTimeline.vue
 * (category-filter-chips, trace-event-row), ViewModeTabs.vue (view-mode-tabs,
 * tab-tree / tab-timeline / tab-graph), TraceWaterfall.vue (waterfall-bar),
 * StateDiagram.vue (diagram-tabs), and the oracle DecideDetail / ConfidenceBar /
 * ReplayButton drawers (decide-verdict, confidence-bar, decide-evidence-toggle,
 * replay-button). The decide/waterfall/graph steps are route "any" and the
 * video's pre-step hooks open the matching surface (expand the decide row,
 * switch the view-mode tab) so the spotlighted testid is on screen.
 */

import { type TourStep } from "./manifest.js";

// Re-export so the Playwright spec can import the step type alongside the array
// from this one module (mirrors agent-actions-manifest.ts).
export type { TourStep };

export const TRACE_INTROSPECTION_TOUR_STEPS: readonly TourStep[] = [
  // ── Welcome on the loaded trace (no home intro — this is a snapshot) ────────
  {
    id: "ti-welcome",
    route: "any",
    title: "Introspecting a frozen trace",
    body: "This is a deterministic bugfix run, loaded from a frozen trace snapshot — no live server, no LLM. Everything you see is derived from the recorded trace, the auditable source of truth. This tour walks the introspection surfaces: observation kinds, decision-first detail, and the three view modes.",
    placement: "center",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },

  // ── 1. Observation kinds ────────────────────────────────────────────────────
  {
    id: "ti-observation-kinds",
    route: "any",
    target: "category-filter-chips",
    waitForTarget: "category-filter-chips",
    title: "Observation kinds",
    body: "Every event carries an observation kind — oracle call, decision, host call, world update, narration. Each kind gets a colored dot on its row and a filter chip up here; toggle a chip to fade that category in or out and isolate the part of the run you care about.",
    placement: "bottom",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },

  // ── 2. Decision-first detail (decide row pre-expanded by the spec) ──────────
  {
    id: "ti-decide-verdict",
    route: "any",
    target: "decide-verdict",
    waitForTarget: "decide-verdict",
    title: "Decision-first detail",
    body: "Expand a decide call and the verdict leads: accept or reject, the chosen intent, and the runner-up moves that were on the table. The decision the judge reached is the first thing you read — not the last thing you scroll to.",
    placement: "left",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },
  {
    id: "ti-confidence",
    route: "any",
    target: "confidence-bar",
    waitForTarget: "confidence-bar",
    title: "Confidence, at a glance",
    body: "The confidence bar quantifies how sure the judge was. A low bar on an accepted verdict is exactly the kind of soft pass an operator wants to eyeball — it's surfaced, not buried in the raw attrs.",
    placement: "left",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },
  {
    id: "ti-evidence",
    route: "any",
    target: "decide-evidence-toggle",
    waitForTarget: "decide-evidence-toggle",
    title: "The evidence behind the call",
    body: "The evidence drawer is collapsed by default so the verdict stays clean — click it to reveal the exact system prompt, prompt, and response the judge saw. Decision first, full provenance one click away.",
    placement: "left",
    kind: "action",
    advance: "click-target",
    dwellMs: 4500,
  },
  {
    id: "ti-replay",
    route: "any",
    target: "replay-button",
    waitForTarget: "replay-button",
    title: "Replay this decision",
    body: "Because the call replays byte-identically from its cassette, the Replay affordance can re-run the recorded decision deterministically — the same prompt, against a chosen operator — with no cost and no drift.",
    placement: "left",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },

  // ── 3. View modes — tree / waterfall / graph ────────────────────────────────
  {
    id: "ti-view-modes",
    route: "any",
    target: "view-mode-tabs",
    waitForTarget: "view-mode-tabs",
    title: "Three views of one trace",
    body: "The same trace renders three ways — the default tree, a latency waterfall, and the state graph. Same source of truth, three lenses; switch with these tabs.",
    placement: "bottom",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },
  {
    id: "ti-waterfall",
    route: "any",
    target: "waterfall-bar",
    waitForTarget: "waterfall-bar",
    title: "Where the wall-clock went",
    body: "The waterfall lays every call out on a time axis — each bar's width is its duration, so the slow oracle turn or the long host call stands out at a glance. The offsets come straight from the trace timestamps, never re-derived.",
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },
  {
    id: "ti-graph",
    route: "any",
    target: "diagram-tabs",
    waitForTarget: "diagram-tabs",
    title: "The run as a state graph",
    body: "The graph view redraws your route through the story's state machine from the trace — where the run has been, where it is, and where it can go. The same machine the metro stepper and ego graph render, here as the whole picture.",
    placement: "bottom",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },

  // ── Wrap-up ─────────────────────────────────────────────────────────────────
  {
    id: "ti-done",
    route: "any",
    title: "All from the trace",
    body: "Observation kinds, decision-first detail with confidence + evidence + replay, and three view modes — every surface derived from one recorded, auditable trace. Hit '?' anytime to replay this tour.",
    placement: "center",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },
];
