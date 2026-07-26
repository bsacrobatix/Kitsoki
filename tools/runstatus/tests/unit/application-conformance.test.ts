import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it, vi } from "vitest";
import {
  dispatchWebApplicationAction,
  submitApplicationFeedback,
  type ApplicationFrame,
} from "../../src/application/index.js";

// conformance-consumer:web-ts
const fixturePath = resolve(
  process.cwd(),
  "../../internal/applicationconformance/application-conformance-v1.json",
);
const fixture = JSON.parse(readFileSync(fixturePath, "utf8")) as {
  schema: "application-conformance/v1";
  session_id: string;
  page: string;
  frame_revision: number;
  action: { id: string; semantic_ref: string; input: Record<string, unknown> };
  feedback: {
    ref: string; instruction: string; kind: string; idempotency_key: string; excluded: string[];
  };
  expected: { outcome_schema: string; receipt_schema: string; outcome: string };
};

describe("shared application conformance fixture", () => {
  it("drives the actual web action and feedback adapters without private state", async () => {
    expect(fixture.schema).toBe("application-conformance/v1");
    const outcome = {
      schema: fixture.expected.outcome_schema,
      outcome: fixture.expected.outcome,
      receipt: {
        schema: fixture.expected.receipt_schema,
        semantic_ref: fixture.action.semantic_ref,
        transport: "web",
      },
    };
    const post = vi.fn()
      .mockResolvedValueOnce(outcome)
      .mockResolvedValueOnce({
        report: {
          schema: "kitsoki.feedback.report.v1",
          anchor: { semantic_element: { plugin: "kitsoki.application", ref: fixture.feedback.ref } },
        },
        receipt: { ref: fixture.feedback.idempotency_key, deduped: false, routed: [] },
      });
    const action = await dispatchWebApplicationAction({ post }, {
      action: fixture.action.id,
      input: fixture.action.input,
      session_id: fixture.session_id,
      frame_revision: fixture.frame_revision,
    }, fixture.page);
    expect(action).toEqual(outcome);
    const feedback = await submitApplicationFeedback(
      { post },
      { session_id: fixture.session_id, page: fixture.page } as Pick<ApplicationFrame, "session_id" | "page">,
      fixture.feedback.ref,
      fixture.feedback.instruction,
      { kind: fixture.feedback.kind, idempotencyKey: fixture.feedback.idempotency_key },
    );
    expect(feedback.report.anchor.semantic_element.ref).toBe(fixture.feedback.ref);
    const encoded = JSON.stringify(post.mock.calls);
    for (const excluded of fixture.feedback.excluded) expect(encoded).not.toContain(excluded);
  });
});
