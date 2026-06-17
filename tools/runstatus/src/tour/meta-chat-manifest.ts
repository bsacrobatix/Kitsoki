/**
 * Meta-chat persistence + launcher-status feature-spotlight tour manifest.
 *
 * HAND-AUTHORED (not code-generated): this manifest narrates the meta-mode chat
 * persistence feature — an in-flight meta conversation now survives close/reopen,
 * and the ✦ Meta launcher shows status badges (a working ⟳ while a turn streams,
 * a ready ● when a reply is waiting). The close → badge → reopen choreography is
 * not a linear "spotlight a control" sequence, so the matching video spec
 * (meta-chat-video.spec.ts) walks ONLY the intro steps here through the tour
 * overlay, then drives the persistence/badge moments directly with captions —
 * the tour overlay (z-index 1500) sits above the meta overlay (z-index 1000) and
 * cannot cleanly narrate an overlay that is being opened and closed.
 *
 * The four-step home → story → new session → observer intro mirrors the golden
 * agent-actions tour so the whole opening stays narrated rather than a silent
 * page.goto. After the intro the spec takes over.
 */
import { type TourStep } from "./types.js";

// Re-export so the Playwright spec imports the step type alongside the array.
export type { TourStep };

export const META_CHAT_TOUR_STEPS: readonly TourStep[] = [
  {
    id: "mc-intro-home",
    route: "home",
    title: "Meta chat that doesn't lose your place",
    body: "kitsoki's meta chat lets you ask about (or edit) a story without leaving the run. This tour shows two new things: an in-flight meta conversation now PERSISTS across close and reopen, and the ✦ Meta launcher shows status badges so you know when a reply is working or waiting — even with the overlay closed. We'll use the bug-fix pipeline.",
    placement: "center",
    kind: "explain",
    advance: "next",
    waitForTarget: "home-view",
    dwellMs: 5500,
  },
  {
    id: "mc-intro-story",
    route: "home",
    target: "story-card",
    title: "The bug-fix pipeline",
    body: "We'll open a run of this story, then talk to the meta agent ABOUT it — read-only Story Q&A. The meta turn streams the agent's thinking and a tool call, paced so you can watch it work.",
    placement: "right",
    kind: "explain",
    advance: "next",
    waitForTarget: "story-card",
    dwellMs: 5000,
  },
  {
    id: "mc-new-session",
    route: "home",
    target: "new-session-btn",
    title: "Spin up a run",
    body: "Click New session to create a fresh run. It opens directly in the chat, where the ✦ Meta launcher lives in the bottom-right corner.",
    placement: "right",
    kind: "action",
    advance: "route-match",
    advanceRoute: "interactive",
    waitForTarget: "new-session-btn",
    dwellMs: 4000,
  },
  {
    id: "mc-launcher",
    route: "interactive",
    target: "meta-button",
    title: "The ✦ Meta launcher",
    body: "Here it is — bottom-right, always present. In a moment we'll open a Story Q&A chat, start a turn streaming, and close the overlay while it's still working. Watch THIS button: it will grow a spinning ⟳ badge to tell you a meta chat is busy, then a green ● when a reply is waiting.",
    placement: "left",
    kind: "explain",
    advance: "next",
    waitForTarget: "meta-button",
    dwellMs: 6000,
  },
];
