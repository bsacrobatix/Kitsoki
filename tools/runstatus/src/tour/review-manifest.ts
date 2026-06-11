/**
 * Mockup Video Studio · /review feedback-mode feature-spotlight tour manifest.
 *
 * The declarative step array for the review-video feature-spotlight demo
 * (tests/playwright/review-video.spec.ts). Like the golden agent-actions /
 * diagram-showcase pairs, this video is TOUR-DRIVEN: the REVIEW_TOUR_STEPS below
 * narrate the whole /review walk via the live TourOverlay
 * (window.__startTourWithSteps). The spec asserts each step's `title` against
 * the live popover, so the manifest and video cannot silently drift.
 *
 * ROUTE NOTE: the /review SPA route is NOT one of the overlay's named routes
 * (home | interactive), so TourOverlay.currentRouteKind maps it to "any". Every
 * step here is therefore route:"any" — the overlay spotlights the on-screen
 * /review testids fine, as long as the target is present when the step runs.
 * TourOverlay is mounted globally in App.vue, so the overlay drives the live
 * /review surface exactly like the other feature tours. (An earlier header here
 * claimed this manifest could not drive the overlay; that was wrong — it does.)
 *
 * The first step is a centered "explain" welcome; the rest spotlight the
 * /review testids the components actually ship (ReviewPage / ChapterTimeline /
 * FlagList / FlagDetail). The spec's pre-step hooks select / expand a flag
 * before the detail-pane steps so their spotlight testids are on screen.
 *
 * Like manifest.ts this file MUST stay free of Vue / Pinia / DOM-runtime
 * imports — plain types and data only.
 */

import { type TourStep } from "./manifest.js";

// Re-export so the Playwright spec can import the step type alongside the array
// from this one module (mirrors agent-actions-manifest.ts / diagram-showcase).
export type { TourStep };

export const REVIEW_TOUR_STEPS: readonly TourStep[] = [
  // ── Intro: centered welcome ─────────────────────────────────────────────────
  {
    id: "review-welcome",
    route: "any",
    title: "Video review — feedback on a rendered walkthrough",
    body: "After the story renders a real walkthrough MP4, the /review surface is where an operator flags moments and dispatches structured refine notes. Every flag carries its still, its source_ref, and its instruction — captured in the browser, refined deterministically in the story.",
    placement: "center",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },
  {
    id: "review-open",
    route: "any",
    target: "review-page",
    waitForTarget: "review-page",
    title: "The /review feedback surface",
    body: "The /review surface opens on a session that rendered a real walkthrough video. Two columns: the player, chapter timeline and flags on the left; the selected flag's detail on the right.",
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 6000,
  },
  {
    id: "review-player",
    route: "any",
    target: "rp-player",
    waitForTarget: "rp-player",
    title: "The rendered walkthrough plays inline",
    body: "This is the actual MP4 the deterministic slidey producer rendered for this run, served through the run's ArtifactResolver by its journalled handle — no upload, no copy.",
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 6000,
  },
  {
    id: "review-timeline",
    route: "any",
    target: "chapter-timeline",
    waitForTarget: "chapter-timeline",
    title: "A chapter timeline from the sidecar",
    body: "Each marker is one scene from the chapter sidecar the render emitted (source_ref kind=slidey). Click or drag the track to pick a moment; the selection is what you flag.",
    placement: "top",
    kind: "explain",
    advance: "next",
    dwellMs: 6500,
  },
  {
    id: "review-flag",
    route: "any",
    target: "ct-flag-btn",
    waitForTarget: "ct-flag-btn",
    title: "Flag a moment",
    body: "Flagging the selected moment grabs a still at that timestamp — a REAL single-frame ffmpeg extraction (internal/video.Frame), recorded back through the run's substrate as its own artifact handle.",
    placement: "top",
    kind: "explain",
    advance: "next",
    dwellMs: 5500,
  },
  {
    id: "review-still",
    route: "any",
    target: "fd-still",
    waitForTarget: "fd-still",
    title: "The captured still",
    body: "The flag's detail pane shows the grabbed frame — proof the frame seam ran end-to-end: video handle → ffmpeg grab at t → recorded PNG handle → served back into the panel.",
    placement: "left",
    kind: "explain",
    advance: "next",
    dwellMs: 6000,
  },
  {
    id: "review-source",
    route: "any",
    target: "fd-source",
    waitForTarget: "fd-source",
    title: "Resolved source_ref + IDE deep-link",
    body: "The dominant chapter resolves the flag to the exact slidey scene that produced it, with a vscode:// deep-link straight to the spec. Feedback lands on the source, not a screenshot.",
    placement: "left",
    kind: "explain",
    advance: "next",
    dwellMs: 6500,
  },
  {
    id: "review-instruction",
    route: "any",
    target: "fd-instruction",
    waitForTarget: "fd-instruction",
    title: "Write the refine instruction",
    body: "Type what should change at this moment. This becomes the structured feedback note's instruction — a binding directive the slice-3 refine arc will honour.",
    placement: "left",
    kind: "explain",
    advance: "next",
    dwellMs: 6000,
  },
  {
    id: "review-send",
    route: "any",
    target: "fd-send-refine",
    waitForTarget: "fd-send-refine",
    title: "Send to refine",
    body: "Dispatch the note via runstatus.feedback.add: it carries the source_ref, the time range, the captured frame handle, and the instruction. The panel captures and dispatches — it never edits a spec itself.",
    placement: "left",
    kind: "explain",
    advance: "next",
    dwellMs: 6500,
  },
  {
    id: "review-flaglist",
    route: "any",
    target: "flag-list",
    waitForTarget: "flag-list",
    title: "Every flag, dispatched together",
    body: "Flags accumulate in the left column; a filled dot marks a dispatched note. 'Send all' fires every open note in one batch for the next refine cycle.",
    placement: "right",
    kind: "explain",
    advance: "next",
    dwellMs: 6000,
  },
];
