/**
 * State-diagram SHOWCASE feature-spotlight tour manifest.
 *
 * A self-contained step array for the dedicated state-diagram video demo. Like
 * agent-actions-manifest.ts, this tour opens on the home story library,
 * introduces the dev-story design pipeline, drives a fresh run via a
 * route-match action step (home → new session), then walks the StateDiagram's
 * four route-centric views AS IMPLEMENTED:
 *   1. Metro stepper — the traveled leg (TRACE), the amber current station
 *      (LIVE) + horizon pills, and the muted road ahead (PROJECTION).
 *   2. Ego-graph — the same 1-hop neighbourhood as a node-link SVG.
 *   3. Path & Horizon — breadcrumb provenance + current-room hero + live chips.
 *   4. Full — the whole static machine.
 *
 * SINGLE SOURCE OF TRUTH: the same array drives both
 *   1. the diagram-showcase overlay (window.__startTourWithSteps), and
 *   2. the Playwright video spec
 *      (tests/playwright/diagram-showcase.spec.ts).
 * The video asserts each step's `title` against the live popover, so the two
 * cannot silently drift.
 *
 * Like manifest.ts this file MUST stay free of Vue / Pinia / DOM-runtime
 * imports — plain types and data only. Every `target` / `waitForTarget` here is
 * a testid the StateDiagram (and the universal home / observer surfaces)
 * actually ships; see src/components/StateDiagram.vue and ViewModeTabs.vue. The
 * diagram-view steps are route "any" and the video's pre-step hooks switch the
 * matching diagram tab so the spotlighted testid is on screen.
 */

import { type TourStep } from "./manifest.js";

// Re-export so the Playwright spec can import the step type alongside the array
// from this one module (mirrors agent-actions-manifest.ts).
export type { TourStep };

export const DIAGRAM_SHOWCASE_TOUR_STEPS: readonly TourStep[] = [
  // ── Intro: the story library → a fresh run (tour-driven navigation) ─────────
  {
    id: "dsg-intro-home",
    route: "home",
    title: "Start at the story library",
    body: "Every run begins here, in the story library — each card is a deterministic story graph kitsoki runs the same way every time. We'll showcase the state diagram on the kitsoki-dev design pipeline.",
    placement: "center",
    kind: "explain",
    advance: "next",
    waitForTarget: "home-view",
    dwellMs: 5000,
  },
  {
    id: "dsg-intro-story",
    route: "home",
    target: "story-card",
    waitForTarget: "story-card",
    title: "The design pipeline",
    body: "This story walks a design from intake through searching, brief, drafting, and published. As the run moves between rooms, the state diagram redraws your route live from the trace.",
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },
  {
    id: "dsg-intro-start",
    route: "home",
    target: "new-session-btn",
    waitForTarget: "new-session-btn",
    title: "Spin up a run",
    body: "Click New session to create a fresh, independently-traced run of the pipeline.",
    placement: "right",
    kind: "action",
    advance: "route-match",
    advanceRoute: "interactive",
    dwellMs: 3500,
  },

  // ── 1. Metro stepper (default view) ─────────────────────────────────────────
  {
    id: "dsg-metro-overview",
    route: "any",
    target: "diagram-metro",
    waitForTarget: "diagram-metro",
    title: "The state diagram is your route",
    body: "A “metro stepper” centred on where the run is — derived entirely from the trace.",
    placement: "left",
    kind: "explain",
    advance: "next",
    dwellMs: 6000,
  },
  {
    id: "dsg-metro-traveled",
    route: "any",
    target: "diagram-metro-station",
    waitForTarget: "diagram-metro-station",
    title: "Where you've been",
    body: "The bright leg is ground truth from the trace — each stop labelled with the intent that got you there (via go_idea, via discuss …).",
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },
  {
    id: "dsg-metro-current",
    route: "any",
    target: "diagram-current-station",
    waitForTarget: "diagram-current-station",
    title: "Where you are",
    body: "The amber station is the current room with its declared phase banner; the pills are the live moves available right now.",
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },
  {
    id: "dsg-metro-horizon",
    route: "any",
    target: "diagram-horizon-pill",
    waitForTarget: "diagram-horizon-pill",
    title: "Click a next-move to see where it leads",
    body: "Each pill highlights its target room across the views and the timeline.",
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 4000,
  },
  {
    id: "dsg-metro-road-ahead",
    route: "any",
    target: "diagram-road-ahead",
    waitForTarget: "diagram-road-ahead",
    title: "Where you can go",
    body: "The muted, dashed road ahead is projection from the static graph — declared, not yet travelled.",
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },

  // ── 2. Ego-graph (node-link) ────────────────────────────────────────────────
  {
    id: "dsg-ego",
    route: "any",
    target: "diagram-ego",
    waitForTarget: "diagram-ego",
    title: "The same neighbourhood as a node graph",
    body: "Came-from → you-are-here → the rooms each live move leads to, with directed elbow connectors.",
    placement: "left",
    kind: "explain",
    advance: "next",
    dwellMs: 6000,
  },

  // ── 3. Path & Horizon (breadcrumb + chips) ──────────────────────────────────
  {
    id: "dsg-path",
    route: "any",
    target: "diagram-path-horizon",
    waitForTarget: "diagram-path-horizon",
    title: "Or as a breadcrumb + live chips",
    body: "Provenance on top (room + via-intent), the current room as a hero card, the live exits as chips.",
    placement: "left",
    kind: "explain",
    advance: "next",
    dwellMs: 6000,
  },

  // ── 4. Full — the whole static machine ──────────────────────────────────────
  {
    id: "dsg-full",
    route: "any",
    target: "diagram-tabs",
    waitForTarget: "diagram-tabs",
    title: "The whole machine is always one click away",
    body: "“Full” flips to the entire static graph — every phase and room — and back to your route.",
    placement: "bottom",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },

  // ── Wrap-up ─────────────────────────────────────────────────────────────────
  {
    id: "dsg-done",
    route: "any",
    target: "diagram-metro",
    waitForTarget: "diagram-metro",
    title: "Path & Horizon",
    body: "Provenance, live moves, and projection — one feature, four views, all from the trace. Hit '?' anytime to replay this tour.",
    placement: "left",
    kind: "explain",
    advance: "next",
    dwellMs: 6000,
  },
];
