/**
 * Weather & Climate — feature-spotlight tour manifest for the
 * host.starlark.run example story.
 *
 * A self-contained step array for the dedicated weather-report video demo.
 * Like agent-actions-manifest.ts, this tour opens on the home story library,
 * frames the weather-report story, drives a fresh run via a route-match action
 * step (home → new session → the interactive /chat view), then walks the
 * feature AS IMPLEMENTED:
 *   the lobby intent picker, a free-text "forecast Tokyo" producing a 5-day
 *   report, the live trace lighting up with the real host.starlark.run call and
 *   its two replayed GETs, a second look-up in climate mode ("climate Oslo"),
 *   the clean fail() path on an unknown place, and the lobby/quit wind-down.
 *
 * Unlike agent-actions (which walks the read-only observer), this story is
 * driven and observed entirely in the INTERACTIVE /chat view: the chat narrates
 * the report while the live trace timeline + diagram sit beside it. So the
 * walk-the-feature steps live on route "interactive" / "any", not the observer.
 *
 * SINGLE SOURCE OF TRUTH: the same array drives both
 *   1. the weather-report overlay (started via window.__startTourWithSteps), and
 *   2. the Playwright video spec
 *      (tests/playwright/weather-report-tour.spec.ts).
 * The video asserts each step's `title` against the live popover, so the two
 * cannot silently drift.
 *
 * Like manifest.ts this file MUST stay free of Vue / Pinia / DOM-runtime
 * imports — plain types and data only. Every `target` / `waitForTarget` here is
 * a testid the interactive view actually ships: see src/views/InteractiveView.vue
 * (home-view, story-card, new-session-btn, chat-section, trace-timeline) and
 * src/components/{InputBar,TraceTimeline}.vue (input-bar, trace-event-row).
 */

import { type TourStep } from "./manifest.js";

// Re-export so the Playwright spec can import the step type alongside the array
// from this one module (mirrors agent-actions-manifest.ts).
export type { TourStep };

export const WEATHER_REPORT_TOUR_STEPS: readonly TourStep[] = [
  // ── Intro: the story library → a fresh run (tour-driven navigation) ─────────
  {
    id: "wr-intro-home",
    route: "home",
    title: "Start at the story library",
    body: "Every run begins here, in the story library — each card is a deterministic story graph kitsoki runs the same way every time. We'll demonstrate host.starlark.run, kitsoki's glue capability, on the Weather & Climate story.",
    placement: "center",
    kind: "explain",
    advance: "next",
    waitForTarget: "home-view",
    dwellMs: 5000,
  },
  {
    id: "wr-intro-story",
    route: "home",
    target: "story-card",
    targetText: "weather",
    waitForTarget: "story-card",
    title: "Weather & Climate",
    body: "This story takes a city and a mode, then runs ONE deterministic Starlark script that geocodes the place and fetches its dataset over ctx.http. No LLM, no hand-written Go — just a small typed glue script whose every HTTP call is captured and replayed from a cassette.",
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },
  {
    id: "wr-intro-start",
    route: "home",
    target: "new-session-btn",
    waitForTarget: "new-session-btn",
    title: "Spin up a run",
    body: "Click New session to create a fresh, independently-traced run of the story. It opens in the interactive view, where you drive the story on the left and watch its live trace on the right.",
    placement: "right",
    kind: "action",
    advance: "route-match",
    advanceRoute: "interactive",
    dwellMs: 3500,
  },

  // ── The lobby: the typed intent picker ──────────────────────────────────────
  {
    id: "wr-lobby",
    route: "interactive",
    target: "input-bar",
    waitForTarget: "input-bar",
    title: "Pick a look-up",
    body: "The run opens in the lobby. The room offers exactly the moves it allows: a free-text 'forecast' field and a 'climate' field — type a city into either. Nothing invalid is possible. Let's ask for a 5-day forecast for Tokyo.",
    placement: "top",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },

  // ── forecast Tokyo: the rendered report (pre-step submits "forecast Tokyo") ──
  {
    id: "wr-forecast",
    route: "interactive",
    target: "chat-section",
    waitForTarget: "chat-section",
    title: "A 5-day forecast, fully derived",
    body: "The report room ran the Starlark script on enter: it geocoded 'Tokyo' to Tokyo, Japan, fetched the forecast over ctx.http, and bound the typed outputs to world keys the view renders — the resolved place, the current conditions, and the 5-day table. Every value here came from the deterministic glue, not a model.",
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },

  // ── The live trace: the real host.starlark.run call ─────────────────────────
  {
    id: "wr-trace",
    route: "interactive",
    target: "trace-timeline",
    waitForTarget: "trace-timeline",
    title: "The live trace — every step recorded",
    body: "Beside the chat, the live timeline records every transition, world update, and host call as a structured, replayable event. This is the audit record: the report you just saw is fully accounted for here.",
    placement: "left",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },
  {
    id: "wr-trace-row",
    route: "any",
    target: "trace-event-row",
    waitForTarget: "trace-event-row",
    title: "host.starlark.run, with its HTTP exchanges",
    body: "Here is the real host.starlark.run invocation. Expand it and its payload carries the __http_exchanges summary — each {method, url, status} GET the script made via ctx.http, replayed byte-for-byte from the cassette. The glue call and the network it rode are both in the trace, not hidden.",
    placement: "left",
    kind: "explain",
    advance: "next",
    dwellMs: 6000,
  },

  // ── climate Oslo: the other mode (pre-step submits "climate Oslo") ──────────
  {
    id: "wr-climate",
    route: "interactive",
    target: "chat-section",
    waitForTarget: "chat-section",
    title: "Same glue, the other mode",
    body: "From the report room you can ask again — this time 'climate Oslo'. The very same script runs in its other branch: it resolves Oslo, Norway and fetches the 2023 month-by-month climate profile (mean temp + precipitation). One script, two datasets, both deterministic.",
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },

  // ── Zzqxville: the clean failure path (pre-step submits an unknown place) ────
  {
    id: "wr-failure",
    route: "interactive",
    target: "chat-section",
    waitForTarget: "chat-section",
    title: "A clean, typed failure",
    body: "Ask for a place that doesn't exist and the script calls fail() — no place found. The on_error arc routes to the failed room with last_error set, so the failure is a first-class, traced outcome with a clear message, not a stack trace or a silent empty report.",
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },

  // ── Wind-down: back to the lobby (pre-step clicks back) ──────────────────────
  {
    id: "wr-lobby-back",
    route: "interactive",
    target: "input-bar",
    waitForTarget: "input-bar",
    title: "Back to the lobby",
    body: "Step back and you're in the lobby again, ready for another look-up — or quit to end the run. Each of those moves is an explicit, allowed intent, and each lands in the trace just like the look-ups did.",
    placement: "top",
    kind: "explain",
    advance: "next",
    dwellMs: 4500,
  },

  // ── Wrap-up ─────────────────────────────────────────────────────────────────
  {
    id: "wr-done",
    route: "any",
    title: "That's host.starlark.run",
    body: "You've seen the full picture: one deterministic Starlark script geocoding and fetching over ctx.http, two modes (5-day forecast and 2023 climate profile), a clean fail() path on a bad place — every HTTP call captured and replayed from a cassette, every step in the live trace. No LLM, no bespoke Go: just typed glue you can audit. Hit '?' anytime to replay this tour.",
    placement: "center",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },
];
