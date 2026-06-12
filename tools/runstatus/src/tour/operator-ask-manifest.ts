/**
 * Operator-ask forwarding feature-spotlight tour manifest.
 *
 * A self-contained step array for the dedicated operator-ask video demo. Like
 * agent-actions-manifest.ts, this tour opens on the home story library, frames
 * the demo story, drives a fresh run via route-match action steps (home → new
 * session → interactive session view), then spotlights the operator-question
 * modal AS IMPLEMENTED: when a dispatched `claude -p` oracle agent forwards an
 * AskUserQuestion into kitsoki, the oracle turn is parked and BLOCKING until the
 * operator answers. The web UI surfaces a hard modal (OperatorQuestionModal.vue)
 * that renders the agent's question + options, lets the operator pick (single-
 * or multi-select), and sends the answer back to unblock the agent.
 *
 * Architecture: docs/architecture/operator-ask.md. Frontend pieces:
 *   components/OperatorQuestionModal.vue, stores/operatorQuestions.ts,
 *   data/live-source.ts (subscribeQuestions / answerQuestion).
 *
 * SINGLE SOURCE OF TRUTH: the same array drives both
 *   1. the operator-ask overlay (started via window.__startTourWithSteps), and
 *   2. the Playwright video spec
 *      (tests/playwright/operator-ask-video.spec.ts).
 * The video asserts each step's `title` against the live popover, so the two
 * cannot silently drift.
 *
 * The modal itself is surfaced deterministically (no-LLM) by the spec via the
 * window.__pushOperatorQuestion demo seam (OperatorQuestionModal.vue onMounted),
 * which injects a realistic OperatorQuestionFrame straight into the store's
 * onFrame(); the answer() local-resolve short-circuit (demo- question_id) lets
 * "Send answer" dismiss the modal without a backend round-trip. The modal
 * rendering + selection UX shown here is exactly what users see in production.
 *
 * Like manifest.ts this file MUST stay free of Vue / Pinia / DOM-runtime
 * imports — plain types and data only. Every `target` / `waitForTarget` here is
 * a testid the operator-question modal (and the session view) actually ships;
 * see OperatorQuestionModal.vue (operator-question-modal, oq-option-*, oq-submit)
 * and InteractiveView.vue (chat-section, current-state).
 */

import { type TourStep } from "./manifest.js";

// Re-export so the Playwright spec can import the step type alongside the array
// from this one module (mirrors how the agent-actions spec imports its manifest).
export type { TourStep };

