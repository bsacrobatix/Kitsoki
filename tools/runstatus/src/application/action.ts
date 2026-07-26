import type {
  ApplicationActionEnvelope,
  ApplicationActionResult,
} from "./types.js";

export interface ApplicationActionClient {
  post(method: string, params: Record<string, unknown>): Promise<ApplicationActionOutcome>;
}

export type ApplicationActionOutcome = Omit<ApplicationActionResult, "ok">;

export function dispatchWebApplicationAction(
  client: ApplicationActionClient,
  envelope: ApplicationActionEnvelope,
  page = "",
  routeParams: Readonly<Record<string, unknown>> = {},
): Promise<ApplicationActionOutcome> {
  return client.post("runstatus.application.web_action", {
    ...envelope,
    ...(page ? { page } : {}),
    ...(Object.keys(routeParams).length ? { route_params: routeParams } : {}),
  });
}
