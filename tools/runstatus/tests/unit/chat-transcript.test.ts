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

  it("renders a fenced ```json block as a code box, not literal backticks", () => {
    const wrapper = mount(ChatTranscript, {
      props: {
        transcript: [
          {
            role: "agent",
            text:
              'Here is the result:\n\n```json\n{\n  "slug": "web-companion-pet"\n}\n```\n\nDone.',
          },
        ],
      },
    });
    const view = wrapper.find(".chat-view");
    const pre = view.find("pre.cv-pre");
    expect(pre.exists()).toBe(true);
    // The JSON body is inside the code box…
    expect(pre.text()).toContain('"slug": "web-companion-pet"');
    // …and the literal fence markers do NOT leak into the rendered HTML.
    expect(view.html()).not.toContain("```");
    // The language hint rides along for syntax-class hooks.
    expect(view.find("pre.cv-pre code").classes()).toContain("language-json");
  });

  it("escapes HTML inside a fenced block (no injection via code)", () => {
    const wrapper = mount(ChatTranscript, {
      props: {
        transcript: [
          { role: "agent", text: "```\n<img src=x onerror=alert(1)>\n```" },
        ],
      },
    });
    const html = wrapper.find(".chat-view").html();
    expect(html).not.toContain("<img");
    expect(html).toContain("&lt;img");
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

  it("renders a preserved stream feed as a collapsed activity section, in order", () => {
    const wrapper = mount(ChatTranscript, {
      props: {
        transcript: [
          {
            role: "agent",
            text: "Final room view.",
            stream: [
              { kind: "thinking", text: "I'll scan the docs first." },
              { kind: "tool", tool: "ToolSearch", preview: "select:WebSearch" },
              { kind: "tool", tool: "Bash", preview: "find … | grep …" },
              { kind: "thinking", text: "The spec lives in proposals." },
              { kind: "tool", tool: "Read", preview: "docs/proposals/x.md" },
            ],
          },
        ],
      },
    });
    const activity = wrapper.find("[data-testid='chat-activity']");
    expect(activity.exists()).toBe(true);
    // Collapsed by default: a <details> WITHOUT the open attribute, summary
    // counts the activity.
    expect(activity.element.hasAttribute("open")).toBe(false);
    expect(activity.find(".chat-activity__summary").text()).toBe(
      "🧠 2 thoughts · 3 tool calls"
    );
    // The feed preserves arrival order: thought, tool, tool, thought, tool.
    const rows = activity.findAll(".chat-activity__thought, .chat-activity__tool");
    expect(
      rows.map((r) => (r.classes().includes("chat-activity__thought") ? "think" : "tool"))
    ).toEqual(["think", "tool", "tool", "think", "tool"]);
    expect(rows[0]!.text()).toContain("🧠");
    expect(rows[0]!.text()).toContain("I'll scan the docs first.");
    // The final view still renders as the bubble body.
    expect(wrapper.find(".chat-view").text()).toContain("Final room view.");
  });

  it("omits the activity section when an entry carries no stream", () => {
    const wrapper = mount(ChatTranscript, {
      props: { transcript: [{ role: "agent", text: "Plain view." }] },
    });
    expect(wrapper.find("[data-testid='chat-activity']").exists()).toBe(false);
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
