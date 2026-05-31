import { defineStore } from "pinia";
import { computed, ref } from "vue";
import type { AppDef, MermaidSnapshot, TraceEvent } from "../types.js";
import type { DataSource } from "../data/source.js";
import { readOracleUsage } from "../components/oracle/lib.js";

export const useRunStore = defineStore("run", () => {
  // ---- state ----
  const appDef = ref<AppDef | null>(null);
  const mermaid = ref<MermaidSnapshot | null>(null);
  const events = ref<TraceEvent[]>([]);
  const currentStatePath = ref<string>("");
  const selectedEventIndex = ref<number | null>(null);
  const terminal = ref<boolean>(false);
  const loading = ref<boolean>(false);
  // Set of state_path values that should be highlighted in the timeline.
  // Driven by clicks on diagram rooms/phases.  Empty = no highlight.
  const highlightedStatePaths = ref<string[]>([]);
  // Bumped each time the highlight set changes; TraceTimeline watches it to
  // scroll the first matching row into view (so re-clicking the same room
  // scrolls again).
  const highlightTick = ref<number>(0);

  // Aggregate token usage + cost across every oracle.call.complete event in the
  // run. Reads the canonical transport meta via readOracleUsage. `present` is
  // false when no call carried any usage (so the UI can hide the chip).
  const usageTotals = computed(() => {
    let promptTokens = 0;
    let responseTokens = 0;
    let costUsd = 0;
    let calls = 0;
    let present = false;
    for (const e of events.value) {
      if (e.msg !== "oracle.call.complete") continue;
      const u = readOracleUsage(e.attrs);
      if (u.promptTokens || u.responseTokens || u.costUsd) present = true;
      promptTokens += u.promptTokens ?? 0;
      responseTokens += u.responseTokens ?? 0;
      costUsd += u.costUsd ?? 0;
      calls += 1;
    }
    return { promptTokens, responseTokens, costUsd, calls, present };
  });

  // ---- internal ----
  let _unsubscribe: (() => void) | null = null;

  // ---- actions ----

  /**
   * Hydrate from a DataSource: load session + app + mermaid + initial trace,
   * then subscribe to keep events/currentStatePath updated.
   */
  async function hydrate(source: DataSource, sessionId: string): Promise<void> {
    loading.value = true;
    try {
      const [session, app, mer, traceResult] = await Promise.all([
        source.getSession(sessionId),
        source.getApp(sessionId),
        source.getMermaid(sessionId),
        source.getTrace(sessionId),
      ]);

      appDef.value = app;
      mermaid.value = mer;
      currentStatePath.value = session.current_state;
      terminal.value = session.terminal;
      events.value = traceResult.events.slice();
    } finally {
      loading.value = false;
    }

    // Subscribe for live updates.
    _unsubscribe = source.subscribe(sessionId, (e: TraceEvent) => {
      events.value.push(e);
      if (e.state_path) {
        currentStatePath.value = e.state_path;
      }
    });
  }

  /** Stop the live subscription. */
  function teardown(): void {
    _unsubscribe?.();
    _unsubscribe = null;
  }

  /** Set the selected event by index (drives inline row highlight). */
  function selectEvent(index: number): void {
    selectedEventIndex.value = index;
  }

  /** Clear the selected event. */
  function clearSelection(): void {
    selectedEventIndex.value = null;
  }

  /** Set the highlighted state paths (driven by diagram clicks). */
  function setHighlightedStatePaths(paths: string[]): void {
    highlightedStatePaths.value = paths.slice();
    highlightTick.value += 1;
  }

  return {
    // state
    appDef,
    mermaid,
    events,
    currentStatePath,
    selectedEventIndex,
    terminal,
    loading,
    highlightedStatePaths,
    highlightTick,
    usageTotals,
    // actions
    hydrate,
    teardown,
    selectEvent,
    clearSelection,
    setHighlightedStatePaths,
  };
});
