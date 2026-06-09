/**
 * Unit tests for src/stores/run.ts
 */

import { describe, it, expect, beforeEach } from "vitest";
import { setActivePinia, createPinia } from "pinia";
import { useRunStore } from "../../src/stores/run.js";
import { SnapshotSource } from "../../src/data/snapshot-source.js";
import type { Snapshot, TraceEvent, TurnResult } from "../../src/types.js";
import type { DataSource } from "../../src/data/source.js";

// ---- Write-RPC stub helpers ------------------------------------------------
// The store's read path doesn't touch the write RPCs; these throwing stubs let
// the inline DataSource literals satisfy the full interface without pulling in
// a live transport.
const writeStubs = {
  view: () => Promise.reject(new Error("not stubbed")) as Promise<TurnResult>,
  submit: () =>
    Promise.reject(new Error("not stubbed")) as Promise<TurnResult>,
  sendTurn: () =>
    Promise.reject(new Error("not stubbed")) as Promise<TurnResult>,
  continueTurn: () =>
    Promise.reject(new Error("not stubbed")) as Promise<TurnResult>,
  offpath: () =>
    Promise.reject(new Error("not stubbed")) as Promise<{ answer: string }>,
};

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
      ...writeStubs,
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

describe("useRunStore — setHighlightedStatePaths", () => {
  it("sets highlightedStatePaths and bumps highlightTick", async () => {
    const store = useRunStore();
    await store.hydrate(new SnapshotSource(SNAPSHOT), "sess-1");

    expect(store.highlightedStatePaths).toEqual([]);
    const tick0 = store.highlightTick;

    store.setHighlightedStatePaths(["root/review", "root/done"]);
    expect(store.highlightedStatePaths).toEqual(["root/review", "root/done"]);
    expect(store.highlightTick).toBe(tick0 + 1);
  });

  it("clears highlightedStatePaths when called with empty array", async () => {
    const store = useRunStore();
    await store.hydrate(new SnapshotSource(SNAPSHOT), "sess-1");

    store.setHighlightedStatePaths(["root/review"]);
    store.setHighlightedStatePaths([]);
    expect(store.highlightedStatePaths).toEqual([]);
  });

  it("bumps highlightTick each call (re-clicking same room scrolls again)", async () => {
    const store = useRunStore();
    await store.hydrate(new SnapshotSource(SNAPSHOT), "sess-1");

    const tick0 = store.highlightTick;
    store.setHighlightedStatePaths(["root/review"]);
    store.setHighlightedStatePaths(["root/review"]);
    expect(store.highlightTick).toBe(tick0 + 2);
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
      ...writeStubs,
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
      ...writeStubs,
    };

    const store = useRunStore();
    await store.hydrate(src, "sess-1");
    store.teardown();
    expect(unsubCalled).toBe(true);
  });
});

// ---- currentStatePath bug fix ---------------------------------------------

describe("useRunStore — currentStatePath prefers machine.state_entered", () => {
  function liveSourceCapturing(onCap: (cb: (e: TraceEvent) => void) => void): DataSource {
    return {
      listSessions: () => Promise.resolve([SNAPSHOT.session]),
      getSession: () => Promise.resolve(SNAPSHOT.session),
      getApp: () => Promise.resolve(SNAPSHOT.app),
      getMermaid: () => Promise.resolve(SNAPSHOT.mermaid),
      getTrace: () => Promise.resolve({ events: [], last_turn: 0 }),
      subscribe: (_sid, onEvent) => {
        onCap(onEvent);
        return () => undefined;
      },
      ...writeStubs,
    };
  }

  function ev(msg: string, statePath: string, turn: number): TraceEvent {
    return {
      time: new Date().toISOString(),
      level: "info",
      msg,
      session_id: "sess-1",
      turn,
      state_path: statePath,
      attrs: {},
    };
  }

  it("a later turn.end stamped with the STARTING state does not rewind the landed state", async () => {
    let cb!: (e: TraceEvent) => void;
    const store = useRunStore();
    await store.hydrate(
      liveSourceCapturing((c) => { cb = c; }),
      "sess-1"
    );

    // The turn started in root/review, entered root/done, then turn.end is
    // stamped with the STARTING state (root/review). The landed state must win.
    cb(ev("machine.state_entered", "root/done", 4));
    expect(store.currentStatePath).toBe("root/done");

    cb(ev("turn.end", "root/review", 4));
    expect(store.currentStatePath).toBe("root/done");
  });

  it("falls back to a raw state_path until the first state_entered is seen", async () => {
    let cb!: (e: TraceEvent) => void;
    const store = useRunStore();
    await store.hydrate(
      liveSourceCapturing((c) => { cb = c; }),
      "sess-1"
    );

    // Before any state_entered, a bare event's state_path seeds the UI.
    cb(ev("turn.start", "root/review", 1));
    expect(store.currentStatePath).toBe("root/review");

    // Once state_entered arrives it becomes authoritative.
    cb(ev("machine.state_entered", "root/done", 2));
    expect(store.currentStatePath).toBe("root/done");
  });
});

