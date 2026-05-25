/**
 * Unit tests for src/components/DetailDrawer.vue
 *
 * DetailDrawer uses <Teleport to="body">, so rendered content lands on
 * document.body rather than inside the wrapper's own element.  We query
 * document.body directly for the teleported nodes.
 */

import { describe, it, expect, afterEach } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";
import DetailDrawer from "../../src/components/DetailDrawer.vue";
import type { NodeRef, TraceEvent, AppDef } from "../../src/types.js";

// ---- Fixture AppDef --------------------------------------------------------

const APP_DEF: AppDef = {
  id: "tiny-app",
  name: "Tiny App",
  root: "root",
  states: {
    root: {
      States: {
        active: {
          Description: "Active state — doing work",
          OnEnter: [
            { Invoke: "host.greet", With: { name: "world" } },
          ],
          On: {
            done: [{ Target: "done" }],
          },
          View: { type: "text", value: "Hello" },
          Menu: ["item1", "item2"],
          Timeout: { duration: "30s", intent: "timeout" },
        },
        done: {
          Description: "Terminal state",
          OnEnter: [],
        },
      },
    },
  },
  World: {
    greeting: { type: "string", default: "" },
    count: { type: "number", default: 0 },
  },
};

// ---- Fixture events --------------------------------------------------------

function makeEvent(overrides: Partial<TraceEvent> & { msg: string }): TraceEvent {
  return {
    time: "2026-01-01T00:00:01Z",
    level: "info",
    session_id: "sess-1",
    turn: 1,
    state_path: "root.active",
    attrs: {},
    ...overrides,
  };
}

// ---- Helper ----------------------------------------------------------------

/**
 * Mount the drawer.  Because <Teleport to="body"> renders outside the wrapper,
 * use document.body to query for rendered content.
 */
function mountDrawer(
  selectedNode: NodeRef | null,
  selectedEvent: TraceEvent | null,
  appDef: AppDef = APP_DEF
) {
  return mount(DetailDrawer, {
    props: { selectedNode, selectedEvent, appDef },
    attachTo: document.body,
  });
}

/** Query document.body for a CSS selector; returns the first element or null. */
function bodyFind(selector: string): Element | null {
  return document.body.querySelector(selector);
}

/** Returns all matching elements in document.body. */
function bodyFindAll(selector: string): NodeListOf<Element> {
  return document.body.querySelectorAll(selector);
}

/** Returns the combined text content of document.body's matching element. */
function bodyText(selector: string): string {
  return bodyFind(selector)?.textContent ?? "";
}

/** Returns full document.body text. */
function bodyAllText(): string {
  return document.body.textContent ?? "";
}

// Cleanup mounted wrappers after each test.
const _wrappers: ReturnType<typeof mountDrawer>[] = [];
afterEach(() => {
  for (const w of _wrappers) w.unmount();
  _wrappers.length = 0;
});

function track(wrapper: ReturnType<typeof mountDrawer>) {
  _wrappers.push(wrapper);
  return wrapper;
}

// ---- Tests — closed / empty state ------------------------------------------

describe("DetailDrawer — closed", () => {
  it("renders nothing (no backdrop) when both selectedNode and selectedEvent are null", async () => {
    track(mountDrawer(null, null));
    await flushPromises();

    expect(bodyFind(".detail-drawer__backdrop")).toBeNull();
    expect(bodyFind(".detail-drawer")).toBeNull();
  });
});

// ---- Tests — state node ----------------------------------------------------

describe("DetailDrawer — state node", () => {
  const stateNode: NodeRef = { kind: "state", ref: "root.active" };

  it("opens and shows the state path", async () => {
    track(mountDrawer(stateNode, null));
    await flushPromises();

    expect(bodyFind(".detail-drawer")).not.toBeNull();
    expect(bodyAllText()).toContain("root.active");
  });

  it("shows Description", async () => {
    track(mountDrawer(stateNode, null));
    await flushPromises();

    expect(bodyAllText()).toContain("Active state");
  });

  it("shows OnEnter effect with Invoke name", async () => {
    track(mountDrawer(stateNode, null));
    await flushPromises();

    expect(bodyAllText()).toContain("host.greet");
  });

  it("shows Transitions table with intent and target", async () => {
    track(mountDrawer(stateNode, null));
    await flushPromises();

    const table = bodyFind(".detail-drawer__table");
    expect(table).not.toBeNull();
    expect(table!.textContent).toContain("done");
  });

  it("shows View block", async () => {
    track(mountDrawer(stateNode, null));
    await flushPromises();

    expect(bodyAllText()).toContain("View");
    expect(bodyAllText()).toContain("Hello");
  });

  it("shows Timeout", async () => {
    track(mountDrawer(stateNode, null));
    await flushPromises();

    expect(bodyAllText()).toContain("Timeout");
    expect(bodyAllText()).toContain("30s");
  });
});

