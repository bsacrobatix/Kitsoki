import { describe, expect, it, vi } from "vitest";
import { ensureApplicationSession } from "../../src/application/session.js";

describe("application session bootstrap", () => {
  it("keeps the current session when it belongs to the generated application", async () => {
    const post = vi.fn()
      .mockResolvedValueOnce({ application_id: "demo" });

    await expect(ensureApplicationSession({ post }, "demo", "session-1"))
      .resolves.toBe("session-1");
    expect(post).toHaveBeenCalledWith("runstatus.application.frame", {
      session_id: "session-1",
    });
  });

  it("creates the declared application session when none is active", async () => {
    const post = vi.fn()
      .mockResolvedValueOnce([{ path: "/stories/demo/app.yaml", app_id: "demo" }])
      .mockResolvedValueOnce({ session_id: "session-new" });

    await expect(ensureApplicationSession({ post }, "demo", null))
      .resolves.toBe("session-new");
    expect(post.mock.calls).toEqual([
      ["runstatus.stories.list"],
      ["runstatus.session.new", { story_path: "/stories/demo/app.yaml" }],
    ]);
  });

  it("replaces a current session that belongs to another story", async () => {
    const post = vi.fn()
      .mockResolvedValueOnce({ application_id: "other" })
      .mockResolvedValueOnce([{ path: "/stories/demo/app.yaml", app_id: "demo" }])
      .mockResolvedValueOnce({ session_id: "session-demo" });

    await expect(ensureApplicationSession({ post }, "demo", "session-other"))
      .resolves.toBe("session-demo");
  });

  it("fails closed when the application id is missing or ambiguous", async () => {
    const missing = vi.fn().mockResolvedValue([]);
    await expect(ensureApplicationSession({ post: missing }, "demo", null))
      .rejects.toThrow('Application story "demo" is not available.');

    const ambiguous = vi.fn().mockResolvedValue([
      { path: "/one/app.yaml", app_id: "demo" },
      { path: "/two/app.yaml", app_id: "demo" },
    ]);
    await expect(ensureApplicationSession({ post: ambiguous }, "demo", null))
      .rejects.toThrow('Application story "demo" is ambiguous.');
  });
});
