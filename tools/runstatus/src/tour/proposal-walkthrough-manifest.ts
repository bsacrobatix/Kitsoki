/**
 * Proposal-pipeline walkthrough feature-spotlight tour manifest.
 *
 * A self-contained step array for the proposal-walkthrough video demo
 * (tests/playwright/proposal-walkthrough.spec.ts). Like
 * agent-actions-manifest.ts, it opens on the home story library, explains the
 * dev-story pipeline, drives a fresh run via a route-match action step
 * (home → new session → the interactive /chat view), then walks the proposal
 * pipeline ON the interactive run: idea intake, the scout-search result, the
 * refined brief, the brief judge, the generated draft, and the published
 * proposal — using the realistic "tamagotchi-style virtual pets" idea as
 * content.
 *
 * SINGLE SOURCE OF TRUTH: the same array drives both
 *   1. the tour overlay (started via window.__startTourWithSteps), and
 *   2. the Playwright video spec.
 * The video asserts each step's `title` against the live popover, so the two
 * cannot silently drift.
 *
 * The pipeline-advancing interactions (submit the idea, click confirm / ready /
 * advance_brief / accept) are NOT tour steps — the spec performs them as
 * pre-step hooks (exactly as the agent-actions spec opens drawers in pre-step
 * hooks) so each spotlighted surface exists before the spotlight lands on it.
 *
 * Like manifest.ts this file MUST stay free of Vue / Pinia / DOM-runtime
 * imports — plain types and data only. Every `target` / `waitForTarget` here is
 * a testid the interactive view ships: see src/views/InteractiveView.vue
 * (current-state, chat-section, observe-link, state-badge) and
 * src/components/InputBar.vue (composer-input, composer-send, input-bar,
 * intent-actions, intent-btn-<name>).
 */

import { type TourStep } from "./manifest.js";

// Re-export so the Playwright spec can import the step type alongside the array
// from this one module (mirrors agent-actions-manifest.ts).
export type { TourStep };

