import type { ApplicationFrame } from "./types.js";

export type ApplicationHistoryMode = "push" | "replace" | "none";

const applicationRouteStateKey = "kitsokiApplicationRoute";

export function readApplicationRoutePath(
  location: Pick<Location, "pathname">,
  applicationId = "",
): string {
  const path = location.pathname || "/";
  const prefix = applicationId ? `/application/${encodeURIComponent(applicationId)}` : "";
  if (!prefix || (path !== prefix && !path.startsWith(`${prefix}/`))) return path;
  const relative = path.slice(prefix.length);
  return relative && relative !== "/" ? relative : "/";
}

export function writeApplicationRoute(
  frame: ApplicationFrame,
  mode: ApplicationHistoryMode,
  history: Pick<History, "state" | "pushState" | "replaceState"> = window.history,
  location: Pick<Location, "href" | "pathname"> = window.location,
  applicationId = "",
): void {
  if (mode === "none" || !frame.route_path) return;
  const currentRoute = readApplicationRoutePath(location, applicationId);
  if (currentRoute === frame.route_path) return;
  const next = new URL(location.href);
  const candidatePrefix = applicationId ? `/application/${encodeURIComponent(applicationId)}` : "";
  const prefix = candidatePrefix &&
    (location.pathname === candidatePrefix || location.pathname.startsWith(`${candidatePrefix}/`))
    ? candidatePrefix
    : "";
  next.pathname = `${prefix}${frame.route_path}`;
  const state = {
    ...(typeof history.state === "object" && history.state !== null ? history.state : {}),
    [applicationRouteStateKey]: frame.route_path,
  };
  if (mode === "push") history.pushState(state, "", next);
  else history.replaceState(state, "", next);
}

export function observeApplicationRoutes(
  callback: (path: string) => void,
  applicationId = "",
  target: Pick<Window, "addEventListener" | "removeEventListener" | "location"> = window,
): () => void {
  const onPopState = () => callback(readApplicationRoutePath(target.location, applicationId));
  target.addEventListener("popstate", onPopState);
  return () => target.removeEventListener("popstate", onPopState);
}
