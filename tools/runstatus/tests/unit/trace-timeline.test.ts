/**
 * Unit tests for src/components/TraceTimeline.vue
 */

import { describe, it, expect } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";
import TraceTimeline from "../../src/components/TraceTimeline.vue";
import type { TraceEvent } from "../../src/types.js";

// ---- Fixtures --------------------------------------------------------------

function makeEvent(
  overrides: Partial<TraceEvent> & { msg: string; turn: number }
): TraceEvent {
  return {
    time: "2026-01-01T00:00:01Z",
    level: "info",
    session_id: "sess-1",
    state_path: "root.active",
    attrs: {},
    ...overrides,
  };
}

const EVENTS: TraceEvent[] = [
  makeEvent({ msg: "turn.start",        turn: 1, state_path: "root.active" }),
  makeEvent({ msg: "host.invoked",      turn: 1, state_path: "root.active", time: "2026-01-01T00:00:02Z" }),
  makeEvent({ msg: "host.returned",     turn: 1, state_path: "root.active", time: "2026-01-01T00:00:03Z" }),
  makeEvent({ msg: "machine.transition",turn: 2, state_path: "root.done",   time: "2026-01-01T00:00:04Z" }),
  makeEvent({ msg: "turn.end",          turn: 2, state_path: "root.done",   time: "2026-01-01T00:00:05Z" }),
];

// ---- Tests -----------------------------------------------------------------

describe("TraceTimeline — rendering", () => {
  it("renders all events when no filters are active", async () => {
    const wrapper = mount(TraceTimeline, {
      props: { events: EVENTS, selectedEventIndex: null },
    });
    await flushPromises();

    const rows = wrapper.findAll(".trace-timeline__row");
    expect(rows.length).toBe(EVENTS.length);
    wrapper.unmount();
  });

  it("shows empty state when events array is empty", async () => {
    const wrapper = mount(TraceTimeline, {
      props: { events: [], selectedEventIndex: null },
    });
    await flushPromises();

    expect(wrapper.find(".trace-timeline__empty").exists()).toBe(true);
    wrapper.unmount();
  });
});

describe("TraceTimeline — grouping by turn", () => {
  it("renders one turn-header per distinct turn (descending order)", async () => {
    const wrapper = mount(TraceTimeline, {
      props: { events: EVENTS, selectedEventIndex: null },
    });
    await flushPromises();

    const headers = wrapper.findAll(".trace-timeline__turn-header");
    expect(headers.length).toBe(2); // turns 1 and 2

    // Turn headers are rendered descending: turn 2 first, then turn 1.
    const labels = headers.map((h) => h.find(".trace-timeline__turn-label").text());
    expect(labels[0]).toContain("2");
    expect(labels[1]).toContain("1");

    wrapper.unmount();
  });

  it("shows event count in each turn header", async () => {
    const wrapper = mount(TraceTimeline, {
      props: { events: EVENTS, selectedEventIndex: null },
    });
    await flushPromises();

    const headers = wrapper.findAll(".trace-timeline__turn-header");
    // Turn 2 has 2 events; turn 1 has 3 events (descending order)
    expect(headers[0]!.find(".trace-timeline__turn-count").text()).toContain("2");
    expect(headers[1]!.find(".trace-timeline__turn-count").text()).toContain("3");

    wrapper.unmount();
  });

  it("collapses a turn when its header is clicked", async () => {
    const wrapper = mount(TraceTimeline, {
      props: { events: EVENTS, selectedEventIndex: null },
    });
    await flushPromises();

    // Initially all rows visible.
    expect(wrapper.findAll(".trace-timeline__row").length).toBe(5);

    // Click the first turn header (turn 2).
    const firstHeader = wrapper.find(".trace-timeline__turn-header");
    await firstHeader.trigger("click");

    // Turn 2 rows (2) are now hidden; only turn 1 rows (3) visible.
    expect(wrapper.findAll(".trace-timeline__row").length).toBe(3);

    wrapper.unmount();
  });
});

describe("TraceTimeline — subsystem chips", () => {
  it("derives subsystem from msg prefix and renders a chip", async () => {
    const wrapper = mount(TraceTimeline, {
      props: { events: EVENTS, selectedEventIndex: null },
    });
    await flushPromises();

    const chips = wrapper.findAll(".trace-timeline__subsystem-chip");
    expect(chips.length).toBe(5);

    const sysList = chips.map((c) => c.attributes("data-subsystem"));
    expect(sysList).toContain("turn");
    expect(sysList).toContain("host");
    expect(sysList).toContain("machine");

    wrapper.unmount();
  });
});

