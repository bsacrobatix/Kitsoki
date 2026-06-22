import { describe, it, expect, vi, beforeEach } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { setActivePinia, createPinia } from "pinia";
import type { TurnResult } from "../../src/types.js";

const route = vi.hoisted(() => ({
  path: "/s/s1/chat",
  query: { chat: "chat-1" } as Record<string, string>,
  params: { sessionId: "s1" },
}));
const replace = vi.hoisted(() => vi.fn());
const showChat = vi.hoisted(() => vi.fn());

const dataSource = {
  getSession: vi.fn().mockResolvedValue({
    session_id: "s1",
    app_id: "demo",
    current_state: "idle",
    turn: 0,
    started_at: "2026-06-04T00:00:00Z",
    terminal: false,
  }),
  getApp: vi.fn().mockResolvedValue({ id: "demo", name: "Demo", root: "idle", states: {} }),
  getMermaid: vi.fn().mockResolvedValue({ source: "", node_map: {} }),
  getTrace: vi.fn().mockResolvedValue({ events: [], last_turn: 0 }),
  subscribe: vi.fn().mockReturnValue(() => {}),
  view: vi.fn(
    (): Promise<TurnResult> =>
      Promise.resolve({
        mode: "transitioned",
        state: "idle",
        view: "Opening",
        typed_view: { Source: "", Elements: [] },
        allowed_intents: [],
        intents: [],
        turn_number: 0,
      }),
  ),
};

vi.mock("../../src/data/source.js", () => ({
  createDataSource: () => dataSource,
}));

vi.mock("../../src/data/live-source.js", () => ({
  TurnCancelledError: class TurnCancelledError extends Error {},
  LiveSource: vi.fn().mockImplementation(() => ({
    showChat,
  })),
}));

vi.mock("vue-router", () => ({
  useRoute: () => route,
  useRouter: () => ({ replace }),
  RouterLink: { props: ["to"], template: '<a :href="to"><slot /></a>' },
}));

import InteractiveView from "../../src/views/InteractiveView.vue";

const mountOpts = {
  props: { sessionId: "s1" },
  global: {
    stubs: {
      RouterLink: { props: ["to"], template: '<a :href="to"><slot /></a>' },
      StateDiagram: true,
      TraceTimeline: true,
      ChatTranscript: true,
      InputBar: true,
      StoryFreshness: {
        template: '<div data-testid="story-freshness-widget"></div>',
      },
      MetaButton: {
        props: ["placement"],
        template: '<div data-testid="meta-launcher" :data-placement="placement || \'floating\'"></div>',
      },
    },
  },
};

describe("InteractiveView focused chat context", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    showChat.mockReset();
    showChat.mockResolvedValue({
      ok: true,
      chat: {
        id: "chat-1",
        app_id: "demo",
        room: "agent",
        scope_key: "scope",
        title: "Background Claude",
        status: "active",
        created_at_unix_micro: 1,
        updated_at_unix_micro: 2,
        last_active_at_unix_micro: 3,
      },
      pty: {
        chat_id: "chat-1",
        tmux_session: "kit-bg",
        tmux_host: "devbox",
        mode: "pty_background",
        created_at_unix_micro: 4,
        updated_at_unix_micro: 5,
      },
      messages: [
        { chat_id: "chat-1", seq: 0, role: "user", content: "check the flaky test", created_at_unix_micro: 6 },
        { chat_id: "chat-1", seq: 1, role: "assistant", content: "the failure is in setup", created_at_unix_micro: 7 },
      ],
    });
    replace.mockReset();
    route.query = { chat: "chat-1" };
    sessionStorage.clear();
  });

  it("loads and renders focused context from the chat query", async () => {
    const wrapper = mount(InteractiveView, mountOpts);
    await flushPromises();

    expect(showChat).toHaveBeenCalledWith("s1", "chat-1");
    expect(wrapper.find('[data-testid="focused-chat"]').text()).toContain("Background Claude");
    expect(wrapper.find('[data-testid="focused-chat"]').text()).toContain("tmux kit-bg");
    expect(wrapper.find('[data-testid="focused-chat"]').text()).toContain("check the flaky test");
    expect(wrapper.find('[data-testid="focused-chat"]').text()).toContain("the failure is in setup");

    await wrapper.find('[data-testid="focused-chat-close"]').trigger("click");
    expect(replace).toHaveBeenCalledWith({ path: "/s/s1/chat", query: {} });
    wrapper.unmount();
  });
});