export const PROPOSAL_WALKTHROUGH_TOUR_STEPS: readonly TourStep[] = [
  // ── Intro: the story library → a fresh run (tour-driven navigation) ─────────
  {
    id: "pw-intro-home",
    route: "home",
    title: "Start with an idea",
    body: "Every run begins here, in the story library. We'll walk the proposal pipeline on the dev-story: an idea becomes a scoped, scouted, brief-reviewed, and published proposal — all as one deterministic flow.",
    placement: "center",
    kind: "explain",
    advance: "next",
    waitForTarget: "home-view",
    dwellMs: 5000,
  },
  {
    id: "pw-intro-story",
    route: "home",
    target: "story-card",
    waitForTarget: "story-card",
    title: "The dev-story pipeline",
    body: "This story takes a raw idea and runs it through scout search, brief refinement, a brief judge, draft authoring, and publishing. We'll demonstrate it with a realistic idea: tamagotchi-style virtual pets in the session UI.",
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },
  {
    id: "pw-intro-start",
    route: "home",
    target: "new-session-btn",
    waitForTarget: "new-session-btn",
    title: "Spin up a run",
    body: "Click New session to create a fresh, independently-traced run of the proposal pipeline.",
    placement: "right",
    kind: "action",
    advance: "route-match",
    advanceRoute: "interactive",
    dwellMs: 3500,
  },

  // ── Scene 0: Proposal intake — enter the idea ───────────────────────────────
  // Pre-step hook: navigate the run into the proposal intake room.
  {
    id: "pw-intake",
    route: "interactive",
    target: "chat-section",
    waitForTarget: "chat-section",
    title: "Proposal intake",
    body: "Type your idea in the chat. kitsoki will name the proposal, search for conflicts, scaffold a brief, and draft a full proposal document — each stage visible on the live trace to the right.",
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 6000,
  },
  {
    id: "pw-idea-input",
    route: "interactive",
    target: "composer-input",
    waitForTarget: "composer-input",
    title: "Entering the idea",
    body: "We type the idea into the chat composer: a tamagotchi-style virtual pet widget for the session UI. Submitting mints a slug and kicks off the scout search for overlapping work.",
    placement: "top",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },

  // ── Scene 1: Proposal search — scout found no conflicts ─────────────────────
  // Pre-step hook: fill + submit the idea, wait for proposal_search.
  {
    id: "pw-search",
    route: "interactive",
    target: "chat-section",
    waitForTarget: "current-state",
    title: "Step 1 of 4 — scout search",
    body: "kitsoki searched existing proposals for overlap. No conflicts found — the idea is clear to proceed. The current room shows at the top, and every stage lands on the live trace.",
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },
  {
    id: "pw-search-intents",
    route: "interactive",
    target: "intent-actions",
    waitForTarget: "intent-actions",
    title: "Confirm or rethink",
    body: "The room offers exactly the moves it allows as buttons. \"confirm\" advances to the brief editor; \"quit\" drops back to the main menu. Nothing invalid is possible.",
    placement: "top",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },

  // ── Scene 2: Refine brief ───────────────────────────────────────────────────
  // Pre-step hook: click confirm, wait for proposal_refine.
  {
    id: "pw-refine",
    route: "interactive",
    target: "chat-section",
    waitForTarget: "current-state",
    title: "Step 2 of 4 — brief refinement",
    body: "The idea has been distilled into a structured brief, opened in the IDE for editing. A refiner agent read the idea and flagged the key gaps: the pet's lifetime scope, and which UI surface hosts the widget.",
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 6000,
  },
  {
    id: "pw-refine-ready",
    route: "interactive",
    target: "intent-actions",
    waitForTarget: "intent-actions",
    title: "Press ready for the judge",
    body: "When the brief looks right, press \"ready\". A judge reviews it for completeness — clear why, scoped change, named kind. If it passes, the pipeline advances.",
    placement: "top",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },

  // ── Scene 3: Brief judge approved → advance ─────────────────────────────────
  // Pre-step hook: click ready (brief judge fires), then the advance_brief
  // choice becomes available.
  {
    id: "pw-judge",
    route: "interactive",
    target: "intent-actions",
    waitForTarget: "intent-btn-advance_brief",
    title: "Brief approved",
    body: "The brief judge's verdict is \"continue\": clear why, scoped change, kind is story. \"advance to draft\" is now the primary action — proceed to draft authoring.",
    placement: "top",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },

  // ── Scene 4: Proposal draft ─────────────────────────────────────────────────
  // Pre-step hook: click advance_brief, wait for proposal_draft.
  {
    id: "pw-draft",
    route: "interactive",
    target: "chat-section",
    waitForTarget: "current-state",
    title: "Step 3 of 4 — proposal draft",
    body: "The brief was approved, so a draft author wrote the full proposal document: a persistent virtual-pet widget in the session header, its mood tracking the story phase, state persisted in world vars.",
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 6000,
  },
  {
    id: "pw-draft-intents",
    route: "interactive",
    target: "intent-actions",
    waitForTarget: "intent-actions",
    title: "Review, then accept to publish",
    body: "Review the draft, then \"accept\" runs the publish script: it moves the draft from the .workspace staging area to docs/proposals/tamagotchi-pet-ui.md.",
    placement: "top",
    kind: "explain",
    advance: "next",
    dwellMs: 5000,
  },

  // ── Scene 5: Done ───────────────────────────────────────────────────────────
  // Pre-step hook: submit accept, reload, wait for proposal_done.
  {
    id: "pw-done",
    route: "interactive",
    target: "chat-section",
    waitForTarget: "current-state",
    title: "Step 4 of 4 — proposal published",
    body: "docs/proposals/tamagotchi-pet-ui.md is now part of the queue. The full pipeline — idea → scout → brief → judge → draft → published — ran as one deterministic, fully-traced flow.",
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 6000,
  },
  {
    id: "pw-final-state",
    route: "interactive",
    target: "state-badge",
    waitForTarget: "state-badge",
    title: "That's the proposal pipeline",
    body: "You've seen the whole flow on-camera: every input, button, and room view, with the live trace recording each stage. Hit '?' anytime to replay this tour.",
    placement: "bottom",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },
];
