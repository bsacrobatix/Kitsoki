/**
 * dev-story "bug triage → autonomous bugfix pipeline" feature-tour manifest.
 *
 * A self-contained TourStep array for the dev-story demo video. Mirrors the
 * golden agent-actions-manifest.ts shape: it opens on the home story library,
 * explains the demo, drives a fresh run via route-match action steps (home →
 * new session → the drive view), points out the read-only observer, then walks
 * the full triage → autonomous-bugfix path one room per beat:
 *
 *   triage:  go_ticket_search → (survey the queue) → pick_ticket (AA-13268)
 *            → (review the triage report, still in ticket_search)
 *   handoff: go_bugfix → bf.reproducing
 *   pipeline: bf__accept ×7  → proposing → implementing → testing →
 *             reviewing → validating → done → pr.open_pr
 *
 * The triage report review happens IN the triage room (ticket_search) BEFORE
 * the handoff — not inside the pipeline. Three beats dwell in ticket_search
 * (survey → pick → review) so it reads as a real, visited destination.
 *
 * SINGLE SOURCE OF TRUTH: the same array drives both
 *   1. the in-app tour overlay (window.__startTourWithSteps), and
 *   2. the Playwright video spec (tests/playwright/dev-story-bugfix-video.spec.ts).
 * The video asserts each step's `title` against the live popover so the two
 * cannot silently drift.
 *
 * Like manifest.ts this file MUST stay free of Vue / Pinia / DOM-runtime
 * imports — plain types and data only. Every `target` / `waitForTarget` is a
 * UNIVERSAL testid the drive view (InteractiveView + InputBar) actually ships:
 * home-view, story-card, new-session-btn, observe-link, current-state,
 * state-badge, chat-transcript, chat-row-agent, composer-*, intent-btn-<name>.
 *
 * The driving mechanics (which intent is slotless vs slotted, and the exact
 * resulting state for each) live in the spec; this manifest is the narration
 * track. The spec drives the turns between the explain beats below.
 */

import { type TourStep } from "./manifest.js";

// Re-export so the Playwright spec can import the step type alongside the array
// from this one module (mirrors the golden agent-actions manifest).
export type { TourStep };

