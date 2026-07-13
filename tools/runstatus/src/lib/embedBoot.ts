/**
 * Embed boot params (portal-kitsoki-chat-embed-plan.md §3.1) — the
 * presentation-free URL contract a host page uses to pin an embedded
 * ChatSurface to a story/scope/catalog and skip the picker:
 *
 *     <kitsoki-origin>/?surface=chat&embed=1
 *       &story=<path-or-app-id>&autostart=1&catalog=<alias>&scope=<key>
 *
 * `story`/`autostart` seed ChatSurface's existing selectedStoryPath/onStart.
 * `catalog`/`scope` ride session.new's initial_world (world.catalog,
 * world.scope_key) — never the URL beyond this one query read.
 */

export interface EmbedBoot {
  story: string | null;
  autostart: boolean;
  catalog: string | null;
  scope: string | null;
  /** Explicit postMessage target origin override (embedHost.ts). */
  origin: string | null;
}

export function resolveEmbedBoot(): EmbedBoot {
  const params = new URLSearchParams(location.search);
  return {
    story: params.get("story"),
    autostart: params.get("autostart") === "1",
    catalog: params.get("catalog"),
    scope: params.get("scope"),
    origin: params.get("origin"),
  };
}
