import { describe, it, expect } from "vitest";
import { mount } from "@vue/test-utils";
import ChatTranscript from "../../src/components/ChatTranscript.vue";

// These exercise the agent-text markdown path (the engine ships rendered text;
// the browser only formats markdown, never evaluates pongo) and confirm user
// text is rendered literally (escaped), not as markdown/HTML.
describe("ChatTranscript", () => {
  it("formats agent view: heading line, bold, inline code — and PRESERVES line structure", () => {
    const wrapper = mount(ChatTranscript, {
      props: {
        transcript: [
          {
            role: "agent",
            // Two numbered items on separate lines must NOT be joined into one.
            text: "## PRD\n1. first question\n2. second question\nSay **ready**, or use `quit`.",
          },
        ],
      },
    });
    const view = wrapper.find(".chat-view");
    expect(view.exists()).toBe(true);
    expect(view.find(".cv-h").text()).toBe("PRD");
    expect(view.find("strong").text()).toBe("ready");
    expect(view.find("code").text()).toBe("quit");
    // The two list lines stay on distinct lines (verbatim newlines preserved),
    // never collapsed into a run-on paragraph.
    expect(view.text()).toContain("1. first question\n2. second question");
  });

  it("escapes HTML in agent text (no injection via the view)", () => {
    const wrapper = mount(ChatTranscript, {
      props: {
        transcript: [{ role: "agent", text: "danger <img src=x onerror=alert(1)>" }],
      },
    });
    const html = wrapper.find(".chat-view").html();
    expect(html).not.toContain("<img");
    expect(html).toContain("&lt;img");
  });

  it("renders user text literally, not as markdown", () => {
    const wrapper = mount(ChatTranscript, {
      props: {
        transcript: [{ role: "user", text: "I want **a CLI** for X" }],
      },
    });
    const userRow = wrapper.find(".chat-text");
    expect(userRow.exists()).toBe(true);
    // Literal asterisks preserved; no <strong>.
    expect(userRow.text()).toContain("**a CLI**");
    expect(userRow.find("strong").exists()).toBe(false);
  });
});
