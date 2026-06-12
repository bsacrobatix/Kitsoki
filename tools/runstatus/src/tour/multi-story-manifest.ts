/**
 * Multi-story full-product walkthrough tour manifest.
 *
 * A self-contained step array for the end-to-end product demo
 * (tests/playwright/multi-story.spec.ts). Like agent-actions-manifest.ts, the
 * WHOLE video is tour-driven: it opens on the home story library, frames the
 * catalogue and one story card, drives a fresh run via a route-match action
 * step (home → new session → the interactive /chat view), narrates the PRD
 * happy path turn-by-turn on the chat surface (composer + intent buttons +
 * state badge), then crosses the product's defining seam — a full page reload —
 * to show that active sessions survive it, and lands back on the home
 * active-sessions table.
 *
 * SINGLE SOURCE OF TRUTH: the same array drives both
 *   1. the tour overlay (started via window.__startTourWithSteps), and
 *   2. the Playwright video spec.
 * The video asserts each step's `title` against the live popover, so the two
 * cannot silently drift.
 *
 * THE RELOAD SEAM. A real `page.goto` reload tears down the in-memory Pinia
 * tour overlay. The spec therefore walks the steps up to and including the
 * `ms-reload` step, performs the reload in that step's pre-step hook, and
 * RE-INJECTS the remaining steps (the home active-sessions slice) via
 * window.__startTourWithSteps after the reload. The reload is itself a narrated
 * step — "active sessions survive reload" is part of THIS demo's story — so the
 * tour stays coherent across it.
 *
 * The pipeline-advancing interactions (submit each PRD intent) are NOT tour
 * steps — the spec performs them as pre-step hooks (exactly as the
 * design-walkthrough spec advances the pipeline before each spotlight) so
 * each spotlighted surface and state exists before the spotlight lands.
 *
 * Like manifest.ts this file MUST stay free of Vue / Pinia / DOM-runtime
 * imports — plain types and data only. Every `target` / `waitForTarget` here is
 * a testid the product ships: home (src/views/HomeView.vue: home-view,
 * story-card, story-title, new-session-btn, rescan-btn, session-row,
 * session-state, session-open, story-active-count), drive
 * (src/views/InteractiveView.vue: chat-section, current-state, state-badge,
 * observe-link) and the composer (src/components/InputBar.vue: composer-input,
 * composer-send, intent-actions, intent-btn-<name>).
 */

import { type TourStep } from "./manifest.js";

// Re-export so the Playwright spec can import the step type alongside the array
// from this one module (mirrors agent-actions-manifest.ts).
export type { TourStep };

