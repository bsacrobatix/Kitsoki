<template>
  <div class="iv">
    <div v-if="store.loading" class="iv__loading">Loading session…</div>
    <template v-else>
      <!-- Top bar -->
      <header class="iv__topbar">
        <router-link to="/" class="iv__back">← Sessions</router-link>
        <span class="iv__app-id">{{ appId }}</span>
        <span class="iv__sep">·</span>
        <code class="iv__current-state" data-testid="current-state">{{ store.currentStatePath || "—" }}</code>
        <span
          class="iv__state-badge"
          data-testid="state-badge"
          :data-terminal="store.terminal ? 'true' : 'false'"
          :class="store.terminal ? 'iv__state-badge--done' : 'iv__state-badge--live'"
        >
          {{ store.terminal ? 'done' : 'live' }}
        </span>
        <span
          v-if="store.usageTotals.present"
          class="iv__usage"
          :title="`${store.usageTotals.calls} oracle calls · in ${fmtTokens(store.usageTotals.promptTokens)} / out ${fmtTokens(store.usageTotals.responseTokens)} tokens`"
        >
          Σ {{ fmtTokens(store.usageTotals.promptTokens + store.usageTotals.responseTokens) }} tok<template v-if="fmtCost(store.usageTotals.costUsd)"> · {{ fmtCost(store.usageTotals.costUsd) }}</template>
        </span>
        <router-link :to="`/s/${sessionId}`" class="iv__observe-link" data-testid="observe-link">Observe ↗</router-link>
      </header>

      <!-- Main row: chat (left) | trace (right) -->
      <div class="iv__main">
        <!-- LEFT: conversation -->
        <section class="iv__chat" aria-label="Conversation" data-testid="chat-section">
          <ChatTranscript class="iv__transcript" :transcript="store.transcript" />
          <div v-if="store.terminal" class="iv__done-note">
            Session complete — no further input accepted.
          </div>
          <InputBar
            v-else
            :intents="store.currentView?.intents ?? []"
            :typed-view="store.currentView?.typed_view"
            :pending="pending"
            @send="onSend"
            @intent="onIntent"
          />
          <div v-if="error" class="iv__error">{{ error }}</div>
        </section>

        <!-- RIGHT: live trace (diagram over timeline) -->
        <section class="iv__trace" aria-label="Trace">
          <div class="iv__panel iv__panel--diagram" data-testid="trace-diagram">
            <div class="iv__panel-header">State Diagram</div>
            <StateDiagram
              v-if="store.mermaid"
              :mermaid-source="store.mermaid.source"
              :node-map="store.mermaid.node_map"
              :current-state-path="store.currentStatePath"
              :highlighted-state-paths="store.highlightedStatePaths"
              :events="store.events"
              :selected-event-index="store.selectedEventIndex"
              @select="onNodeSelect"
              @select-phase="onPhaseSelect"
              @select-event="onEventSelect"
            />
            <div v-else class="iv__empty">No diagram.</div>
          </div>
          <div class="iv__panel iv__panel--timeline" data-testid="trace-timeline">
            <div class="iv__panel-header">
              <span>Trace</span>
              <button
                v-if="store.highlightedStatePaths.length > 0"
                class="iv__clear-highlight"
                title="Clear diagram highlight"
                @click="onClearHighlight"
              >clear highlight ({{ store.highlightedStatePaths.length }})</button>
            </div>
            <TraceTimeline
              :events="store.events"
              :selected-event-index="store.selectedEventIndex"
              :highlighted-state-paths="store.highlightedStatePaths"
              :highlight-tick="store.highlightTick"
              :mermaid-source="store.mermaid?.source ?? null"
              @select="onEventSelect"
            />
          </div>
        </section>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import { useRunStore } from "../stores/run.js";
import { createDataSource } from "../data/source.js";
import type { DataSource } from "../data/source.js";
import ChatTranscript from "../components/ChatTranscript.vue";
import InputBar from "../components/InputBar.vue";
import StateDiagram from "../components/StateDiagram.vue";
import TraceTimeline from "../components/TraceTimeline.vue";
import { fmtTokens, fmtCost } from "../components/oracle/lib.js";
import type { NodeRef } from "../types.js";

const props = defineProps<{ sessionId: string }>();
const store = useRunStore();

// One DataSource for the lifetime of the view (subscribe + write RPCs).
let source: DataSource | null = null;

// True while a turn is in flight; disables the input so the operator can't
// fire a second overlapping turn against the live session.
const pending = ref(false);
const error = ref<string | null>(null);

const appId = computed(() => store.appDef?.id ?? store.appDef?.name ?? "kitsoki");

onMounted(async () => {
  source = createDataSource();
  // hydrate loads session/app/mermaid/trace and opens the live subscription.
  await store.hydrate(source, props.sessionId);
  // loadInitialView seeds currentView + opening agent transcript entry.
  await store.loadInitialView(source, props.sessionId);
});

onUnmounted(() => {
  store.teardown();
});

/**
 * Run a write action with the pending guard. The store actions push the
 * user/agent transcript entries and apply the result; we only manage the
 * in-flight flag and surface transport-level errors here. Guard rejections /
 * clarifications ride back inside the TurnResult and are rendered as agent
 * transcript entries, so they are NOT errors.
 */
async function runTurn(fn: () => Promise<unknown>): Promise<void> {
  if (pending.value || !source || store.terminal) return;
  pending.value = true;
  error.value = null;
  try {
    await fn();
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    pending.value = false;
  }
}