export const DEV_STORY_BUGFIX_TOUR_STEPS: readonly TourStep[] = [
  // ── Intro: the story library → a fresh run (tour-driven navigation) ─────────
  {
    id: "ds-intro-home",
    route: "home",
    title: "An engineer's day, end to end",
    body: "dev-story: triage a bug, then watch the autonomous fix pipeline reproduce → propose → implement → test → review → validate → open a PR. Every step is a deterministic state-machine room kitsoki runs the same way every time — no LLM in the loop for this demo.",
    placement: "center",
    kind: "explain",
    advance: "next",
    waitForTarget: "home-view",
    dwellMs: 5500,
  },
  {
    id: "ds-intro-story",
    route: "home",
    target: "story-card",
    waitForTarget: "story-card",
    title: "The dev-story",
    body: "This card is the dev-story — an engineer's day modelled as one graph. It opens in a triage queue, picks a fixable ticket, then hands that ticket off into the autonomous bugfix pipeline. Let's run it.",
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },
  {
    id: "ds-intro-start",
    route: "home",
    target: "new-session-btn",
    waitForTarget: "new-session-btn",
    title: "Spin up a run",
    body: "Click New session to create a fresh, independently-traced run of the dev-story.",
    placement: "right",
    kind: "action",
    advance: "route-match",
    advanceRoute: "interactive",
    dwellMs: 3500,
  },
  {
    id: "ds-intro-observe",
    route: "interactive",
    target: "observe-link",
    waitForTarget: "observe-link",
    title: "Drive here, observe there",
    body: "We're on the drive view: pick intents, submit turns, and watch the state badge advance one room at a time. The Observe link opens the read-only observer for this same run — the trace tree, timeline, and graph. We'll drive the whole pipeline from here.",
    placement: "bottom",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },

  // ── Triage ──────────────────────────────────────────────────────────────────
  {
    id: "ds-triage-open",
    route: "any",
    target: "composer-input",
    waitForTarget: "composer-input",
    title: "Into bug triage",
    body: "The engineer's day starts in triage. The slotless go_ticket_search intent steps from the main room into ticket_search — the bug triage queue. Let's go there now.",
    placement: "top",
    kind: "explain",
    advance: "next",
    dwellMs: 4500,
  },
  {
    id: "ds-triage-survey",
    route: "any",
    target: "chat-transcript",
    waitForTarget: "chat-transcript",
    title: "The bug triage queue",
    body: "This is bug triage: the ticket_search room, loaded with the open bugs the moment we arrive — no search needed. Three are waiting: AA-13268 (P1, the note-list bug), AA-13204 (P2), AA-13190 (P3). Severity-ordered, the worst one first.",
    placement: "top",
    kind: "explain",
    advance: "next",
    dwellMs: 6000,
  },
  {
    id: "ds-triage-pick",
    route: "any",
    target: "chat-transcript",
    waitForTarget: "chat-transcript",
    title: "Pick the fixable ticket",
    body: "Straight from the queue, pick_ticket selects AA-13268 — 'Note list disappears after creating a second note' — by id. No leaving the triage room to do it.",
    placement: "top",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },
  {
    id: "ds-triage-review",
    route: "any",
    target: "chat-transcript",
    waitForTarget: "chat-transcript",
    title: "Review the triage report",
    body: "Triage's verdict, still in the ticket_search room: AA-13268 is picked (★), assessed P1 / open, and the room marks it ✓ Ready to hand off. We've reviewed the triage report and chosen the bug worth fixing — only now do we leave triage for the pipeline.",
    placement: "top",
    kind: "explain",
    advance: "next",
    dwellMs: 6000,
  },

  // ── Handoff into the autonomous bugfix pipeline ──────────────────────────────
  {
    id: "ds-handoff",
    route: "any",
    target: "intent-btn-go_bugfix",
    waitForTarget: "intent-btn-go_bugfix",
    title: "Hand off to the pipeline",
    body: "go_bugfix hands AA-13268 into the imported autonomous bugfix pipeline. The run enters bf.reproducing — the agent now owns the fix end to end, and each operator accept advances it exactly one room.",
    placement: "top",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },

  // ── The pipeline walk: one bf__accept per room ───────────────────────────────
  {
    id: "ds-bf-reproducing",
    route: "any",
    target: "intent-btn-bf__accept",
    waitForTarget: "intent-btn-bf__accept",
    title: "Reproduce the bug",
    body: "Now we're in the pipeline. bf.reproducing confirms the bug is real before touching code: it stands up the environment, follows the steps (create alpha, create beta, list re-renders empty), and verifies the note list really does vanish. Accept to move on.",
    placement: "top",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },
  {
    id: "ds-bf-proposing",
    route: "any",
    target: "intent-btn-bf__accept",
    waitForTarget: "intent-btn-bf__accept",
    title: "Propose a root-cause fix",
    body: "bf.proposing — root cause: the list re-render reuses the same array reference, so React's diff bails out. The proposal: replace the in-place splice with a new array on add. Accept the proposal.",
    placement: "top",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },
  {
    id: "ds-bf-implementing",
    route: "any",
    target: "intent-btn-bf__accept",
    waitForTarget: "intent-btn-bf__accept",
    title: "Implement the fix",
    body: "bf.implementing — the agent edits NotesList.tsx in an isolated worktree, applying exactly the proposed change. Accept to run the tests.",
    placement: "top",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },
  {
    id: "ds-bf-testing",
    route: "any",
    target: "intent-btn-bf__accept",
    waitForTarget: "intent-btn-bf__accept",
    title: "Add and run tests",
    body: "bf.testing — a regression test is added and the suite runs green: 4 passed, 0 failed. The fix is proven, not just plausible. Accept to self-review.",
    placement: "top",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },
  {
    id: "ds-bf-reviewing",
    route: "any",
    target: "intent-btn-bf__accept",
    waitForTarget: "intent-btn-bf__accept",
    title: "Self-review the diff",
    body: "bf.reviewing — the agent reviews its own diff for scope creep and unintended changes before anyone else sees it. Accept to validate end to end.",
    placement: "top",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },
  {
    id: "ds-bf-validating",
    route: "any",
    target: "intent-btn-bf__accept",
    waitForTarget: "intent-btn-bf__accept",
    title: "Validate end to end",
    body: "bf.validating — a full end-to-end check: build ok, UI shows both notes, the originally-reported symptom is gone. Accept to close out.",
    placement: "top",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },
  {
    id: "ds-bf-done",
    route: "any",
    target: "intent-btn-bf__accept",
    waitForTarget: "intent-btn-bf__accept",
    title: "Close out the fix",
    body: "bf.done — the pipeline has reproduced, fixed, tested, reviewed and validated the bug. One last accept opens the pull request and transitions the ticket to resolved.",
    placement: "top",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },

  // ── Terminal: PR opened ──────────────────────────────────────────────────────
  {
    id: "ds-pr-open",
    route: "any",
    target: "state-badge",
    waitForTarget: "state-badge",
    title: "Pull request opened",
    body: "pr.open_pr — the validated fix is pushed to fix/AA-13268, the PR is opened, and AA-13268 transitions to resolved. The whole arc — triage to merged-ready PR — ran as one deterministic graph, no LLM in the loop.",
    placement: "left",
    kind: "explain",
    advance: "next",
    dwellMs: 6000,
  },
  {
    id: "ds-done",
    route: "any",
    title: "That's the engineer's day",
    body: "You watched triage pick a fixable bug and hand it to an autonomous pipeline that reproduced, proposed, implemented, tested, reviewed, validated, and shipped the fix as a PR — every room a deterministic, replayable state-machine step. Hit '?' anytime to replay this tour.",
    placement: "center",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },
];
