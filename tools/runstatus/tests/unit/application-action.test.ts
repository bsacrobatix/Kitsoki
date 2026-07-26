import { describe, expect, it, vi } from "vitest";
import { dispatchWebApplicationAction } from "../../src/application/index.js";

describe("application web action adapter", () => {
  it("uses the server-bound web method without a client transport", async () => {
    const post = vi.fn().mockResolvedValue({ frame: { session_id: "session-1" } });
    await dispatchWebApplicationAction({ post }, {
      action: "demo.open",
      input: { item_id: "item-1" },
      session_id: "session-1",
      frame_revision: 8,
    }, "home");

    expect(post).toHaveBeenCalledWith("runstatus.application.web_action", {
      action: "demo.open",
      input: { item_id: "item-1" },
      session_id: "session-1",
      frame_revision: 8,
      page: "home",
    });
  });
});
