import { defineStore } from "pinia";
import { ref } from "vue";
import type { AppDef, MermaidSnapshot, TraceEvent, NodeRef } from "../types.js";
import type { DataSource } from "../data/source.js";

export const useRunStore = defineStore("run", () => {
  // ---- state ----
  const appDef = ref<AppDef | null>(null);
  const mermaid = ref<MermaidSnapshot | null>(null);
  const events = ref<TraceEvent[]>([]);
  const currentStatePath = ref<string>("");
  const selectedNode = ref<NodeRef | null>(null);
  const selectedEventIndex = ref<number | null>(null);
  const terminal = ref<boolean>(false);
  const loading = ref<boolean>(false);

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

  /**
   * Look up nodeId in mermaid.node_map and set selectedNode.
   * Sets selectedNode to null if nodeId is not found.
   */
  function selectNode(nodeId: string): void {
    const map = mermaid.value?.node_map;
    if (map === undefined) {
      selectedNode.value = null;
      return;
    }
    const ref = map[nodeId];
    selectedNode.value = ref ?? null;
  }

  /** Set the selected event by index. */
  function selectEvent(index: number): void {
    selectedEventIndex.value = index;
  }

  return {
    // state
    appDef,
    mermaid,
    events,
    currentStatePath,
    selectedNode,
    selectedEventIndex,
    terminal,
    loading,
    // actions
    hydrate,
    teardown,
    selectNode,
    selectEvent,
  };
});