export const MULTI_STORY_TOUR_STEPS: readonly TourStep[] = [
  // ── Intro: the story library → a fresh run (tour-driven navigation) ─────────
  {
    id: "ms-intro-home",
    route: "home",
    title: "One home for every story",
    body: "kitsoki discovers every story under your stories tree and lists them here as a live catalogue — each a deterministic state machine you can run, observe, and reload. We'll take the whole product for a spin on the PRD authoring story.",
    placement: "center",
    kind: "explain",
    advance: "next",
    waitForTarget: "home-view",
    dwellMs: 5500,
  },
  {
    id: "ms-intro-story",
    route: "home",
    target: "story-card",
    waitForTarget: "story-card",
    title: "A story card per pipeline",
    body: "Each card names a discovered story, its on-disk path, and a live-session badge. Rescan re-walks the tree on demand — no fsnotify magic, just an explicit, auditable refresh. We'll drive the PRD card: idea → clarify → brief → references → draft → done.",
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },
  {
    id: "ms-intro-start",
    route: "home",
    target: "new-session-btn",
    waitForTarget: "new-session-btn",
    title: "Start a fresh, traced run",
    body: "New session mints an independently-traced run from the story and drops you straight onto its chat surface — no dead-end on a read-only observer. Click it to begin the PRD walkthrough.",
    placement: "right",
    kind: "action",
    advance: "route-match",
    advanceRoute: "interactive",
    dwellMs: 4000,
  },

  // ── Drive the PRD happy path on the chat surface ────────────────────────────
  {
    id: "ms-chat-idle",
    route: "interactive",
    target: "chat-section",
    waitForTarget: "current-state",
    title: "Act immediately — the chat surface",
    body: "A new session opens live in idle, with the room's opening prompt already on screen and the composer ready. The state badge in the header is the hard signal of where the run is; it updates the instant a turn lands.",
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },
  {
    id: "ms-chat-composer",
    route: "interactive",
    target: "composer-input",
    waitForTarget: "composer-input",
    title: "Type an idea, send a turn",
    body: "Text-slot intents are typed right into the composer. We send the idea \"a CLI for X\" via the discuss intent — idle absorbs it as a self-transition, distilling the conversation before the pipeline advances.",
    placement: "top",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },
  {
    id: "ms-chat-intents",
    route: "interactive",
    target: "intent-actions",
    waitForTarget: "intent-actions",
    title: "Only the moves the room allows",
    body: "Slot-less intents render as buttons — exactly the transitions the current room offers, nothing invalid. We press start, clear the prior-art search gate, and land in clarifying, where the interviewer asks scoping questions.",
    placement: "top",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },
  {
    id: "ms-chat-clarifying",
    route: "interactive",
    target: "current-state",
    waitForTarget: "current-state",
    title: "Clarifying → brief",
    body: "We answer the clarifying questions in the composer; submit_answers appends the round to the transcript and advances to brief, where the editable brief artifact is written and opened in the IDE.",
    placement: "bottom",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },
  {
    id: "ms-chat-references",
    route: "interactive",
    target: "current-state",
    waitForTarget: "current-state",
    title: "Brief → references → drafting",
    body: "Confirming the brief runs the researcher and lands on references; confirming again advances to drafting, where the full PRD is authored. Each confirm is a single, unambiguous button — the pipeline never asks for invalid input.",
    placement: "bottom",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },
  {
    id: "ms-chat-done",
    route: "interactive",
    target: "state-badge",
    waitForTarget: "state-badge",
    title: "Accept → done, fully traced",
    body: "Accept publishes the draft and the run reaches its terminal exit. The badge flips to a done state, the composer is replaced by the done note, and the trace timeline on the right holds every event the run produced — the whole flow, no LLM, reproducible byte-for-byte.",
    placement: "bottom",
    kind: "explain",
    advance: "next",
    dwellMs: 6000,
  },

  // ── The reload seam — active sessions survive a full page reload ────────────
  // Pre-step hook: page.goto reload back to home, then re-inject the remaining
  // steps (this one onward) — the reload tears down the Pinia overlay.
  {
    id: "ms-reload",
    route: "home",
    target: "home-view",
    waitForTarget: "home-view",
    title: "Reload — your runs persist",
    body: "Sessions live in the server's store, not the browser tab. We just hard-reloaded the whole page and navigated home: every run is still here, its current state recovered straight from the trace. Close the tab, reopen tomorrow — the runs are exactly where you left them.",
    placement: "center",
    kind: "explain",
    advance: "next",
    dwellMs: 6000,
  },
  {
    id: "ms-active-sessions",
    route: "home",
    target: "session-row",
    waitForTarget: "session-row",
    title: "The active-sessions table",
    body: "Home rolls every live run up into one table — each row its session, current state, and an Open link straight back into the run view. The completed PRD run shows its terminal exit state; a fresh one sits at idle. The state is reported raw from the trace, never rewritten.",
    placement: "top",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },
  {
    id: "ms-story-badge",
    route: "home",
    target: "story-active-count",
    waitForTarget: "story-active-count",
    title: "Live-session count, per story",
    body: "Back on the PRD card, the live-session badge now reflects its running sessions. One catalogue, every run accounted for — discovery, driving, observing, reload-survival, and roll-up, all over one deterministic, fully-traced product. Hit '?' anytime to replay this tour.",
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 6000,
  },
];