// ---- Tests — effect node ----------------------------------------------------

describe("DetailDrawer — effect node", () => {
  const effectNode: NodeRef = { kind: "effect", ref: "root.active:on_enter:0" };

  it("shows Invoke name for the resolved effect", async () => {
    track(mountDrawer(effectNode, null));
    await flushPromises();

    expect(bodyAllText()).toContain("host.greet");
  });

  it("shows With block as JSON", async () => {
    track(mountDrawer(effectNode, null));
    await flushPromises();

    expect(bodyAllText()).toContain("world");
  });

  it("shows error message when effect index is out of range", async () => {
    const badNode: NodeRef = { kind: "effect", ref: "root.active:on_enter:99" };
    track(mountDrawer(badNode, null));
    await flushPromises();

    expect(bodyAllText()).toContain("not found");
  });
});

// ---- Tests — world node ----------------------------------------------------

describe("DetailDrawer — world node", () => {
  it("shows the world var definition for a single-key ref", async () => {
    const worldNode: NodeRef = { kind: "world", ref: "world:greeting" };
    track(mountDrawer(worldNode, null));
    await flushPromises();

    expect(bodyAllText()).toContain("greeting");
    expect(bodyAllText()).toContain("string");
  });

  it("shows all vars for a multi-key ref", async () => {
    const worldNode: NodeRef = { kind: "world", ref: "world:count,greeting" };
    track(mountDrawer(worldNode, null));
    await flushPromises();

    expect(bodyAllText()).toContain("count");
    expect(bodyAllText()).toContain("greeting");
  });
});

// ---- Tests — event panel: LLM event ----------------------------------------

describe("DetailDrawer — LLM event", () => {
  it("shows prompt, response, token_count", async () => {
    const event = makeEvent({
      msg: "oracle.called",
      attrs: {
        prompt: "What is the meaning of life?",
        response: "42",
        token_count: 7,
      },
    });
    track(mountDrawer(null, event));
    await flushPromises();

    expect(bodyAllText()).toContain("Prompt");
    expect(bodyAllText()).toContain("What is the meaning of life?");
    expect(bodyAllText()).toContain("42");
    expect(bodyAllText()).toContain("7");
  });

  it("truncates long prompt and shows 'Show full' button", async () => {
    const longPrompt = "x".repeat(600);
    const event = makeEvent({
      msg: "oracle.called",
      attrs: { prompt: longPrompt },
    });
    track(mountDrawer(null, event));
    await flushPromises();

    const toggleBtn = bodyFind(".detail-drawer__toggle-btn");
    expect(toggleBtn).not.toBeNull();
    expect(toggleBtn!.textContent).toContain("Show full");

    // The pre should be truncated to ≤ 500 + "…"
    const pre = bodyFind(".detail-drawer__pre");
    expect(pre).not.toBeNull();
    expect((pre!.textContent ?? "").length).toBeLessThan(600);
  });

  it("expands full prompt when 'Show full' is clicked", async () => {
    const longPrompt = "x".repeat(600);
    const event = makeEvent({
      msg: "oracle.called",
      attrs: { prompt: longPrompt },
    });
    const wrapper = track(mountDrawer(null, event));
    await flushPromises();

    const toggleBtn = bodyFind(".detail-drawer__toggle-btn") as HTMLButtonElement;
    expect(toggleBtn).not.toBeNull();
    await wrapper.vm.$nextTick();
    toggleBtn.click();
    await flushPromises();

    const pre = bodyFind(".detail-drawer__pre");
    expect((pre!.textContent ?? "").length).toBeGreaterThanOrEqual(600);
  });
});

