import { describe, expect, it, vi } from "vitest";
import { dispatchWebApplicationAction } from "../../src/application/index.js";

describe("application web action adapter", () => {
  it("uses the server-bound web method without a client transport", async () => {
    const canonical = {
      schema: "application-outcome/v1" as const,
      handler: "demo.open",
      outcome: "selected",
      output: { item_id: "item-1" },
      receipt: {
        schema: "application-receipt/v1" as const,
        id: "ar_1",
        handler_id: "demo.open",
        semantic_ref: "demo.action.open",
        effect: "read",
        routing: { requested: "exact", resolved: "exact" },
        budget: { allowed: true },
        input_digest: "sha256:input",
        output_digest: "sha256:output",
        transport: "web",
        outcome: "selected",
      },
      frame: { session_id: "session-1" },
    };
    const post = vi.fn().mockResolvedValue(canonical);
    const outcome = await dispatchWebApplicationAction({ post }, {
      action: "demo.open",
      input: { item_id: "item-1" },
      session_id: "session-1",
      frame_revision: 8,
    }, "home", { change_id: "chg-42" });

    expect(post).toHaveBeenCalledWith("runstatus.application.web_action", {
      action: "demo.open",
      input: { item_id: "item-1" },
      session_id: "session-1",
      frame_revision: 8,
      page: "home",
      route_params: { change_id: "chg-42" },
    });
    expect(outcome).toEqual(canonical);
  });
});
