import { describe, it, expect } from "vitest";
import { mount } from "@vue/test-utils";

import ViewElement from "../../src/components/ViewElement.vue";
import type { ViewElement as VE } from "../../src/types.js";

function render(element: VE) {
  return mount(ViewElement, { props: { element } });
}

describe("ViewElement", () => {
  it("renders prose as <p> with inline code", () => {
    const w = render({
      Kind: "prose",
      Source: "Hello `world` of traces.\n\nSecond paragraph.",
    });
    const ps = w.findAll("p.ve-prose");
    expect(ps.length).toBe(2);
    expect(ps[0].text()).toContain("Hello");
    expect(ps[0].find("code.ve-inline-code").text()).toBe("world");
    expect(ps[1].text()).toBe("Second paragraph.");
    w.unmount();
  });

  it("renders heading as <h3>", () => {
    const w = render({ Kind: "heading", Source: "Section Title" });
    const h = w.find("h3.ve-heading");
    expect(h.exists()).toBe(true);
    expect(h.text()).toBe("Section Title");
    w.unmount();
  });

  it("renders code as <pre><code>", () => {
    const w = render({ Kind: "code", Source: "let x = 1;" });
    const pre = w.find("pre.ve-code");
    expect(pre.exists()).toBe(true);
    expect(pre.find("code").text()).toBe("let x = 1;");
    w.unmount();
  });

  it("renders list as <ul><li> with labels and hints", () => {
    const w = render({
      Kind: "list",
      Items: [
        { Label: "first", Hint: "a hint" },
        { Label: "second" },
      ],
    });
    const lis = w.findAll("ul.ve-list li");
    expect(lis.length).toBe(2);
    expect(lis[0].find(".ve-list-label").text()).toBe("first");
    expect(lis[0].find(".ve-list-hint").text()).toBe("a hint");
    expect(lis[1].find(".ve-list-hint").exists()).toBe(false);
    w.unmount();
  });

  it("renders kv as a definition list of key/value", () => {
    const w = render({
      Kind: "kv",
      Pairs: [
        { Key: "state", Value: "proposal_draft" },
        { Key: "turn", Value: "7" },
      ],
    });
    const dl = w.find("dl.ve-kv");
    expect(dl.exists()).toBe(true);
    const dts = w.findAll("dt.ve-kv-key");
    const dds = w.findAll("dd.ve-kv-value");
    expect(dts.map((d) => d.text())).toEqual(["state", "turn"]);
    expect(dds.map((d) => d.text())).toEqual(["proposal_draft", "7"]);
    w.unmount();
  });

  it("renders banner as a styled callout with marker and subtitle", () => {
    const w = render({
      Kind: "banner",
      Source: "Heads up",
      Subtitle: "details here",
      Marker: "!",
      Color: "warn",
    });
    const b = w.find(".ve-banner");
    expect(b.exists()).toBe(true);
    expect(b.classes()).toContain("banner--warn");
    expect(b.find(".ve-banner-marker").text()).toBe("!");
    expect(b.find(".ve-banner-text").text()).toBe("Heads up");
    expect(b.find(".ve-banner-subtitle").text()).toBe("details here");
    w.unmount();
  });

  it("renders choice as a labeled option line", () => {
    const w = render({
      Kind: "choice",
      ChoicePrompt: "Pick one",
      ChoiceIntent: "select_option",
    });
    const c = w.find(".ve-choice");
    expect(c.exists()).toBe(true);
    expect(c.find(".ve-choice-prompt").text()).toBe("Pick one");
    expect(c.find(".ve-choice-intent").text()).toContain("select_option");
    w.unmount();
  });

  it("renders template kind as prose paragraphs", () => {
    const w = render({ Kind: "template", Source: "resolved text" });
    const ps = w.findAll("p.ve-prose");
    expect(ps.length).toBe(1);
    expect(ps[0].text()).toBe("resolved text");
    w.unmount();
  });

  it("tolerates null Items / Pairs from Go zero-value marshalling", () => {
    const list = render({ Kind: "list", Items: null });
    expect(list.findAll("li").length).toBe(0);
    list.unmount();
    const kv = render({ Kind: "kv", Pairs: null });
    expect(kv.findAll("dt").length).toBe(0);
    kv.unmount();
  });
});
