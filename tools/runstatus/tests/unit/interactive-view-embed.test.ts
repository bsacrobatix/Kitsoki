/**
 * Component test for InteractiveView.vue's EMBED layout (the VS Code webview).
 *
 * When isEmbedded() is true the interactive view drops its browser two-column
 * layout for a chat-dominant one with a hint rail: collapsed Trace + Graph cards
 * that maximize the full TraceTimeline / StateDiagram in place. This guards that
 * seam without a real webview (the full path is covered deterministically by
 * tools/vscode-kitsoki/tests/vscode-tour.e2e.spec.ts). The DataSource is mocked
 * (no live server, no LLM) and heavy children are stubbed.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { setActivePinia, createPinia } from "pinia";
import type { TurnResult } from "../../src/types.js";

const dataSource = {
  getSession: vi.fn().mockResolvedValue({
    session_id: "s1",
    app_id: "demo",
    current_state: "lobby",
    turn: 0,
    started_at: "2026-06-04T00:00:00Z",
    terminal: false,
  }),
  getApp: vi.fn().mockResolvedValue({ id: "demo", name: "Demo", root: "lobby", states: {} }),
  getMermaid: vi.fn().mockResolvedValue({ source: "graph TD;", node_map: {} }),
  getTrace: vi.fn().mockResolvedValue({ events: [], last_turn: 0 }),
  subscribe: vi.fn().mockReturnValue(() => {}),
  view: vi.fn(
    (id: string): Promise<TurnResult> =>
      Promise.resolve({
        mode: "transitioned",
        state: "lobby",
        view: `Opening for ${id}`,
        typed_view: { Source: "", Elements: [] },
        allowed_intents: [],
        intents: [],
        turn_number: 0,
      }),
  ),
};

vi.mock("../../src/data/source.js", () => ({ createDataSource: () => dataSource }));

import InteractiveView from "../../src/views/InteractiveView.vue";
import { setEmbeddedOverride } from "../../src/lib/embed.js";

const mountOpts = {
  props: { sessionId: "s1" },
  global: {
    stubs: {
      RouterLink: { props: ["to"], template: '<a :href="to"><slot /></a>' },
      StateDiagram: true,
      TraceTimeline: true,
      ChatTranscript: true,
      InputBar: true,
    },
  },
};

describe("InteractiveView — embed (VS Code) layout", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    setEmbeddedOverride(true);
    sessionStorage.clear();
  });
  afterEach(() => {
    setEmbeddedOverride(null);
  });

  it("renders the hint rail with collapsed Trace + Graph cards, not the full panels", async () => {
    const wrapper = mount(InteractiveView, mountOpts);
    await flushPromises();

    expect(wrapper.find('[data-testid="hint-rail"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="hint-trace"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="hint-graph"]').exists()).toBe(true);
    // Collapsed: the heavy panels are NOT mounted until maximized.
    expect(wrapper.find('[data-testid="trace-timeline"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="trace-diagram"]').exists()).toBe(false);

    wrapper.unmount();
  });

  it("maximizes Trace, switches to Graph, and minimizes back to the rail", async () => {
    const wrapper = mount(InteractiveView, mountOpts);
    await flushPromises();

    // Maximize Trace → the full timeline mounts, the rail cards are gone.
    await wrapper.find('[data-testid="hint-trace"]').trigger("click");
    expect(wrapper.find('[data-testid="trace-timeline"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="hint-trace"]').exists()).toBe(false);

    // Switch to Graph in place → the diagram mounts, the timeline is gone.
    await wrapper.find('[data-testid="switch-graph"]').trigger("click");
    expect(wrapper.find('[data-testid="trace-diagram"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="trace-timeline"]').exists()).toBe(false);

    // Minimize → back to the collapsed rail.
    await wrapper.find('[data-testid="expanded-minimize"]').trigger("click");
    expect(wrapper.find('[data-testid="hint-rail"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="hint-trace"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="trace-diagram"]').exists()).toBe(false);

    wrapper.unmount();
  });
});
