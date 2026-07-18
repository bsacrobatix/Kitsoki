/**
 * Unit tests for the webauth 401 handling added to src/transport/transport.ts
 * HttpTransport: a login-gated server (public deployment) turns an expired or
 * missing session into HTTP 401, and the transport must bounce the tab to
 * /auth/login instead of leaving the SPA stuck on a failed call or a silently
 * reconnect-looping SSE stream. No real network calls; fetch is mocked.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type { HttpTransport as HttpTransportType } from "../../src/transport/transport.js";

function jsonResponse(body: unknown, status: number): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("HttpTransport 401 handling", () => {
  let fetchMock: ReturnType<typeof vi.fn>;
  let assignSpy: ReturnType<typeof vi.fn>;
  let HttpTransport: typeof HttpTransportType;

  beforeEach(async () => {
    // The module keeps a one-shot "already redirecting" guard so a burst of
    // in-flight 401s only navigates once; reset the module between cases so
    // each test observes a fresh guard.
    vi.resetModules();
    ({ HttpTransport } = await import("../../src/transport/transport.js"));

    fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    assignSpy = vi.fn();
    vi.stubGlobal("window", {
      location: { pathname: "/some/page", search: "?x=1", assign: assignSpy },
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("call() redirects to /auth/login on a 401 and preserves the current URL as ?next=", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: "unauthenticated" }, 401));
    const transport = new HttpTransport("/");

    await expect(transport.call("runstatus.session.submit", {}, 1)).rejects.toThrow();

    expect(assignSpy).toHaveBeenCalledTimes(1);
    const [url] = assignSpy.mock.calls[0] as [string];
    expect(url).toBe(`/auth/login?next=${encodeURIComponent("/some/page?x=1")}`);
  });

  it("call() does not redirect on a non-401 error", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: "boom" }, 500));
    const transport = new HttpTransport("/");

    await expect(transport.call("runstatus.session.submit", {}, 1)).rejects.toThrow();

    expect(assignSpy).not.toHaveBeenCalled();
  });

  it("postEventStream() redirects to login on a 401", async () => {
    fetchMock.mockResolvedValueOnce(new Response("", { status: 401 }));
    const transport = new HttpTransport("/");

    await expect(
      transport.postEventStream("rpc/turn-stream", {}, {
        onFrame: () => {},
        reduce: () => undefined,
      })
    ).rejects.toThrow();

    expect(assignSpy).toHaveBeenCalledTimes(1);
  });
});
