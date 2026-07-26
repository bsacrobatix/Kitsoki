import type { ApplicationFrame } from "./types.js";
import {
  inspectApplicationSemanticElement,
  type ApplicationSemanticInspection,
} from "./semantic.js";

export interface ApplicationFeedbackClient {
  post(method: string, params: Record<string, unknown>): Promise<unknown>;
}

export interface ApplicationSemanticAnchor {
  readonly kind: "semantic_element";
  readonly semantic_element: {
    readonly plugin: "kitsoki.application";
    readonly ref: string;
    readonly semantic_kind: string;
    readonly label: string;
    readonly description: string;
    readonly data: Readonly<Record<string, unknown>>;
  };
}

export interface ApplicationFeedbackSubmission {
  readonly report: {
    readonly schema: "kitsoki.feedback.report.v1";
    readonly idempotencyKey: string;
    readonly app: string;
    readonly producer: "kitsoki.application";
    readonly kind: string;
    readonly userText: string;
    readonly reviewed: true;
    readonly anchor: ApplicationSemanticAnchor;
  };
  readonly receipt: {
    readonly ref: string;
    readonly deduped: boolean;
    readonly routed: readonly unknown[];
  };
}

interface CanonicalSemanticInspection {
  readonly node: { readonly ref: string };
}

export async function inspectApplicationFeedbackTarget(
  client: ApplicationFeedbackClient,
  frame: ApplicationFrame,
  target: EventTarget | null,
): Promise<ApplicationSemanticInspection | null> {
  const local = inspectApplicationSemanticElement(target, frame);
  if (!local) return null;
  const canonical = await client.post(
    "runstatus.application.inspect",
    {
      session_id: frame.session_id,
      page: frame.page,
      ref: local.semantic_element.ref,
    },
  ) as CanonicalSemanticInspection;
  return canonical.node.ref === local.semantic_element.ref ? local : null;
}

// submitApplicationFeedback uses the backend's canonical frame resolver and
// existing reviewed local sink. The browser never copies props or world values
// into the report.
export function submitApplicationFeedback(
  client: ApplicationFeedbackClient,
  frame: Pick<ApplicationFrame, "session_id" | "page">,
  ref: string,
  instruction: string,
  options: { kind?: string; idempotencyKey?: string } = {},
): Promise<ApplicationFeedbackSubmission> {
  return client.post("runstatus.application.feedback", {
    session_id: frame.session_id,
    page: frame.page,
    ref,
    instruction,
    ...(options.kind ? { kind: options.kind } : {}),
    ...(options.idempotencyKey ? { idempotency_key: options.idempotencyKey } : {}),
  }) as Promise<ApplicationFeedbackSubmission>;
}
