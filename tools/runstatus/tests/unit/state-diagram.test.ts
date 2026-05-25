/**
 * Unit tests for src/components/StateDiagram.vue
 *
 * Mermaid is mocked to return a known SVG string containing <g id="..."> nodes
 * so we can test click binding and .current class assignment without a real
 * browser renderer.
 *
 * IMPORTANT: vi.mock() is hoisted to the top of the file by Vitest/Vite, so
 * the mock factory CANNOT reference const/let variables declared in this file
 * (they haven't been initialized yet when the factory runs).  We work around
 * this by storing the desired SVG in a plain object literal that is initialized
 * before the mock factory closure captures it.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";
import StateDiagram from "../../src/components/StateDiagram.vue";
import type { NodeRef } from "../../src/types.js";

// ---- Mutable SVG slot -------------------------------------------------------
// Accessed by the mocked mermaid.render().  Populated in beforeEach.

const _svg = { value: "" };

// ---- Mock mermaid ----------------------------------------------------------

vi.mock("mermaid", () => ({
  default: {
    initialize: vi.fn(),
    render: vi.fn().mockImplementation(() => Promise.resolve({ svg: _svg.value })),
  },
}));

// ---- Known SVG fixture -----------------------------------------------------

const MOCK_SVG = `
<svg xmlns="http://www.w3.org/2000/svg" id="__MOCK__">
  <g id="flowchart-ST_root_active-1" class="node default">
    <rect width="100" height="40" />
    <text>active</text>
  </g>
  <g id="flowchart-ST_root_done-2" class="node default">
    <rect width="100" height="40" />
    <text>done</text>
  </g>
  <g id="flowchart-STEP_root_active_0-3" class="node default">
    <rect width="60" height="30" />
    <text>greet</text>
  </g>
</svg>
`;

// ---- Fixtures --------------------------------------------------------------

const NODE_MAP: Record<string, NodeRef> = {
  ST_root_active:     { kind: "state",  ref: "root.active" },
  ST_root_done:       { kind: "state",  ref: "root.done" },
  STEP_root_active_0: { kind: "effect", ref: "root.active:on_enter:0" },
};

const MERMAID_SOURCE = `flowchart LR
  ST_root_active["active"]
  ST_root_done["done"]
  ST_root_active -->|done| ST_root_done`;

// ---- Test helpers ----------------------------------------------------------

beforeEach(() => {
  // Ensure every test gets the default SVG.
  _svg.value = MOCK_SVG;
});

async function mountDiagram(overrides: {
  mermaidSource?: string;
  nodeMap?: Record<string, NodeRef>;
  currentStatePath?: string;
} = {}) {
  const wrapper = mount(StateDiagram, {
    props: {
      mermaidSource: overrides.mermaidSource ?? MERMAID_SOURCE,
      nodeMap: overrides.nodeMap ?? NODE_MAP,
      currentStatePath: overrides.currentStatePath ?? "root.active",
    },
    attachTo: document.body,
  });
  await flushPromises();
  return wrapper;
}

// SVG variant with Mermaid 11's container-prefix on every g[id].
// e.g. id="<containerId>-flowchart-<nodeId>-<n>"
const MOCK_SVG_M11 = `
<svg xmlns="http://www.w3.org/2000/svg" id="__MOCK__">
  <g id="kitsoki-mermaid-1-flowchart-ST_root_active-1" class="node default">
    <rect width="100" height="40" />
    <text>active</text>
  </g>
  <g id="kitsoki-mermaid-1-flowchart-ST_root_done-2" class="node default">
    <rect width="100" height="40" />
    <text>done</text>
  </g>
</svg>
`;

// ---- Tests: render ---------------------------------------------------------

describe("StateDiagram — render", () => {
  afterEach(() => vi.clearAllMocks());

  it("injects the mocked SVG into the component DOM", async () => {
    const wrapper = await mountDiagram();
    expect(wrapper.find("svg").exists()).toBe(true);
    wrapper.unmount();
  });

  it("does not render an SVG when mermaidSource is empty", async () => {
    const wrapper = mount(StateDiagram, {
      props: { mermaidSource: "", nodeMap: NODE_MAP, currentStatePath: "" },
      attachTo: document.body,
    });
    await flushPromises();
    expect(wrapper.find(".state-diagram__empty").exists()).toBe(true);
    expect(wrapper.find("svg").exists()).toBe(false);
    wrapper.unmount();
  });
});

// ---- Tests: .current class -------------------------------------------------

describe("StateDiagram — .current class", () => {
  afterEach(() => vi.clearAllMocks());

  it("applies .current to the g element whose NodeRef matches currentStatePath", async () => {
    const wrapper = await mountDiagram({ currentStatePath: "root.active" });

    const activeG = wrapper.find('[id="flowchart-ST_root_active-1"]');
    expect(activeG.exists()).toBe(true);
    expect(activeG.classes()).toContain("current");

    const doneG = wrapper.find('[id="flowchart-ST_root_done-2"]');
    expect(doneG.exists()).toBe(true);
    expect(doneG.classes()).not.toContain("current");

    wrapper.unmount();
  });

  it("does not apply .current to effect nodes (kind != 'state')", async () => {
    const wrapper = await mountDiagram({ currentStatePath: "root.active:on_enter:0" });

    const effectG = wrapper.find('[id="flowchart-STEP_root_active_0-3"]');
    expect(effectG.exists()).toBe(true);
    expect(effectG.classes()).not.toContain("current");

    wrapper.unmount();
  });

  it("updates .current when currentStatePath prop changes (no full re-render)", async () => {
    const wrapper = await mountDiagram({ currentStatePath: "root.active" });

    let activeG = wrapper.find('[id="flowchart-ST_root_active-1"]');
    expect(activeG.classes()).toContain("current");

    await wrapper.setProps({ currentStatePath: "root.done" });

    activeG = wrapper.find('[id="flowchart-ST_root_active-1"]');
    const doneG = wrapper.find('[id="flowchart-ST_root_done-2"]');
    expect(activeG.classes()).not.toContain("current");
    expect(doneG.classes()).toContain("current");

    wrapper.unmount();
  });
});

// ---- Tests: Mermaid 11 container-prefix id form ---------------------------

describe("StateDiagram — Mermaid 11 container-prefix id extraction", () => {
  afterEach(() => vi.clearAllMocks());

  it("applies .current and binds clicks when ids carry the container prefix", async () => {
    _svg.value = MOCK_SVG_M11;
    const wrapper = await mountDiagram({ currentStatePath: "root.active" });

    const activeG = wrapper.find('[id="kitsoki-mermaid-1-flowchart-ST_root_active-1"]');
    expect(activeG.exists()).toBe(true);
    expect(activeG.classes()).toContain("current");

    await activeG.trigger("click");
    const emitted = wrapper.emitted("select") as [string, NodeRef][] | undefined;
    expect(emitted![0]).toEqual(["ST_root_active", { kind: "state", ref: "root.active" }]);

    wrapper.unmount();
  });
});

// ---- Tests: click → select emit --------------------------------------------

describe("StateDiagram — click emits select", () => {
  afterEach(() => vi.clearAllMocks());

  it("emits select with correct nodeId and NodeRef when a state node is clicked", async () => {
    const wrapper = await mountDiagram({ currentStatePath: "root.active" });

    const activeG = wrapper.find('[id="flowchart-ST_root_active-1"]');
    expect(activeG.exists()).toBe(true);

    await activeG.trigger("click");

    const emitted = wrapper.emitted("select") as [string, NodeRef][] | undefined;
    expect(emitted).toBeDefined();
    expect(emitted![0]).toEqual(["ST_root_active", { kind: "state", ref: "root.active" }]);

    wrapper.unmount();
  });

  it("emits select for effect nodes", async () => {
    const wrapper = await mountDiagram();

    const effectG = wrapper.find('[id="flowchart-STEP_root_active_0-3"]');
    expect(effectG.exists()).toBe(true);

    await effectG.trigger("click");

    const emitted = wrapper.emitted("select") as [string, NodeRef][] | undefined;
    expect(emitted).toBeDefined();
    expect(emitted![0]).toEqual([
      "STEP_root_active_0",
      { kind: "effect", ref: "root.active:on_enter:0" },
    ]);

    wrapper.unmount();
  });

  it("does not emit select for SVG g elements not in nodeMap", async () => {
    // Inject an SVG with an extra node whose id is NOT in the node map.
    _svg.value = MOCK_SVG.replace(
      "</svg>",
      `<g id="flowchart-UNKNOWN_NODE-99"><rect /></g></svg>`
    );
    const wrapper = await mountDiagram();

    const unknownG = wrapper.find('[id="flowchart-UNKNOWN_NODE-99"]');
    if (unknownG.exists()) {
      await unknownG.trigger("click");
    }

    // No "select" emission expected.
    expect(wrapper.emitted("select")).toBeUndefined();

    wrapper.unmount();
  });

  it("re-attaches click handlers after mermaidSource changes", async () => {
    const wrapper = await mountDiagram({ currentStatePath: "root.active" });

    // Trigger a source change — component re-renders.
    await wrapper.setProps({ mermaidSource: "flowchart LR\n  A --> B" });
    await flushPromises();

    // After re-render the mock SVG nodes are still present; click should work.
    const activeG = wrapper.find('[id="flowchart-ST_root_active-1"]');
    if (activeG.exists()) {
      await activeG.trigger("click");
      const emitted = wrapper.emitted("select");
      expect(emitted).toBeDefined();
    }

    wrapper.unmount();
  });
});
