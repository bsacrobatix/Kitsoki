import { afterEach, describe, expect, it } from "vitest";
import {
  observeApplicationRoutes,
  readApplicationRoutePath,
  writeApplicationRoute,
  type ApplicationFrame,
} from "../../src/application/index.js";

function frame(path: string): ApplicationFrame {
  return {
    schema: "application-frame/v1",
    application_id: "demo",
    session_id: "session-1",
    revision: 1,
    page: "change",
    route: { template: "/changes/{change_id}", params: ["change_id"] },
    route_path: path,
    route_params: { change_id: "chg-42" },
    semantic: {
      ref: "demo.application", kind: "application", name: "Demo", description: "Demo",
      source: { story: "demo", member: "application" },
    },
    page_semantic: {
      ref: "demo.page.change", kind: "page", name: "Change", description: "Change",
      source: { story: "demo", member: "application.pages.change" },
    },
    workflow: { state: "ready" },
  };
}

describe("application history adapter", () => {
  afterEach(() => window.history.replaceState({}, "", "/"));

  it("uses canonical paths for app-dev navigation and popstate-compatible history", () => {
    window.history.replaceState({}, "", "/");
    writeApplicationRoute(frame("/changes/chg-42"), "push");
    expect(window.location.pathname).toBe("/changes/chg-42");
    expect(readApplicationRoutePath(window.location)).toBe("/changes/chg-42");

    writeApplicationRoute(frame("/changes/chg-43"), "replace");
    expect(window.location.pathname).toBe("/changes/chg-43");

    const observed: string[] = [];
    const dispose = observeApplicationRoutes((path) => observed.push(path));
    window.history.replaceState({}, "", "/changes/chg-42");
    window.dispatchEvent(new PopStateEvent("popstate"));
    dispose();
    expect(observed).toEqual(["/changes/chg-42"]);
  });

  it("preserves the production bundle prefix used by the VS Code web reuse adapter", () => {
    window.history.replaceState({}, "", "/application/demo/");
    writeApplicationRoute(frame("/changes/chg-42"), "replace", window.history, window.location, "demo");
    expect(window.location.pathname).toBe("/application/demo/changes/chg-42");
    expect(readApplicationRoutePath(window.location, "demo")).toBe("/changes/chg-42");
  });
});
