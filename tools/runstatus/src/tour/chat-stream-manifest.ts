/**
 * Live-chat-streaming feature-spotlight tour manifest.
 *
 * A self-contained step array for the main-chat streaming video demo. Like
 * agent-actions-manifest.ts, this tour opens on the home story library and
 * drives a fresh bug-fix run via route-match action steps — but where the
 * agent-actions tour leaves for the observer, this one STAYS in the main chat
 * (InteractiveView): it kicks off the autofix turn through the chat choice
 * widget, watches the thinking bubble stream the agent's reasoning and tool
 * calls live (cassette slow-play paces the recorded transcript through the
 * turn-stream SSE path in real-ish time), then shows how the finished turn
 * keeps that activity reviewable — collapsed inside the final agent bubble and
 * expandable back to the exact same interleaved feed.
 *
 * SINGLE SOURCE OF TRUTH: the same array drives both
 *   1. the live overlay (started via window.__startTourWithSteps), and
 *   2. the Playwright video spec
 *      (tests/playwright/chat-stream-video.spec.ts).
 * The video asserts each step's `title` against the live popover, so the two
 * cannot silently drift.
 *
 * Like manifest.ts this file MUST stay free of Vue / Pinia / DOM-runtime
 * imports — plain types and data only. Every `target` / `waitForTarget` is a
 * testid the chat surface actually ships: see InteractiveView.vue
 * (thinking-bubble, intent-btn-*) and ChatTranscript.vue (chat-activity,
 * chat-activity-summary, chat-activity-feed).
 */

import { type TourStep } from "./manifest.js";

// Re-export so the Playwright spec can import the step type alongside the
// array from this one module (mirrors agent-actions-manifest.ts).
export type { TourStep };

export const CHAT_STREAM_TOUR_STEPS: readonly TourStep[] = [
  // ── Intro: the story library → a fresh run (tour-driven navigation) ────────
  {
    id: "cs-intro-home",
    route: "home",
    title: "The live agent chat",
    body: "kitsoki's main chat is where an operator drives a run and watches the agent work. This tour shows the live turn stream: the agent's thinking and tool calls arriving in real time, and how that activity stays reviewable after the turn lands. We'll use the bug-fix pipeline.",
    placement: "center",
    kind: "explain",
    advance: "next",
    waitForTarget: "home-view",
    dwellMs: 5000,
  },
  {
    id: "cs-intro-story",
    route: "home",
    target: "story-card",
    waitForTarget: "story-card",
    title: "The bug-fix pipeline",
    body: "This story hands a ticket to an autofix agent that reads the repo, edits code, and runs the build. While that agent works, its native execution stream — reasoning and tool calls — flows straight into the chat you're about to watch.",
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },
  {
    id: "cs-new-session",
    route: "home",
    target: "new-session-btn",
    waitForTarget: "new-session-btn",
    title: "Spin up a run",
    body: "Click New session to create a fresh, independently-traced run. It opens directly in the chat.",
    placement: "right",
    kind: "action",
    advance: "route-match",
    advanceRoute: "interactive",
    dwellMs: 3500,
  },

  // ── The streaming turn ──────────────────────────────────────────────────────
  {
    id: "cs-stream-start",
    route: "interactive",
    target: "intent-btn-start",
    waitForTarget: "intent-btn-start",
    title: "Kick off the autofix turn",
    body: "Drive the run the way an operator does — from the chat choice widget. The submit routes through the live turn-stream path, so the agent's execution stream arrives here in flight, not only after the turn lands.",
    placement: "top",
    kind: "action",
    advance: "click-target",
    dwellMs: 3500,
  },
  {
    id: "cs-stream-watch",
    route: "interactive",
    target: "thinking-bubble",
    waitForTarget: "thinking-bubble",
    title: "Thinking and tools, in arrival order",
    body: "While the turn is in flight the bubble streams the agent's activity exactly as it happens: each 🧠 thought stays ABOVE the tool calls it explains — Read, Grep, then the reasoned Edit and the Bash build — never re-ordered, never pushed to the bottom.",
    // "top" keeps the popover in the empty chat area ABOVE the bubble — a
    // left/overlapping placement would cover the very feed being filmed.
    placement: "top",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },

  // ── After the turn: the activity survives, collapsed ───────────────────────
  {
    id: "cs-collapsed",
    route: "interactive",
    target: "chat-activity",
    waitForTarget: "chat-activity",
    title: "The turn lands — the activity stays",
    body: "When the final view renders, the live bubble dissolves into the agent's reply — but the activity that produced it is NOT thrown away. It collapses into a one-line summary inside the bubble: the thought and tool-call counts, one click away.",
    placement: "top",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },
  {
    id: "cs-expand",
    route: "interactive",
    target: "chat-activity-summary",
    waitForTarget: "chat-activity-summary",
    title: "Expand it anytime",
    body: "Click the summary to reopen the full feed.",
    placement: "top",
    kind: "action",
    advance: "click-target",
    dwellMs: 3000,
  },
  {
    id: "cs-expanded",
    route: "interactive",
    target: "chat-activity-feed",
    waitForTarget: "chat-activity-feed",
    title: "The same feed, preserved",
    body: "The expanded feed is the exact stream you watched live — same 🧠 thoughts, same tool rows, same arrival order. The agent's reasoning never disappears behind its answer; every turn keeps its own audit trail in the conversation.",
    // "right" parks the popover over the trace pane so the tall expanded
    // feed — the payoff frame — stays fully unobscured in the chat column.
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },

  // ── Wrap-up ─────────────────────────────────────────────────────────────────
  {
    id: "cs-done",
    route: "interactive",
    title: "That's the live chat",
    body: "You've seen the full loop: drive a turn from the chat, watch the agent's thinking and tool calls stream in arrival order, and review the same feed — collapsed but never lost — after the answer renders. Hit '?' anytime to replay this tour.",
    placement: "center",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },
];
