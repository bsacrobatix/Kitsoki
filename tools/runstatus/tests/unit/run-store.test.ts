/**
 * Unit tests for src/stores/run.ts
 */

import { describe, it, expect, beforeEach } from "vitest";
import { setActivePinia, createPinia } from "pinia";
import { useRunStore } from "../../src/stores/run.js";
import { SnapshotSource } from "../../src/data/snapshot-source.js";
import type { Snapshot, TraceEvent } from "../../src/types.js";
import type { DataSource } from "../../src/data/source.js";

// ---- Fixture ---------------------------------------------------------------

const SNAPSHOT: Snapshot = {
  session: {
    session_id: "sess-1",
    app_id: "app-1",
    current_state: "root/review",
    turn: 3,
    started_at: "2026-01-01T00:00:00Z",
    terminal: false,
  },
  app: {
    id: "app-1",
    name: "Test App",
    root: "root",
    states: {
      "root/review": { description: "Reviewing" },
      "root/done": { description: "Done" },
    },
  },
  mermaid: {
    source: "flowchart LR\n  root_review --> root_done",
    node_map: {
      root_review: { kind: "state", ref: "root/review" },
      root_done: { kind: "state", ref: "root/done" },
      effect_0: { kind: "effect", ref: "root/review:0" },
      transition_0: { kind: "transition", ref: "root/review>root/done" },
    },
  },
  events: [
    {
      time: "2026-01-01T00:00:01Z",
      level: "info",
      msg: "TurnStarted",
      session_id: "sess-1",
      turn: 1,
      state_path: "root/review",
      attrs: {},
    },
    {
      time: "2026-01-01T00:00:02Z",
      level: "info",
      msg: "LLMCalled",
      session_id: "sess-1",
      turn: 2,
      state_path: "root/review",
      attrs: { tokens: 10 },
    },
    {
      time: "2026-01-01T00:00:03Z",
      level: "info",
      msg: "TransitionApplied",
      session_id: "sess-1",
      turn: 3,
      state_path: "root/done",
      attrs: {},
    },
  ],
};

// ---- Tests -----------------------------------------------------------------

beforeEach(() => {
  setActivePinia(createPinia());
});

describe("useRunStore — hydrate with SnapshotSource", () => {
  it("populates appDef, mermaid, events, currentStatePath after hydration", async () => {
    const store = useRunStore();
    const src = new SnapshotSource(SNAPSHOT);

    expect(store.loading).toBe(false);
    await store.hydrate(src, "sess-1");

    expect(store.appDef?.id).toBe("app-1");
    expect(store.mermaid?.source).toContain("flowchart LR");
    expect(store.events).toHaveLength(3);
    expect(store.currentStatePath).toBe("root/review");
    expect(store.terminal).toBe(false);
    expect(store.loading).toBe(false);
  });

  it("sets loading=true during hydration and false after", async () => {
    const store = useRunStore();
    const loadingStates: boolean[] = [];

    // Spy: capture loading state asynchronously via a slow source.
    let resolveGetSession!: (v: unknown) => void;
    const slowSource: DataSource = {
      getSession: () =>
        new Promise((resolve) => {
          resolveGetSession = resolve;
        }) as never,
      getApp: () => Promise.resolve(SNAPSHOT.app),
      getMermaid: () => Promise.resolve(SNAPSHOT.mermaid),
      getTrace: () =>
        Promise.resolve({ events: SNAPSHOT.events, last_turn: 3 }),
      listSessions: () => Promise.resolve([SNAPSHOT.session]),
      subscribe: () => () => undefined,
    };

    const hydratePromise = store.hydrate(slowSource, "sess-1");
    loadingStates.push(store.loading); // should be true

    resolveGetSession(SNAPSHOT.session);
    await hydratePromise;
    loadingStates.push(store.loading); // should be false

    expect(loadingStates[0]).toBe(true);
    expect(loadingStates[1]).toBe(false);
  });
});

describe("useRunStore — selectNode", () => {
  it("resolves a known nodeId from node_map", async () => {
    const store = useRunStore();
    await store.hydrate(new SnapshotSource(SNAPSHOT), "sess-1");

    store.selectNode("root_review");
    expect(store.selectedNode).toEqual({ kind: "state", ref: "root/review" });
  });

  it("resolves effect and transition node kinds", async () => {
    const store = useRunStore();
    await store.hydrate(new SnapshotSource(SNAPSHOT), "sess-1");

    store.selectNode("effect_0");
    expect(store.selectedNode?.kind).toBe("effect");

    store.selectNode("transition_0");
    expect(store.selectedNode?.kind).toBe("transition");
  });

  it("sets selectedNode to null for an unknown nodeId", async () => {
    const store = useRunStore();
    await store.hydrate(new SnapshotSource(SNAPSHOT), "sess-1");

    store.selectNode("root_review"); // set to non-null first
    store.selectNode("nonexistent_node");
    expect(store.selectedNode).toBeNull();
  });
});

describe("useRunStore — selectEvent", () => {
  it("sets selectedEventIndex", async () => {
    const store = useRunStore();
    await store.hydrate(new SnapshotSource(SNAPSHOT), "sess-1");

    store.selectEvent(2);
    expect(store.selectedEventIndex).toBe(2);

    store.selectEvent(0);
    expect(store.selectedEventIndex).toBe(0);
  });
});

describe("useRunStore — live event appending", () => {
  it("appends events and updates currentStatePath from live subscription", async () => {
    let capturedCallback: ((e: TraceEvent) => void) | null = null;

    const liveSource: DataSource = {
      listSessions: () => Promise.resolve([SNAPSHOT.session]),
      getSession: () => Promise.resolve(SNAPSHOT.session),
      getApp: () => Promise.resolve(SNAPSHOT.app),
      getMermaid: () => Promise.resolve(SNAPSHOT.mermaid),
      getTrace: () =>
        Promise.resolve({ events: SNAPSHOT.events.slice(0, 1), last_turn: 1 }),
      subscribe: (_sessionId, onEvent) => {
        capturedCallback = onEvent;
        return () => undefined;
      },
    };

    const store = useRunStore();
    await store.hydrate(liveSource, "sess-1");

    expect(store.events).toHaveLength(1);

    // Simulate a live event arriving.
    const newEvent: TraceEvent = {
      time: new Date().toISOString(),
      level: "info",
      msg: "TurnStarted",
      session_id: "sess-1",
      turn: 4,
      state_path: "root/done",
      attrs: {},
    };

    expect(capturedCallback).not.toBeNull();
    capturedCallback!(newEvent);

    expect(store.events).toHaveLength(2);
    expect(store.events[1]!.turn).toBe(4);
    expect(store.currentStatePath).toBe("root/done");
  });

  it("teardown calls the unsubscribe function", async () => {
    let unsubCalled = false;
    const src: DataSource = {
      listSessions: () => Promise.resolve([SNAPSHOT.session]),
      getSession: () => Promise.resolve(SNAPSHOT.session),
      getApp: () => Promise.resolve(SNAPSHOT.app),
      getMermaid: () => Promise.resolve(SNAPSHOT.mermaid),
      getTrace: () =>
        Promise.resolve({ events: [], last_turn: 0 }),
      subscribe: () => {
        return () => { unsubCalled = true; };
      },
    };

    const store = useRunStore();
    await store.hydrate(src, "sess-1");
    store.teardown();
    expect(unsubCalled).toBe(true);
  });
});
