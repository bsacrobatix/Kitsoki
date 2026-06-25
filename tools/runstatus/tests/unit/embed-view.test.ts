/**
 * Unit tests for lib/embedView.ts — the host side of the generic `embed:view`
 * protocol an embedded artifact uses to report which place it is showing, so a
 * refine targets the slide the operator is looking at.
 */
import { describe, it, expect, vi } from "vitest";
import { parseEmbedView, installEmbedViewListener } from "../../src/lib/embedView.js";

describe("parseEmbedView", () => {
  it("parses a well-formed embed:view message", () => {
    expect(
      parseEmbedView({ type: "embed:view", producer: "slidey", scope: "9", label: "Cat Wrangling", count: 35 }),
    ).toEqual({ producer: "slidey", scope: "9", label: "Cat Wrangling", count: 35 });
  });

  it("coerces a numeric scope to a string (opaque round-trip token)", () => {
    expect(parseEmbedView({ type: "embed:view", scope: 9 })?.scope).toBe("9");
  });

  it("ignores non-embed:view and malformed messages", () => {
    expect(parseEmbedView({ type: "slidey:scene", sceneIndex: 9 })).toBeNull();
    expect(parseEmbedView({ type: "embed:view" })).toBeNull(); // no scope
    expect(parseEmbedView({ type: "embed:view", scope: "" })).toBeNull();
    expect(parseEmbedView(null)).toBeNull();
    expect(parseEmbedView("embed:view")).toBeNull();
  });
});

describe("installEmbedViewListener", () => {
  it("invokes onView for embed:view messages and tears down cleanly", () => {
    const listeners: Record<string, (ev: Event) => void> = {};
    const target = {
      addEventListener: vi.fn((t: string, h: (ev: Event) => void) => (listeners[t] = h)),
      removeEventListener: vi.fn((t: string) => delete listeners[t]),
    };
    const seen: string[] = [];
    const teardown = installEmbedViewListener((v) => seen.push(v.scope), target as never);

    listeners.message({ data: { type: "embed:view", scope: "2", label: "B" } } as MessageEvent);
    listeners.message({ data: { type: "noise" } } as MessageEvent);
    listeners.message({ data: { type: "embed:view", scope: "9" } } as MessageEvent);
    expect(seen).toEqual(["2", "9"]);

    teardown();
    expect(target.removeEventListener).toHaveBeenCalled();
  });

  it("is a no-op without a target window", () => {
    expect(() => installEmbedViewListener(() => {}, undefined)()).not.toThrow();
  });
});
