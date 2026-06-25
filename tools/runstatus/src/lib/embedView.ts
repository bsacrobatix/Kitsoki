/**
 * embedView — the host side of the generic, producer-neutral embed protocol.
 *
 * An embedded artifact (a deck, a notebook, any multi-view producer) posts which
 * place it is currently showing to its parent window:
 *
 *   window.parent.postMessage(
 *     { type: 'embed:view', producer, scope, label, count }, '*')
 *
 * `scope` is an OPAQUE token the host round-trips back to target feedback at the
 * thing on screen (e.g. a slide index). The host interprets nothing producer-
 * specific — it just remembers the latest scope so a refine/annotation dispatch
 * can carry it. This is how kitsoki targets "the slide you're looking at" without
 * knowing anything about slidey: slidey (the producer) speaks this protocol.
 *
 * Frame-free + DI-friendly: parseEmbedView is a pure function (unit-tested), and
 * installEmbedViewListener wires it to window with a teardown handle.
 */

export interface EmbedViewMessage {
  /** The producer that owns the embed (e.g. "slidey"). Informational. */
  producer?: string;
  /** The opaque scope token the host round-trips back (e.g. a scene index). */
  scope: string;
  /** Human label for the current view ("Scene 9 · Cat Wrangling"). */
  label?: string;
  /** Total number of views, when the producer knows it. */
  count?: number;
}

/**
 * parseEmbedView returns the EmbedViewMessage carried by a postMessage event, or
 * null when the event is not a well-formed `embed:view` message. Defensive: a
 * page receives messages from many sources, so anything off-shape is ignored.
 */
export function parseEmbedView(data: unknown): EmbedViewMessage | null {
  if (!data || typeof data !== "object") return null;
  const m = data as Record<string, unknown>;
  if (m.type !== "embed:view") return null;
  // scope is the one required field; coerce a number scope to string so an
  // opaque index round-trips cleanly.
  if (m.scope === undefined || m.scope === null) return null;
  const scope = typeof m.scope === "number" ? String(m.scope) : m.scope;
  if (typeof scope !== "string" || scope === "") return null;
  return {
    producer: typeof m.producer === "string" ? m.producer : undefined,
    scope,
    label: typeof m.label === "string" ? m.label : undefined,
    count: typeof m.count === "number" ? m.count : undefined,
  };
}

/**
 * installEmbedViewListener subscribes to window 'message' events, parses
 * `embed:view` messages, and calls onView with each. Returns a teardown function
 * that removes the listener. A no-op (returns a no-op teardown) when there is no
 * window (SSR / tests without a DOM).
 */
export function installEmbedViewListener(
  onView: (view: EmbedViewMessage) => void,
  target: Pick<Window, "addEventListener" | "removeEventListener"> | undefined =
    typeof window !== "undefined" ? window : undefined,
): () => void {
  if (!target) return () => {};
  const handler = (ev: Event) => {
    const view = parseEmbedView((ev as MessageEvent).data);
    if (view) onView(view);
  };
  target.addEventListener("message", handler);
  return () => target.removeEventListener("message", handler);
}
