import { describe, expect, it, vi } from "vitest";
import {
  inspectApplicationFeedbackTarget,
  submitApplicationFeedback,
  type ApplicationFrame,
} from "../../src/application/index.js";

describe("application feedback adapter", () => {
  it("confirms the rendered current semantic target through the canonical inspector", async () => {
    const post = vi.fn().mockResolvedValue({ node: { ref: "demo.action.open" } });
    const button = document.createElement("button");
    button.dataset.semanticRef = "demo.action.open";
    const frame = {
      application_id: "demo",
      session_id: "session-1",
      revision: 4,
      page: "review",
      semantic: {
        ref: "demo.application",
        kind: "application",
        name: "Demo",
        description: "Demo application.",
        source: { story: "demo", member: "application" },
      },
      page_semantic: {
        ref: "demo.page.review",
        kind: "page",
        name: "Review",
        description: "Review the current item.",
        source: { story: "demo", member: "application.pages.review" },
      },
      actions: [{
        id: "demo.open",
        enabled: true,
        semantic: {
          ref: "demo.action.open",
          kind: "action",
          name: "Open",
          description: "Open the selected item.",
          source: { story: "demo", member: "application.actions.demo.open" },
        },
      }],
    } as ApplicationFrame;

    const selected = await inspectApplicationFeedbackTarget({ post }, frame, button);

    expect(selected?.semantic_element.ref).toBe("demo.action.open");
    expect(post).toHaveBeenCalledWith("runstatus.application.inspect", {
      session_id: "session-1",
      page: "review",
      ref: "demo.action.open",
    });
    expect(JSON.stringify(post.mock.calls)).not.toMatch(/secret-prop|api-token|\/Users\/operator/);
  });

  it("submits only canonical identity and reviewed prose to the shared backend", async () => {
    const post = vi.fn().mockResolvedValue({
      report: {
        schema: "kitsoki.feedback.report.v1",
        anchor: {
          kind: "semantic_element",
            semantic_element: {
              plugin: "kitsoki.application",
              ref: "demo.action.open",
              data: { application_id: "demo", frame_revision: 4 },
          },
        },
      },
      receipt: { ref: "feedback-1", deduped: false, routed: [] },
    });
    const result = await submitApplicationFeedback(
      { post },
      { session_id: "session-1", page: "review" },
      "demo.action.open",
      "The summary is unclear.",
      { idempotencyKey: "feedback-1" },
    );
    expect(post).toHaveBeenCalledWith("runstatus.application.feedback", {
      session_id: "session-1",
      page: "review",
      ref: "demo.action.open",
      instruction: "The summary is unclear.",
      idempotency_key: "feedback-1",
    });
    expect(JSON.stringify({ calls: post.mock.calls, result })).not.toMatch(
      /props|secret-prop|api-token|\/Users\/operator/,
    );
    expect(result.report.anchor.semantic_element.ref).toBe("demo.action.open");
  });
});
