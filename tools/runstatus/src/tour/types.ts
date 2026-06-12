/**
 * The tour-step vocabulary shared by every tour manifest.
 *
 * Manifests under src/tour/generated/ are CODE-GENERATED from the feature
 * catalog (features/*.yaml at the repo root) — edit the YAML and run
 * `make features`, never the generated files. This module is the one
 * hand-written home of the step types; it MUST stay free of any Vue / Pinia /
 * DOM-runtime import because the Node-based Playwright specs import the
 * generated manifests (and through them, this module) directly.
 *
 * Robustness rules for new steps (enforced by the feature-catalog schema):
 *   - Anchor by a `data-testid` that is present on every story (top bar, chat,
 *     trace panels, global buttons) — NOT a story-specific intent/state.
 *   - Prefer `kind: "explain"` (advance on Next). Reserve `action` for cheap,
 *     universal gestures: navigation (`route-match`) or an immediate click
 *     (`click-target`). NEVER gate on `state-match` / `waitForState` — that
 *     couples the tour to LLM-turn latency and can strand it.
 */

/** Which route a step belongs to. Matched against the hash path by the overlay. */
export type TourRoute = "home" | "interactive" | "any";

/** How the current step is dismissed / advanced. */
export type AdvanceTrigger =
  | "next" // explain step: the user clicks the popover's Next
  | "click-target" // action step: the click on the real element is the signal
  | "route-match"; // action step: advance when the route becomes `advanceRoute`

export type Placement = "top" | "bottom" | "left" | "right" | "center";

export interface TourStep {
  /** Stable id; also the Playwright screenshot label. */
  id: string;

  /** Route this step lives on; the overlay holds until we're there. */
  route: TourRoute;

  /**
   * `data-testid` of the element to spotlight, resolved as
   * `[data-testid="<target>"]`. Omit for a centered, anchorless step. Must be a
   * UNIVERSAL testid (present on every story), not a story-specific one.
   */
  target?: string;

  /** Narrows an ambiguous target by visible text (rarely needed; keep generic). */
  targetText?: string;

  title: string;
  body: string;
  placement: Placement;

  /**
   * 'explain' — highlight + popover with Back / Skip / Next (read-only).
   * 'action' — the highlighted element is a REAL control; clicking it advances
   *            both the app and the tour. Keep these cheap & universal.
   */
  kind: "explain" | "action";

  /** When this step is considered done. 'explain' steps always use 'next'. */
  advance: AdvanceTrigger;

  /** Required when advance === 'route-match'. */
  advanceRoute?: TourRoute;

  /**
   * Gate BEFORE showing: wait until this testid exists in the DOM (it appears
   * after a route transition or hydration — both fast, NOT turn-dependent).
   */
  waitForTarget?: string;

  /** ms the video spec dwells on this step. The live UI ignores it. */
  dwellMs?: number;
}
