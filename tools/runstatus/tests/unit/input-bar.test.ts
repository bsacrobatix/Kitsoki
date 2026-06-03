import { describe, it, expect } from "vitest";
import { mount } from "@vue/test-utils";
import InputBar from "../../src/components/InputBar.vue";
import type { IntentInfo } from "../../src/types.js";

const startIntent: IntentInfo = { name: "start", title: "Start", has_slots: false };
const confirmIntent: IntentInfo = {
  name: "confirm",
  title: "Confirm",
  has_slots: false,
};
const discussIntent: IntentInfo = {
  name: "discuss",
  title: "Discuss",
  text_slot: "message",
  has_slots: true,
};
const submitIntent: IntentInfo = {
  name: "submit_answers",
  title: "Submit",
  text_slot: "answer",
  has_slots: true,
};

describe("InputBar", () => {
  it("renders a button for each no-slot intent", () => {
    const wrapper = mount(InputBar, {
      props: { intents: [startIntent, confirmIntent, discussIntent] },
    });
    const buttons = wrapper.findAll(".input-bar__action-btn");
    expect(buttons.length).toBe(2);
    expect(buttons.map((b) => b.text())).toEqual(["Start", "Confirm"]);
    wrapper.unmount();
  });

  it("does not render an action button for slotted/text intents", () => {
    const wrapper = mount(InputBar, {
      props: { intents: [discussIntent] },
    });
    expect(wrapper.findAll(".input-bar__action-btn").length).toBe(0);
    expect(wrapper.find(".input-bar__composer").exists()).toBe(true);
    wrapper.unmount();
  });

  it("emits 'intent' with empty slots when a no-slot button is clicked", async () => {
    const wrapper = mount(InputBar, {
      props: { intents: [startIntent, confirmIntent] },
    });
    await wrapper.findAll(".input-bar__action-btn")[1].trigger("click");
    const ev = wrapper.emitted("intent");
    expect(ev).toBeTruthy();
    expect(ev![0]).toEqual(["confirm", {}]);
    wrapper.unmount();
  });

  it("Send emits 'send'(text, intentName) and 'intent'(name, {slot: text}) for the first text_slot intent", async () => {
    const wrapper = mount(InputBar, {
      props: { intents: [startIntent, discussIntent] },
    });
    await wrapper.find(".input-bar__input").setValue("hello there");
    await wrapper.find(".input-bar__composer").trigger("submit");

    const sendEv = wrapper.emitted("send");
    expect(sendEv).toBeTruthy();
    expect(sendEv![0]).toEqual(["hello there", "discuss"]);

    const intentEv = wrapper.emitted("intent");
    expect(intentEv).toBeTruthy();
    expect(intentEv![0]).toEqual(["discuss", { message: "hello there" }]);
    wrapper.unmount();
  });

  it("binds Send to the FIRST text_slot intent when multiple exist, and shows a selector", () => {
    const wrapper = mount(InputBar, {
      props: { intents: [discussIntent, submitIntent] },
    });
    expect(wrapper.find(".input-bar__select").exists()).toBe(true);
    const opts = wrapper.findAll(".input-bar__select option").map((o) => o.text());
    expect(opts).toEqual(["Discuss", "Submit"]);
    wrapper.unmount();
  });

  it("hides the selector when only one text_slot intent exists", () => {
    const wrapper = mount(InputBar, {
      props: { intents: [discussIntent] },
    });
    expect(wrapper.find(".input-bar__select").exists()).toBe(false);
    wrapper.unmount();
  });

  it("disables actions and composer while pending", () => {
    const wrapper = mount(InputBar, {
      props: { intents: [startIntent, discussIntent], pending: true },
    });
    expect(
      (wrapper.find(".input-bar__action-btn").element as HTMLButtonElement).disabled,
    ).toBe(true);
    expect(
      (wrapper.find(".input-bar__input").element as HTMLInputElement).disabled,
    ).toBe(true);
    wrapper.unmount();
  });

  it("does not emit on submit when the input is empty", async () => {
    const wrapper = mount(InputBar, {
      props: { intents: [discussIntent] },
    });
    await wrapper.find(".input-bar__composer").trigger("submit");
    expect(wrapper.emitted("send")).toBeFalsy();
    wrapper.unmount();
  });
});
