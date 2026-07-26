import { flushPromises, mount } from "@vue/test-utils";
import { defineComponent, h } from "vue";
import { describe, expect, it, vi } from "vitest";
import {
  ApplicationFrameRenderer,
  inspectApplicationSemanticElement,
  type ApplicationActionDispatcher,
  type ApplicationFrame,
  type ApplicationSemanticKind,
  type ApplicationSemanticNode,
} from "../../src/application/index.js";

function semantic(
  ref: string,
  kind: ApplicationSemanticKind,
  name: string,
  description: string,
  member = ref
): ApplicationSemanticNode {
  return {
    ref,
    kind,
    name,
    description,
    source: { story: "demo", member, program_node: member },
  };
}

const regionSemantic = semantic(
  "demo.region.main",
  "region",
  "Main work",
  "Contains the current work and its available operations.",
  "application.pages.home.regions.main"
);

function frame(overrides: Partial<ApplicationFrame> = {}): ApplicationFrame {
  return {
    schema: "application-frame/v1",
    application_id: "demo",
    session_id: "session-1",
    revision: 12,
    page: "home",
    semantic: semantic("demo.application", "application", "Demo", "Exercises the default renderer."),
    page_semantic: semantic("demo.page.home", "page", "Home", "The primary application page."),
    workflow: { state: "ready", allowed_intents: ["open"] },
    navigation: [{
      id: "home",
      page: "home",
      semantic: semantic("demo.nav.home", "navigation", "Home", "Open the primary page."),
      state: { enabled: true, selected: true },
    }],
    pages: [{
      id: "home",
      current: true,
      semantic: semantic("demo.page.home", "page", "Home", "The primary application page."),
    }],
    components: [{
      id: "demo.change-list",
      semantic: semantic(
        "demo.component.change-list",
        "component",
        "Change list",
        "Presents changes available for inspection."
      ),
      fallback: "list",
    }],
    handlers: [],
    actions: [],
    regions: [{
      id: "main",
      semantic: regionSemantic,
      cards: [{
        id: "changes",
        semantic: semantic(
          "demo.card.changes",
          "card",
          "Active changes",
          "Lists changes available for inspection."
        ),
        body: [
          { kind: "prose", value: "Two changes need review." },
          {
            kind: "component",
            component: "demo.change-list",
            props: { count: 2 },
          },
        ],
        actions: [{
          id: "demo.change.open",
          handler: "demo.change.open",
          enabled: true,
          semantic: semantic(
            "demo.action.change-open",
            "action",
            "Open change",
            "Open the selected change for inspection."
          ),
        }, {
          id: "demo.change.archive",
          enabled: false,
          semantic: semantic(
            "demo.action.change-archive",
            "action",
            "Archive",
            "Archive the selected change."
          ),
        }],
        state: { visible: true, selected: false },
      }],
    }],
    errors: [],
    capabilities: { presentation: ["typed-elements", "custom-components"] },
    ...overrides,
  };
}