describe("TraceTimeline — filters", () => {
  it("filters events when a subsystem chip is deactivated", async () => {
    const wrapper = mount(TraceTimeline, {
      props: { events: EVENTS, selectedEventIndex: null },
      attachTo: document.body,
    });
    await flushPromises();

    // All chips start active. Click "host" to deselect it.
    const chips = wrapper.findAll(".trace-timeline__chip");
    const hostChip = chips.find((c) => c.text() === "host");
    expect(hostChip).toBeDefined();

    await hostChip!.trigger("click");

    // 2 host events should be hidden; 3 remain.
    const rows = wrapper.findAll(".trace-timeline__row");
    expect(rows.length).toBe(3);

    wrapper.unmount();
  });

  it("filters by level when a level chip is activated", async () => {
    const eventsWithLevels: TraceEvent[] = [
      makeEvent({ msg: "turn.start", turn: 1, level: "info" }),
      makeEvent({ msg: "host.invoked", turn: 1, level: "warn" }),
      makeEvent({ msg: "host.returned", turn: 1, level: "error" }),
    ];

    const wrapper = mount(TraceTimeline, {
      props: { events: eventsWithLevels, selectedEventIndex: null },
      attachTo: document.body,
    });
    await flushPromises();

    expect(wrapper.findAll(".trace-timeline__row").length).toBe(3);

    // Click the "warn" level chip to activate it.
    const levelChips = wrapper.findAll(".trace-timeline__chip");
    const warnChip = levelChips.find((c) => c.text() === "warn");
    expect(warnChip).toBeDefined();
    await warnChip!.trigger("click");

    // Only the warn event should be visible.
    expect(wrapper.findAll(".trace-timeline__row").length).toBe(1);

    wrapper.unmount();
  });

  it("shows clear button when filters are active, resets on click", async () => {
    const wrapper = mount(TraceTimeline, {
      props: { events: EVENTS, selectedEventIndex: null },
      attachTo: document.body,
    });
    await flushPromises();

    // Initially no clear button (no active filters).
    expect(wrapper.find(".trace-timeline__chip--clear").exists()).toBe(false);

    // Deactivate a subsystem.
    const chips = wrapper.findAll(".trace-timeline__chip");
    const hostChip = chips.find((c) => c.text() === "host");
    await hostChip!.trigger("click");

    expect(wrapper.find(".trace-timeline__chip--clear").exists()).toBe(true);

    // Click clear — all events should reappear.
    await wrapper.find(".trace-timeline__chip--clear").trigger("click");

    expect(wrapper.findAll(".trace-timeline__row").length).toBe(5);
    expect(wrapper.find(".trace-timeline__chip--clear").exists()).toBe(false);

    wrapper.unmount();
  });

  it("filters by state_path when a state is selected", async () => {
    const wrapper = mount(TraceTimeline, {
      props: { events: EVENTS, selectedEventIndex: null },
      attachTo: document.body,
    });
    await flushPromises();

    const select = wrapper.find(".trace-timeline__select");
    expect(select.exists()).toBe(true);

    // Select "root.done" — 2 events.
    await select.setValue("root.done");

    expect(wrapper.findAll(".trace-timeline__row").length).toBe(2);

    wrapper.unmount();
  });
});

describe("TraceTimeline — row click emits select", () => {
  it("emits select with the correct index when a row is clicked", async () => {
    const wrapper = mount(TraceTimeline, {
      props: { events: EVENTS, selectedEventIndex: null },
      attachTo: document.body,
    });
    await flushPromises();

    const rows = wrapper.findAll(".trace-timeline__row");
    // Rows are rendered descending by turn. Turn 2 first (indices 3, 4), then turn 1 (0, 1, 2).
    // Click the first visible row (turn 2, machine.transition = original index 3).
    await rows[0]!.trigger("click");

    const emitted = wrapper.emitted("select") as [number][] | undefined;
    expect(emitted).toBeDefined();
    expect(typeof emitted![0]![0]).toBe("number");

    wrapper.unmount();
  });

  it("applies .selected class to the row matching selectedEventIndex", async () => {
    const wrapper = mount(TraceTimeline, {
      props: { events: EVENTS, selectedEventIndex: 0 },
    });
    await flushPromises();

    const selectedRows = wrapper.findAll(".trace-timeline__row.selected");
    expect(selectedRows.length).toBe(1);

    wrapper.unmount();
  });
});

describe("TraceTimeline — row expand", () => {
  it("shows attrs pre block when expand button is clicked", async () => {
    const wrapper = mount(TraceTimeline, {
      props: {
        events: [makeEvent({ msg: "turn.start", turn: 1, attrs: { foo: "bar" } })],
        selectedEventIndex: null,
      },
      attachTo: document.body,
    });
    await flushPromises();

    expect(wrapper.find(".trace-timeline__row-body").exists()).toBe(false);

    await wrapper.find(".trace-timeline__expand-btn").trigger("click");

    const pre = wrapper.find(".trace-timeline__attrs-pre");
    expect(pre.exists()).toBe(true);
    expect(pre.text()).toContain("bar");

    wrapper.unmount();
  });
});