// ---- write-side actions ----------------------------------------------------

describe("useRunStore — write-side actions", () => {
  function turnResult(over: Partial<TurnResult> = {}): TurnResult {
    return {
      mode: "transitioned",
      state: "idle",
      view: "Welcome to PRD discovery",
      typed_view: { Source: "", Elements: [{ Kind: "heading", Source: "PRD discovery" }] },
      allowed_intents: ["start", "discuss"],
      intents: [
        { name: "start", has_slots: false },
        { name: "discuss", text_slot: "message", has_slots: true },
      ],
      turn_number: 1,
      ...over,
    };
  }

  function writeSource(over: Partial<DataSource> = {}): DataSource {
    return {
      listSessions: () => Promise.resolve([SNAPSHOT.session]),
      getSession: () => Promise.resolve(SNAPSHOT.session),
      getApp: () => Promise.resolve(SNAPSHOT.app),
      getMermaid: () => Promise.resolve(SNAPSHOT.mermaid),
      getTrace: () => Promise.resolve({ events: [], last_turn: 0 }),
      subscribe: () => () => undefined,
      ...writeStubs,
      ...over,
    };
  }

  it("loadInitialView seeds currentView, allowedIntents, currentStatePath, and an opening agent entry", async () => {
    const result = turnResult();
    const src = writeSource({ view: () => Promise.resolve(result) });

    const store = useRunStore();
    await store.loadInitialView(src, "sess-1");

    expect(store.currentView).toEqual(result);
    expect(store.currentStatePath).toBe("idle");
    expect(store.allowedIntents.map((i) => i.name)).toEqual(["start", "discuss"]);
    expect(store.transcript).toHaveLength(1);
    expect(store.transcript[0]!.role).toBe("agent");
    expect(store.transcript[0]!.text).toBe("Welcome to PRD discovery");
    expect(store.transcript[0]!.typedView?.Elements?.[0]!.Kind).toBe("heading");
    expect(store.terminal).toBe(false);
  });

  it("submitIntent pushes a user entry, calls submit, applies the result, and pushes an agent entry", async () => {
    let captured: { intent: string; slots?: Record<string, unknown> } | null = null;
    const next = turnResult({ state: "idle", view: "Discovery in progress", turn_number: 2 });
    const src = writeSource({
      submit: (_sid, intent, slots) => {
        captured = { intent, slots };
        return Promise.resolve(next);
      },
    });

    const store = useRunStore();
    const out = await store.submitIntent(src, "sess-1", "discuss", { message: "I want a CLI for X" });

    expect(captured).toEqual({ intent: "discuss", slots: { message: "I want a CLI for X" } });
    expect(out).toEqual(next);
    expect(store.transcript).toHaveLength(2);
    expect(store.transcript[0]).toMatchObject({ role: "user", text: "I want a CLI for X" });
    expect(store.transcript[1]).toMatchObject({ role: "agent", text: "Discovery in progress" });
    expect(store.currentView).toEqual(next);
    expect(store.currentStatePath).toBe("idle");
  });

  it("submitIntent with a no-slot intent labels the user entry with the intent name", async () => {
    const src = writeSource({ submit: () => Promise.resolve(turnResult({ state: "clarifying" })) });
    const store = useRunStore();
    await store.submitIntent(src, "sess-1", "start", {});
    expect(store.transcript[0]).toMatchObject({ role: "user", text: "start" });
    expect(store.currentStatePath).toBe("clarifying");
  });

  it("submitIntent sets terminal on mode=completed", async () => {
    const src = writeSource({
      submit: () => Promise.resolve(turnResult({ mode: "completed", state: "__exit__done" })),
    });
    const store = useRunStore();
    await store.submitIntent(src, "sess-1", "accept", {});
    expect(store.terminal).toBe(true);
    expect(store.currentStatePath).toBe("__exit__done");
  });

  it("submitIntent renders a rejection's error_message as the agent entry when no view is present", async () => {
    const src = writeSource({
      submit: () =>
        Promise.resolve(
          turnResult({ mode: "rejected", view: undefined, error_message: "guard failed: not ready" })
        ),
    });
    const store = useRunStore();
    await store.submitIntent(src, "sess-1", "confirm", {});
    expect(store.transcript[1]).toMatchObject({ role: "agent", text: "guard failed: not ready" });
  });

  it("submitIntent renders a clarify's slot prompts when no view is present", async () => {
    const src = writeSource({
      submit: () =>
        Promise.resolve(
          turnResult({
            mode: "clarify",
            view: undefined,
            slots_needed: [{ Name: "n", Prompt: "How many?" }],
          })
        ),
    });
    const store = useRunStore();
    await store.submitIntent(src, "sess-1", "answer", {});
    expect(store.transcript[1]).toMatchObject({ role: "agent", text: "How many?" });
  });

  it("sendText pushes the raw text as the user entry and applies the result", async () => {
    let capturedInput = "";
    const src = writeSource({
      sendTurn: (_sid, input) => {
        capturedInput = input;
        return Promise.resolve(turnResult({ view: "Got it." }));
      },
    });
    const store = useRunStore();
    await store.sendText(src, "sess-1", "build me a thing", "discuss");
    expect(capturedInput).toBe("build me a thing");
    expect(store.transcript[0]).toMatchObject({ role: "user", text: "build me a thing" });
    expect(store.transcript[1]).toMatchObject({ role: "agent", text: "Got it." });
  });

  // ---- session-switch isolation (bug: transcripts mixed across sessions) ----
  // When the operator switches from one session's chat to another, the store is
  // a singleton — hydrating the second session must drop the first session's
  // conversational state, or its transcript bubbles bleed into the new session.
  it("hydrate clears the prior session's transcript and view state", async () => {
    const first = turnResult({ state: "idle", view: "Session ONE opening" });
    const srcA = writeSource({ view: () => Promise.resolve(first) });

    const store = useRunStore();
    await store.hydrate(srcA, "sess-1");
    await store.loadInitialView(srcA, "sess-1");
    await store.submitIntent(
      writeSource({ submit: () => Promise.resolve(turnResult({ view: "ONE reply" })) }),
      "sess-1",
      "discuss",
      { message: "hello from one" }
    );
    expect(store.transcript.length).toBeGreaterThan(0);

    // Switch to a second session.
    const second = turnResult({ state: "idle", view: "Session TWO opening" });
    const srcB = writeSource({ view: () => Promise.resolve(second) });
    await store.hydrate(srcB, "sess-2");

    // The first session's bubbles must be gone before the new view seeds.
    expect(store.transcript).toEqual([]);
    expect(store.currentView).toBeNull();
    expect(store.selectedEventIndex).toBeNull();
    expect(store.highlightedStatePaths).toEqual([]);

    await store.loadInitialView(srcB, "sess-2");
    expect(store.transcript).toHaveLength(1);
    expect(store.transcript[0]).toMatchObject({ role: "agent", text: "Session TWO opening" });
    expect(store.transcript.some((e) => e.text.includes("ONE"))).toBe(false);
  });
});