/**
 * Composer + action submit. InputBar emits BOTH a high-level `send` (raw text +
 * bound intent name, for text composers) and a structured `intent` (name +
 * slots: empty for action buttons, the bound slot for text composers).
 *
 * We drive every turn through the STRUCTURED intent (submitIntent) — the
 * operator picked a concrete intent from the room's menu and the slot value is
 * already bound, so there is nothing for an interpreter to classify. This keeps
 * the interactive UI working in the deterministic (no-harness) posture
 * (`kitsoki web --flow`), where the free-text route has no interpreter, while
 * remaining correct for the live posture (an explicit intent submit is always
 * unambiguous). `onSend` is therefore a no-op label hook; `onIntent` does the
 * work for both action buttons (empty slots) and text composers (bound slot).
 */
function onSend(_text: string, _intentName: string): void {
  // Handled by the paired @intent emit (submitIntent) — see onIntent.
}

function onIntent(name: string, slots: Record<string, unknown>): void {
  if (!source) return;
  void runTurn(() => store.submitIntent(source!, props.sessionId, name, slots));
}

// ---- trace interactions (mirror RunView observer behavior) ----
function onNodeSelect(_nodeId: string, nodeRef: NodeRef): void {
  if (nodeRef.kind === "state") {
    store.setHighlightedStatePaths([nodeRef.ref]);
  }
}
function onPhaseSelect(_phaseId: string, roomRefs: string[]): void {
  store.setHighlightedStatePaths(roomRefs);
}
function onClearHighlight(): void {
  store.setHighlightedStatePaths([]);
}
function onEventSelect(index: number): void {
  store.selectEvent(index);
}
</script>

<style scoped>
.iv {
  display: flex;
  flex-direction: column;
  height: 100vh;
  background: #0a1120;
  color: #e2e8f0;
  overflow: hidden;
}

.iv__loading {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  color: #64748b;
  font-size: 1rem;
}

/* ---- Top bar ---- */
.iv__topbar {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.55rem 1rem;
  background: #0f172a;
  border-bottom: 1px solid #1e293b;
  flex-shrink: 0;
  font-size: 0.8125rem;
}

.iv__back {
  color: #60a5fa;
  text-decoration: none;
}
.iv__back:hover {
  text-decoration: underline;
}

.iv__app-id {
  font-weight: 600;
  color: #e2e8f0;
}

.iv__sep {
  color: #334155;
}

.iv__current-state {
  font-family: ui-monospace, monospace;
  font-size: 0.775rem;
  color: #7dd3fc;
}

.iv__state-badge {
  display: inline-block;
  padding: 0.1rem 0.45rem;
  border-radius: 999px;
  font-size: 0.7rem;
  font-weight: 600;
}
.iv__state-badge--live {
  background: #14532d;
  color: #86efac;
}
.iv__state-badge--done {
  background: #1e293b;
  color: #64748b;
}

.iv__usage {
  font-family: ui-monospace, monospace;
  font-size: 0.75rem;
  color: #a3e635;
  background: #1a2e05;
  border: 1px solid #3f6212;
  border-radius: 4px;
  padding: 0.1rem 0.45rem;
  white-space: nowrap;
}

.iv__observe-link {
  margin-left: auto;
  color: #94a3b8;
  text-decoration: none;
  font-size: 0.75rem;
}
.iv__observe-link:hover {
  color: #cbd5e1;
  text-decoration: underline;
}

/* ---- Main row ---- */
.iv__main {
  display: flex;
  flex: 1;
  min-height: 0;
  gap: 0;
}

/* LEFT: chat column */
.iv__chat {
  display: flex;
  flex-direction: column;
  flex: 1 1 46%;
  min-width: 0;
  min-height: 0;
  border-right: 1px solid #1e293b;
  background: #0f1115;
}

.iv__transcript {
  flex: 1 1 auto;
  min-height: 0;
}

.iv__done-note {
  padding: 0.6rem 1.1rem;
  font-size: 0.8rem;
  color: #64748b;
  background: #14171d;
  border-top: 1px solid #2a2f3a;
  text-align: center;
}

.iv__error {
  padding: 0.5rem 1.1rem;
  font-size: 0.78rem;
  color: #fca5a5;
  background: #2a1518;
  border-top: 1px solid #7f1d1d;
}

/* RIGHT: trace column */
.iv__trace {
  display: flex;
  flex-direction: column;
  flex: 1 1 54%;
  min-width: 0;
  min-height: 0;
  padding: 0.5rem;
  gap: 0.5rem;
}

.iv__panel {
  display: flex;
  flex-direction: column;
  overflow: hidden;
  border-radius: 6px;
  min-height: 0;
}

.iv__panel--diagram {
  flex: 1 1 45%;
}

.iv__panel--timeline {
  flex: 1 1 55%;
}

.iv__panel-header {
  font-size: 0.75rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: #64748b;
  padding: 0.25rem 0;
  flex-shrink: 0;
  display: flex;
  align-items: center;
  gap: 0.5rem;
}

.iv__clear-highlight {
  background: #3a2d0e;
  border: 1px solid #fbbf24;
  color: #fde68a;
  font-size: 0.65rem;
  text-transform: none;
  letter-spacing: normal;
  padding: 0.1rem 0.4rem;
  border-radius: 999px;
  cursor: pointer;
  font-family: inherit;
}
.iv__clear-highlight:hover {
  background: #4a3a14;
}

.iv__panel--diagram :deep(.state-diagram) {
  flex: 1;
  height: 100%;
}

.iv__panel--timeline :deep(.trace-timeline) {
  flex: 1;
  height: 100%;
  min-height: 0;
}

.iv__empty {
  color: #475569;
  font-size: 0.875rem;
  padding: 1rem;
}
</style>