// ---- Tests — event panel: Host event ----------------------------------------

describe("DetailDrawer — host event", () => {
  it("shows handler, input, return, duration_ms", async () => {
    const event = makeEvent({
      msg: "host.invoked",
      attrs: {
        handler: "host.greet",
        input: { name: "world" },
        return: "Hello, world!",
        duration_ms: 12,
      },
    });
    track(mountDrawer(null, event));
    await flushPromises();

    expect(bodyAllText()).toContain("host.greet");
    expect(bodyAllText()).toContain("world");
    expect(bodyAllText()).toContain("Hello, world!");
    expect(bodyAllText()).toContain("12");
  });
});

// ---- Tests — event panel: Transition event ----------------------------------

describe("DetailDrawer — transition event", () => {
  it("shows intent, from, to, guard", async () => {
    const event = makeEvent({
      msg: "machine.transition",
      attrs: {
        intent: "done",
        from: "root.active",
        to: "root.done",
        guard: "always",
      },
    });
    track(mountDrawer(null, event));
    await flushPromises();

    expect(bodyAllText()).toContain("done");
    expect(bodyAllText()).toContain("root.active");
    expect(bodyAllText()).toContain("root.done");
    expect(bodyAllText()).toContain("always");
  });
});

// ---- Tests — event panel: World write event ---------------------------------

describe("DetailDrawer — world write event", () => {
  it("shows key and value", async () => {
    const event = makeEvent({
      msg: "machine.world.set",
      attrs: { key: "greeting", value: "Hello!" },
    });
    track(mountDrawer(null, event));
    await flushPromises();

    expect(bodyAllText()).toContain("greeting");
    expect(bodyAllText()).toContain("Hello!");
  });
});

// ---- Tests — event panel: default (all attrs) -------------------------------

describe("DetailDrawer — default event", () => {
  it("renders all attrs as JSON for an unrecognised msg", async () => {
    const event = makeEvent({
      msg: "some.unknown.event",
      attrs: { custom_key: "custom_value", count: 99 },
    });
    track(mountDrawer(null, event));
    await flushPromises();

    expect(bodyAllText()).toContain("custom_key");
    expect(bodyAllText()).toContain("custom_value");
    expect(bodyAllText()).toContain("99");
  });
});

// ---- Tests — close ----------------------------------------------------------

describe("DetailDrawer — close", () => {
  it("emits close when the X button is clicked", async () => {
    const wrapper = track(mountDrawer({ kind: "state", ref: "root.active" }, null));
    await flushPromises();

    const closeBtn = bodyFind(".detail-drawer__close") as HTMLButtonElement;
    expect(closeBtn).not.toBeNull();
    closeBtn.click();
    await flushPromises();

    expect(wrapper.emitted("close")).toBeDefined();
    expect(wrapper.emitted("close")!.length).toBe(1);
  });

  it("emits close when the backdrop is clicked", async () => {
    const wrapper = track(mountDrawer({ kind: "state", ref: "root.active" }, null));
    await flushPromises();

    const backdrop = bodyFind(".detail-drawer__backdrop") as HTMLElement;
    expect(backdrop).not.toBeNull();
    backdrop.click();
    await flushPromises();

    expect(wrapper.emitted("close")).toBeDefined();
  });

  it("emits close when Escape is pressed on the drawer", async () => {
    const wrapper = track(mountDrawer({ kind: "state", ref: "root.active" }, null));
    await flushPromises();

    const drawer = bodyFind(".detail-drawer") as HTMLElement;
    expect(drawer).not.toBeNull();
    drawer.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    await flushPromises();

    expect(wrapper.emitted("close")).toBeDefined();
  });
});

// ---- Tests — both node + event visible simultaneously ----------------------

describe("DetailDrawer — node + event coexistence", () => {
  it("shows both node and event sections when both are provided", async () => {
    const node: NodeRef = { kind: "state", ref: "root.active" };
    const event = makeEvent({
      msg: "host.invoked",
      attrs: { handler: "host.greet" },
    });
    track(mountDrawer(node, event));
    await flushPromises();

    const sections = bodyFindAll(".detail-drawer__section");
    expect(sections.length).toBe(2);
  });
});
