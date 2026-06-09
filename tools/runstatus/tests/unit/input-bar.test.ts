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

  it("Send emits 'intent'(name, {slot: text}) for the first text_slot intent (no 'send' — text intents use session.submit, not session.turn)", async () => {
    const wrapper = mount(InputBar, {
      props: { intents: [startIntent, discussIntent] },
    });
    await wrapper.find(".input-bar__textarea").setValue("hello there");
    await wrapper.find(".input-bar__composer").trigger("submit");

    // Text-slot intents submit via 'intent' (→ session.submit / SubmitDirect).
    // 'send' (→ session.turn / semantic router) is only emitted by the semantic textarea.
    expect(wrapper.emitted("send")).toBeFalsy();

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
      (wrapper.find(".input-bar__textarea").element as HTMLTextAreaElement).disabled,
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

  it("shows a semantic textarea and emits 'send' (raw text) when typedView has elements but no choice/form", async () => {
    const semanticView = {
      Elements: [
        { Kind: "prose" as const, Source: "Say what you want to do." },
        { Kind: "list" as const, Items: [{ Label: "tickets" }, { Label: "drive" }] },
      ],
    };
    // No-slot intents only (nav intents like main room).
    const wrapper = mount(InputBar, {
      props: { intents: [startIntent, confirmIntent], typedView: semanticView },
    });
    // No action buttons — semantic textarea takes over.
    expect(wrapper.findAll(".input-bar__action-btn").length).toBe(0);
    expect(wrapper.find(".input-bar__textarea").exists()).toBe(true);

    await wrapper.find(".input-bar__textarea").setValue("find auth bugs");
    await wrapper.find(".input-bar__composer--semantic").trigger("submit");

    const sendEv = wrapper.emitted("send");
    expect(sendEv).toBeTruthy();
    expect(sendEv![0]).toEqual(["find auth bugs", ""]);
    wrapper.unmount();
  });

  it("does not show semantic textarea when typedView is absent (legacy path)", () => {
    const wrapper = mount(InputBar, {
      props: { intents: [startIntent, confirmIntent] },
    });
    expect(wrapper.find(".input-bar__textarea").exists()).toBe(false);
    expect(wrapper.findAll(".input-bar__action-btn").length).toBe(2);
    wrapper.unmount();
  });

  // ── Choice-element mutual-exclusion invariants ────────────────────────────────
  // These guard against a class of regression where adding a choice: element to
  // a room's view inadvertently hides the primary text input or breaks navigation.

  it("choice items present: text-slot composer is hidden even when text-slot intents exist", () => {
    // This is the exact regression that was introduced: proposal room had
    // discuss + capture_existing as text-slot intents, a choice: element was
    // added to the view, and the text-slot composer silently disappeared.
    const captureIntent: IntentInfo = {
      name: "capture_existing",
      title: "Reference Docs",
      text_slot: "paths",
      has_slots: true,
    };
    const viewWithChoice = {
      Elements: [
        {
          Kind: "choice" as const,
          ChoiceMode: "single",
          ChoicePrompt: "Actions",
          ChoiceItems: [
            { Label: "capture_existing", Intent: "capture_existing", Param: { Slot: "paths", Type: "string" } },
            { Label: "quit", Intent: "quit" },
          ],
        },
      ],
    };
    const wrapper = mount(InputBar, {
      props: { intents: [discussIntent, captureIntent], typedView: viewWithChoice },
    });
    // Choice items are shown.
    expect(wrapper.find(".input-bar__forms").exists()).toBe(true);
    // Text-slot composer must NOT be shown — discuss has nowhere to go.
    expect(wrapper.find('[data-testid="composer"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it("choice items present: semantic textarea is also hidden (choice is the only input)", () => {
    const viewWithChoice = {
      Elements: [
        {
          Kind: "choice" as const,
          ChoiceMode: "single",
          ChoicePrompt: "Actions",
          ChoiceItems: [{ Label: "quit", Intent: "quit" }],
        },
      ],
    };
    const wrapper = mount(InputBar, {
      props: { intents: [startIntent], typedView: viewWithChoice },
    });
    expect(wrapper.find('[data-testid="composer"]').exists()).toBe(false);
    expect(wrapper.find(".input-bar__composer--semantic").exists()).toBe(false);
    // But the choice buttons are shown.
    expect(wrapper.find('[data-testid="intent-actions"]').exists()).toBe(true);
    wrapper.unmount();
  });

  it("when two text-slot intents exist, dropdown defaults to the first one listed", () => {
    // Guards the priority-ordering fix: discuss must be first so it is the
    // default selection, not capture_existing ("Reference Docs").
    const captureIntent: IntentInfo = {
      name: "capture_existing",
      title: "Reference Docs",
      text_slot: "paths",
      has_slots: true,
    };
    // discuss first → should be selected by default.
    const wrapper = mount(InputBar, {
      props: { intents: [discussIntent, captureIntent] },
    });
    const select = wrapper.find<HTMLSelectElement>(".input-bar__select");
    expect(select.exists()).toBe(true);
    // First option is Discuss, and it is the active intent for the composer.
    expect(select.element.value).toBe("discuss");
    wrapper.unmount();
  });
});