export const OPERATOR_ASK_TOUR_STEPS: readonly TourStep[] = [
  // ── Intro: the story library → a fresh run (tour-driven navigation) ─────────
  // These home/interactive steps mean the WHOLE video is tour-driven: the intro
  // explains where the feature lives and why before we reach the session view,
  // and the navigation itself (home → new session → interactive) is performed by
  // route-match action steps, not silent spec orchestration.
  {
    id: "oa-intro-home",
    route: "home",
    title: "Start at the story library",
    body: "Every run begins here, in the story library — each card is a deterministic story graph kitsoki runs the same way every time. We'll demonstrate operator-ask forwarding on the bug-fix pipeline.",
    placement: "center",
    kind: "explain",
    advance: "next",
    waitForTarget: "home-view",
    dwellMs: 5000,
  },
  {
    id: "oa-intro-story",
    route: "home",
    target: "story-card",
    waitForTarget: "story-card",
    title: "The bug-fix pipeline",
    body: "This story hands a ticket to a dispatched `claude -p` autofix agent. When that agent hits a fork it can't decide alone, it forwards an AskUserQuestion back into kitsoki — and the oracle turn parks, blocking, until an operator answers.",
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },
  {
    id: "oa-intro-start",
    route: "home",
    target: "new-session-btn",
    waitForTarget: "new-session-btn",
    title: "Spin up a run",
    body: "Click New session to create a fresh run of the pipeline — the surface where the operator drives the run and where a forwarded question will surface.",
    placement: "right",
    kind: "action",
    advance: "route-match",
    advanceRoute: "interactive",
    dwellMs: 3500,
  },

  // ── The session view + the parked agent ────────────────────────────────────
  {
    id: "oa-session",
    route: "interactive",
    target: "chat-section",
    waitForTarget: "chat-section",
    title: "Driving the run",
    body: "This is the session view: the operator drives the pipeline turn by turn. Somewhere inside, the dispatched autofix agent is running native execution — and it's about to hit a decision it can't make alone.",
    placement: "left",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },

  // ── The forwarded question modal (the feature) ─────────────────────────────
  {
    id: "oa-modal",
    route: "interactive",
    target: "operator-question-modal",
    waitForTarget: "operator-question-modal",
    title: "The agent has a question",
    body: "The agent forwarded an AskUserQuestion. kitsoki surfaces it as a hard, blocking modal — no dismiss, no backdrop close — because a real agent goroutine is parked on the answer. The header names the question; below it are the agent's own options, verbatim.",
    placement: "left",
    kind: "explain",
    advance: "next",
    dwellMs: 6000,
  },
  {
    id: "oa-options",
    route: "interactive",
    target: "oq-option-0-0",
    waitForTarget: "oq-option-0-0",
    title: "The agent's options",
    body: "Each option is exactly what the agent offered — its label and an optional description. This is a single-select question: the operator picks one path forward for the blocked agent.",
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },
  {
    id: "oa-pick",
    route: "interactive",
    target: "oq-option-0-0",
    waitForTarget: "oq-option-0-0",
    title: "Pick a path",
    body: "Selecting an option arms the answer. The Send button stays disabled until every question has a selection — kitsoki never forwards a half-answer back to a parked agent.",
    placement: "right",
    kind: "action",
    advance: "click-target",
    dwellMs: 4500,
  },
  {
    id: "oa-send",
    route: "interactive",
    target: "oq-submit",
    waitForTarget: "oq-submit",
    title: "Send the answer",
    body: "Send answer echoes the operator's choice back to the backend keyed by the question_id, which unblocks the exact parked oracle goroutine — and the modal dismisses. The agent resumes with the operator's decision.",
    placement: "top",
    kind: "action",
    advance: "click-target",
    dwellMs: 4500,
  },

  // ── A multi-select example (the second forwarded question) ─────────────────
  {
    id: "oa-multi",
    route: "interactive",
    target: "operator-question-modal",
    waitForTarget: "operator-question-modal",
    title: "Multi-select questions too",
    body: "An agent can forward a multi-select question — 'choose any that apply'. The same modal renders checkboxes instead of radios; the operator can pick several before sending. Here the agent asks which checks to run before it ships.",
    placement: "left",
    kind: "explain",
    advance: "next",
    dwellMs: 6000,
  },
  {
    id: "oa-multi-pick",
    route: "interactive",
    target: "oq-option-0-0",
    waitForTarget: "oq-option-0-0",
    title: "Pick several",
    body: "Toggle as many options as apply — the marks switch from radios to checkboxes. Multi-select answers travel back as a list, single-select as a single label.",
    placement: "right",
    kind: "action",
    advance: "click-target",
    dwellMs: 5000,
  },
  {
    id: "oa-multi-send",
    route: "interactive",
    target: "oq-submit",
    waitForTarget: "oq-submit",
    title: "Unblock the agent",
    body: "Send answer forwards the full selection set back, the second parked goroutine unblocks, and the queue empties — the modal closes for good. That's the full operator-ask round-trip.",
    placement: "top",
    kind: "action",
    advance: "click-target",
    dwellMs: 4500,
  },

  // ── Wrap-up ────────────────────────────────────────────────────────────────
  {
    id: "oa-done",
    route: "interactive",
    title: "That's operator-ask forwarding",
    body: "You've seen the full round-trip: a dispatched agent forwards a question, kitsoki parks the oracle turn and surfaces a blocking modal, the operator picks (single- or multi-select), and Send answer echoes the choice back to unblock the exact parked goroutine. The agent never silently guesses — a human decides the fork.",
    placement: "center",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },
];
