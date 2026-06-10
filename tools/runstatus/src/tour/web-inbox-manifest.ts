/**
 * Web async-inbox feature-spotlight tour manifest.
 *
 * A self-contained step array for the dedicated async-inbox video demo. Like
 * agent-actions-manifest.ts, this tour opens on the home story library, frames
 * the demo story, drives a fresh run via route-match action steps (home → new
 * session → chat), then walks the async-inbox feature AS IMPLEMENTED: a
 * `background: true` host call is launched from the chat composer; when the
 * background job reaches a terminal state the runtime auto-posts a completion
 * notification that the server fans out over the global notifications SSE; the
 * SPA chrome's GLOBAL inbox badge increments, a transient toast appears, and
 * clicking the toast teleports the session back to the originating room. The
 * panel surfaces the same item with jump / dismiss affordances.
 *
 * SINGLE SOURCE OF TRUTH: the same array drives both
 *   1. the async-inbox overlay (started via window.__startTourWithSteps), and
 *   2. the Playwright video spec (tests/playwright/web-inbox-video.spec.ts).
 * The video asserts each step's `title` against the live popover, so the two
 * cannot silently drift.
 *
 * Like manifest.ts this file MUST stay free of Vue / Pinia / DOM-runtime
 * imports — plain types and data only. Every `target` / `waitForTarget` here is
 * a testid the inbox surface (InboxBadge / InboxToast / InboxPanel) and the
 * universal chat surface (InputBar) actually ship.
 */

import { type TourStep } from "./manifest.js";

export type { TourStep };

export const WEB_INBOX_TOUR_STEPS: readonly TourStep[] = [
  // ── Intro: the story library → a fresh run (tour-driven navigation) ─────────
  {
    id: "wi-intro-home",
    route: "home",
    title: "Start at the story library",
    body: "Every run begins here, in the story library. We'll demonstrate the async inbox on a tiny story whose only job is to launch background work — exactly the shape that produces a runtime notification.",
    placement: "center",
    kind: "explain",
    advance: "next",
    waitForTarget: "home-view",
    dwellMs: 5000,
  },
  {
    id: "wi-intro-story",
    route: "home",
    target: "story-card",
    waitForTarget: "story-card",
    title: "The async-inbox demo story",
    body: "This story dispatches a long-running task in the background. When the task finishes, the runtime posts a notification — and that is what the global inbox surfaces, no matter which room you're in.",
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },
  {
    id: "wi-intro-start",
    route: "home",
    target: "new-session-btn",
    waitForTarget: "new-session-btn",
    title: "Spin up a run",
    body: "Click New session to create a fresh, independently-traced run of the story and drop into its interactive chat view.",
    placement: "right",
    kind: "action",
    advance: "route-match",
    advanceRoute: "interactive",
    dwellMs: 3500,
  },

  // ── Launch the background work ──────────────────────────────────────────────
  {
    id: "wi-launch",
    route: "interactive",
    target: "intent-btn-start",
    waitForTarget: "intent-btn-start",
    title: "Launch the background task",
    body: "Click start to kick off the background host call. It runs asynchronously — you keep control of the session while it works, and the inbox will tell you when it's done.",
    placement: "top",
    kind: "action",
    advance: "click-target",
    dwellMs: 2500,
  },

  // ── The toast appears (transient — capture it first) ────────────────────────
  // The toast auto-dismisses 6s after the SSE push, so it is the first scene
  // after launch and the spec screenshots it promptly with a short dwell.
  {
    id: "wi-toast",
    route: "interactive",
    target: "inbox-toast",
    waitForTarget: "inbox-toast",
    title: "A toast for the completion",
    body: "For a success or action-required notification the inbox raises a transient toast the instant the background job completes. It auto-dismisses after a few seconds — or you can click it to jump straight to the room the work came from.",
    placement: "left",
    kind: "explain",
    advance: "next",
    dwellMs: 3000,
  },

  // ── The badge increments (SSE-fed) ──────────────────────────────────────────
  {
    id: "wi-badge",
    route: "interactive",
    target: "inbox-badge",
    waitForTarget: "inbox-badge-count",
    title: "The global inbox badge lights up",
    body: "Even after the toast fades the count persists on the global inbox badge — fixed in the chrome, visible on every route. When the background job reached a terminal state the runtime posted a notification, the server fanned it out over the global notifications SSE stream, and this badge incremented. The count is the live unread total.",
    placement: "left",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },

  // ── Open the panel ──────────────────────────────────────────────────────────
  // The toast auto-dismisses, but the badge persists; open the panel for a
  // stable surface that holds the same notification and a durable jump control.
  {
    id: "wi-panel",
    route: "interactive",
    target: "inbox-item",
    waitForTarget: "inbox-panel",
    title: "The inbox panel",
    body: "Opening the badge reveals the full inbox panel: every notification for the run, each with its own jump and dismiss controls. The same teleport target backs the panel item as the toast.",
    placement: "left",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },

  // ── Click jump → teleport back to origin ────────────────────────────────────
  {
    id: "wi-jump",
    route: "interactive",
    target: "inbox-jump",
    waitForTarget: "inbox-jump",
    title: "Jump to the originating room",
    body: "Click 'jump →' on the notification. The inbox teleports the session back to the exact room the background job was launched from — the notification carries that origin state, so a one-click jump lands you where the work happened.",
    placement: "left",
    kind: "action",
    advance: "click-target",
    dwellMs: 4500,
  },

  // ── Wrap-up ─────────────────────────────────────────────────────────────────
  {
    id: "wi-done",
    route: "interactive",
    title: "That's the async inbox",
    body: "A background turn posted a runtime notification, the global badge incremented over SSE, a toast surfaced it, and one click teleported the session back to its originating room — all deterministic, no LLM. Hit '?' anytime to replay this tour.",
    placement: "center",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },
];
