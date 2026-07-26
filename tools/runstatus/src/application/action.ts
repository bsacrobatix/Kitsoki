import type { ApplicationActionEnvelope, ApplicationFrame } from "./types.js";

export interface ApplicationActionClient {
  post(method: string, params: Record<string, unknown>): Promise<ApplicationActionOutcome>;
}

export interface ApplicationActionOutcome {
  readonly frame?: ApplicationFrame;
}

export function dispatchWebApplicationAction(
  client: ApplicationActionClient,
  envelope: ApplicationActionEnvelope,
  page = "",
): Promise<ApplicationActionOutcome> {
  return client.post("runstatus.application.web_action", {
    ...envelope,
    ...(page ? { page } : {}),
  });
}