describe("application-frame/v1 default renderer", () => {
  it("renders navigation, semantic regions, cards, body, custom components, and actions", () => {
    const ChangeList = defineComponent({
      props: { count: Number },
      setup(props) {
        return () => h("output", { "data-testid": "change-list" }, `${props.count} changes`);
      },
    });
    const wrapper = mount(ApplicationFrameRenderer, {
      props: {
        frame: frame(),
        dispatch: vi.fn(),
        components: { "demo.change-list": ChangeList },
      },
    });

    expect(wrapper.attributes("data-application-id")).toBe("demo");
    expect(wrapper.attributes("data-frame-revision")).toBe("12");
    expect(wrapper.attributes("data-semantic-ref")).toBe("demo.application");
    expect(wrapper.get("[data-semantic-ref='demo.nav.home']").attributes("aria-current")).toBe("page");
    expect(wrapper.get("[data-semantic-ref='demo.region.main']").attributes("data-program-node"))
      .toBe("application.pages.home.regions.main");
    expect(wrapper.get("[data-semantic-ref='demo.card.changes']").text()).toContain("Two changes need review.");
    expect(wrapper.get("[data-testid='change-list']").text()).toBe("2 changes");
    expect(wrapper.get("[data-semantic-ref='demo.action.change-open']").text()).toBe("Open change");
    expect(wrapper.get("[data-semantic-ref='demo.action.change-archive']").attributes())
      .toHaveProperty("disabled");
  });

  it("dispatches exact action envelopes and emits page navigation", async () => {
    const dispatch = vi.fn<ApplicationActionDispatcher>().mockResolvedValue({ ok: true });
    const wrapper = mount(ApplicationFrameRenderer, {
      props: { frame: frame(), dispatch },
    });

    await wrapper.get("[data-semantic-ref='demo.action.change-open']").trigger("click");
    await flushPromises();
    expect(dispatch).toHaveBeenNthCalledWith(1, {
      action: "demo.change.open",
      input: {},
      session_id: "session-1",
      frame_revision: 12,
    });

    await wrapper.get("[data-semantic-ref='demo.nav.home']").trigger("click");
    expect(wrapper.emitted("navigate")).toEqual([["home"]]);
    expect(dispatch).toHaveBeenCalledTimes(1);
  });

  it("passes the dispatcher and frame context to registered components", async () => {
    const dispatch = vi.fn<ApplicationActionDispatcher>().mockResolvedValue({ ok: true });
    const ChangeList = defineComponent({
      props: {
        frame: { type: Object, required: true },
        dispatch: { type: Function, required: true },
      },
      setup(props) {
        return () => h("button", {
          "data-testid": "component-action",
          onClick: () => props.dispatch({
            action: "demo.component.refresh",
            input: { scope: "all" },
            session_id: (props.frame as ApplicationFrame).session_id,
            frame_revision: (props.frame as ApplicationFrame).revision,
          }),
        }, "Refresh");
      },
    });
    const wrapper = mount(ApplicationFrameRenderer, {
      props: {
        frame: frame(),
        dispatch,
        components: { "demo.change-list": ChangeList },
      },
    });

    await wrapper.get("[data-testid='component-action']").trigger("click");
    expect(dispatch).toHaveBeenCalledWith({
      action: "demo.component.refresh",
      input: { scope: "all" },
      session_id: "session-1",
      frame_revision: 12,
    });
  });

  it("uses registry and frame component fallbacks and reports unknown elements", () => {
    const current = frame();
    const card = current.regions![0]!.cards![0]!;
    const wrapper = mount(ApplicationFrameRenderer, {
      props: {
        frame: {
          ...current,
          regions: [{
            ...current.regions![0]!,
            cards: [{
              ...card,
              body: [
                card.body![1]!,
                { kind: "component", component: "demo.registry-only", props: {} },
                { kind: "future-map" },
              ],
            }],
          }],
        },
        dispatch: vi.fn(),
        components: {
          "demo.registry-only": {
            fallback: { kind: "status", value: "Registry fallback" },
          },
        },
      },
    });

    expect(wrapper.findAll("[data-component-fallback]")).toHaveLength(2);
    expect(wrapper.text()).toContain('{"count":2}');
    expect(wrapper.text()).toContain("Registry fallback");
    expect(wrapper.get("[data-body-kind='future-map'] [role='alert']").text())
      .toBe("Unable to render future-map content.");
  });

  it("blocks dispatch for stale sessions and surfaces frame and dispatch errors", async () => {
    const dispatch = vi.fn<ApplicationActionDispatcher>().mockResolvedValue({
      ok: false,
      error: "The action was rejected.",
    });
    const stale = mount(ApplicationFrameRenderer, {
      props: {
        frame: frame({
          errors: [{ code: "STALE_FRAME", message: "The story schema changed." }],
        }),
        dispatch,
      },
    });
    expect(stale.get("[data-testid='application-stale']").text()).toContain("The story schema changed.");
    expect(stale.get("[data-semantic-ref='demo.action.change-open']").attributes()).toHaveProperty("disabled");
    await stale.get("[data-semantic-ref='demo.action.change-open']").trigger("click");
    expect(dispatch).not.toHaveBeenCalled();

    const ready = mount(ApplicationFrameRenderer, {
      props: { frame: frame(), dispatch },
    });
    await ready.get("[data-semantic-ref='demo.action.change-open']").trigger("click");
    await flushPromises();
    expect(ready.get("[data-testid='application-dispatch-error']").text())
      .toBe("The action was rejected.");
  });

  it("inspects the nearest semantic root from canonical frame metadata", () => {
    const current = frame();
    const wrapper = mount(ApplicationFrameRenderer, {
      props: { frame: current, dispatch: vi.fn() },
    });
    const heading = wrapper.get("[data-semantic-ref='demo.region.main'] h2").element;
    const inspected = inspectApplicationSemanticElement(heading, current);

    expect(inspected).toMatchObject({
      kind: "semantic_element",
      semantic_element: {
        plugin: "kitsoki.application",
        ref: "demo.region.main",
        semantic_kind: "region",
        label: "Main work",
        description: "Contains the current work and its available operations.",
        data: {
          application_id: "demo",
          frame_revision: 12,
          program_node: "application.pages.home.regions.main",
          story: "demo",
          member: "application.pages.home.regions.main",
        },
      },
    });
    expect(inspected?.element).toBe(heading.parentElement?.parentElement);
    expect(inspectApplicationSemanticElement(document.body, current)).toBeNull();
  });
});
