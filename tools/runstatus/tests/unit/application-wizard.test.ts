import { flushPromises, mount } from "@vue/test-utils";
import { defineComponent } from "vue";
import { describe, expect, it, vi } from "vitest";
import {
  ApplicationWizard,
  applicationThemeStyle,
  installApplicationComponents,
  installApplicationTheme,
  installedApplicationComponents,
  installedApplicationTheme,
  projectVSCodeNative,
  type ApplicationFrame,
  type ApplicationSemanticKind,
  type ApplicationSemanticNode,
} from "../../src/application/index.js";

function semantic(
  ref: string,
  kind: ApplicationSemanticKind,
  name: string
): ApplicationSemanticNode {
  return {
    ref,
    kind,
    name,
    description: `${name} description`,
    source: { story: "demo", member: ref },
  };
}

function wizardFrame(): ApplicationFrame {
  const submit = {
    id: "demo.submit",
    handler: "demo.submit",
    enabled: true,
    semantic: semantic("demo.action.submit", "action", "Submit"),
  } as const;
  return {
    schema: "application-frame/v1",
    application_id: "demo",
    session_id: "session-1",
    revision: 4,
    page: "details",
    semantic: semantic("demo.application", "application", "Demo"),
    page_semantic: semantic("demo.page.details", "page", "Details"),
    workflow: { state: "collecting", budget_state: "available", degradation: "full" },
    pages: [
      { id: "start", semantic: semantic("demo.page.start", "page", "Start") },
      { id: "details", current: true, semantic: semantic("demo.page.details", "page", "Details") },
      { id: "done", semantic: semantic("demo.page.done", "page", "Done") },
    ],
    regions: [{
      id: "main",
      semantic: semantic("demo.region.main", "region", "Main"),
      cards: [{
        id: "form",
        semantic: semantic("demo.card.form", "card", "Details"),
        body: [{
          kind: "form",
          props: {
            submit_action: "demo.submit",
            submit_label: "Save details",
            fields: [
              { id: "name", label: "Name", type: "text", required: true },
              { id: "count", label: "Count", type: "number", min: 2 },
            ],
          },
        }],
        actions: [submit],
      }],
    }],
    actions: [submit],
  };
}

describe("default application wizard", () => {
  it("shows progress, workflow state, and event activity", () => {
    const wrapper = mount(ApplicationWizard, {
      props: {
        frame: wizardFrame(),
        dispatch: vi.fn(),
        eventCount: 3,
        connectionState: "reconnecting",
      },
    });
    expect(wrapper.get("[data-testid='application-progress']").text()).toBe("Step 2 of 3");
    expect(wrapper.text()).toContain("Budget: available");
    expect(wrapper.text()).toContain("Mode: full");
    expect(wrapper.get("[data-testid='application-event-count']").text()).toBe("3 updates");
    expect(wrapper.text()).toContain("Reconnecting");
  });

  it("validates form fields and dispatches their values", async () => {
    const dispatch = vi.fn().mockResolvedValue({ ok: true });
    const wrapper = mount(ApplicationWizard, {
      props: { frame: wizardFrame(), dispatch },
    });
    const form = wrapper.get("[data-testid='application-wizard-form']");
    await form.trigger("submit");
    expect(wrapper.get("[role='alert']").text()).toBe("Name is required.");
    expect(dispatch).not.toHaveBeenCalled();

    const inputs = form.findAll("input");
    await inputs[0]!.setValue("Ada");
    await inputs[1]!.setValue("3");
    await form.trigger("submit");
    await flushPromises();
    expect(dispatch).toHaveBeenCalledWith({
      action: "demo.submit",
      input: { name: "Ada", count: 3 },
      session_id: "session-1",
      frame_revision: 4,
    });
  });
});

describe("application presentation extensions", () => {
  it("installs story components and maps only scoped theme tokens", () => {
    const DemoComponent = defineComponent({ template: "<p>Demo</p>" });
    installApplicationComponents({ "demo.component": DemoComponent });
    installApplicationTheme({ accent: "#06c", background: "#fff" });
    expect(installedApplicationComponents()).toEqual({ "demo.component": DemoComponent });
    expect(installedApplicationTheme()).toEqual({ background: "#fff", accent: "#06c" });
    expect(applicationThemeStyle({ background: "#fff", accent: "#06c" })).toEqual({
      "--k-bg": "#fff",
      "--k-fg-accent": "#06c",
    });
    expect(() => installApplicationComponents({ broken: null })).toThrow(/does not export/);
    expect(() => installApplicationTheme({ globalBody: "red" } as never)).toThrow(/not scoped/);
    expect(() => installApplicationTheme({ accent: 12 } as never)).toThrow(/non-empty string/);
  });

  it("projects VS Code commands from canonical frame actions", () => {
    const projection = projectVSCodeNative(wizardFrame(), {
      reuse: "web",
      commands: ["demo.submit"],
    });
    expect(projection.reuse).toBe("web");
    expect(projection.commands[0]).toMatchObject({
      id: "demo.submit",
      semantic_ref: "demo.action.submit",
      envelope: {
        action: "demo.submit",
        session_id: "session-1",
        frame_revision: 4,
      },
    });
    expect(() => projectVSCodeNative(wizardFrame(), { commands: ["demo.missing"] }))
      .toThrow(/no frame action/);
  });
});
