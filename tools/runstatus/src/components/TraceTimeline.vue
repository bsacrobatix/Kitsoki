<template>
  <div class="trace-timeline">
    <!-- Filter bar -->
    <div class="trace-timeline__filters">
      <!-- Subsystem chips -->
      <div class="trace-timeline__filter-group">
        <span class="trace-timeline__filter-label">Subsystem:</span>
        <button
          v-for="sys in ALL_SUBSYSTEMS"
          :key="sys"
          class="trace-timeline__chip"
          :class="{ active: selectedSubsystems.has(sys) }"
          @click="toggleSubsystem(sys)"
        >{{ sys }}</button>
      </div>

      <!-- Level chips -->
      <div class="trace-timeline__filter-group">
        <span class="trace-timeline__filter-label">Level:</span>
        <button
          v-for="lvl in availableLevels"
          :key="lvl"
          class="trace-timeline__chip"
          :class="{ active: selectedLevels.has(lvl) }"
          @click="toggleLevel(lvl)"
        >{{ lvl }}</button>
      </div>

      <!-- State path single-select -->
      <div class="trace-timeline__filter-group">
        <span class="trace-timeline__filter-label">State:</span>
        <select
          class="trace-timeline__select"
          :value="selectedStatePath ?? ''"
          @change="onStatePathChange"
        >
          <option value="">All</option>
          <option v-for="sp in availableStatePaths" :key="sp" :value="sp">{{ sp }}</option>
        </select>
      </div>

      <!-- Clear -->
      <button
        v-if="hasActiveFilters"
        class="trace-timeline__chip trace-timeline__chip--clear"
        @click="clearFilters"
      >Clear</button>
    </div>

    <!-- Timeline body -->
    <div
      class="trace-timeline__body"
      ref="bodyRef"
      @scroll="onScroll"
    >
      <!-- Virtual spacer (top) -->
      <div v-if="useVirtualisation" :style="{ height: topSpacerHeight + 'px' }" />

      <template v-for="group in visibleGroups" :key="group.turn">
        <!-- Turn header -->
        <div
          class="trace-timeline__turn-header"
          @click="toggleTurnCollapse(group.turn)"
        >
          <span class="trace-timeline__turn-caret">{{ collapsedTurns.has(group.turn) ? '▶' : '▼' }}</span>
          <span class="trace-timeline__turn-label">Turn {{ group.turn }}</span>
          <span class="trace-timeline__turn-count">{{ group.events.length }} event{{ group.events.length !== 1 ? 's' : '' }}</span>
        </div>

        <!-- Rows within turn (hidden if collapsed) -->
        <template v-if="!collapsedTurns.has(group.turn)">
          <div
            v-for="row in group.events"
            :key="row.index"
            class="trace-timeline__row"
            :class="{ selected: row.index === selectedEventIndex, expanded: expandedRows.has(row.index) }"
            @click="onRowClick(row.index)"
          >
            <div class="trace-timeline__row-main">
              <span class="trace-timeline__subsystem-chip" :data-subsystem="row.subsystem">{{ row.subsystem }}</span>
              <span class="trace-timeline__msg">{{ row.event.msg }}</span>
              <span class="trace-timeline__level" :data-level="row.event.level">{{ row.event.level }}</span>
              <span class="trace-timeline__time">{{ formatTime(row.event.time) }}</span>
              <button
                class="trace-timeline__expand-btn"
                @click.stop="toggleRowExpand(row.index)"
                :title="expandedRows.has(row.index) ? 'Collapse' : 'Expand'"
              >{{ expandedRows.has(row.index) ? '−' : '+' }}</button>
            </div>

            <!-- Expanded body -->
            <div v-if="expandedRows.has(row.index)" class="trace-timeline__row-body" @click.stop>
              <div class="trace-timeline__attrs-header">
                <span>Attrs</span>
                <button class="trace-timeline__copy-btn" @click="copyAttrs(row.event)">Copy</button>
              </div>
              <pre class="trace-timeline__attrs-pre">{{ JSON.stringify(row.event.attrs, null, 2) }}</pre>
            </div>
          </div>
        </template>
      </template>

      <!-- Virtual spacer (bottom) -->
      <div v-if="useVirtualisation" :style="{ height: bottomSpacerHeight + 'px' }" />
    </div>

    <!-- Empty state -->
    <div v-if="filteredEvents.length === 0" class="trace-timeline__empty">
      No events{{ hasActiveFilters ? ' match the current filters' : '' }}.
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, reactive } from "vue";
import type { TraceEvent } from "../types.js";

// ---- props & emits ----------------------------------------------------------

const props = defineProps<{
  events: TraceEvent[];
  selectedEventIndex: number | null;
}>();

const emit = defineEmits<{
  (e: "select", index: number): void;
}>();

// ---- constants --------------------------------------------------------------

const ALL_SUBSYSTEMS = ["turn", "harness", "machine", "host", "oracle", "other"] as const;
type Subsystem = (typeof ALL_SUBSYSTEMS)[number];

const VIRTUALISATION_THRESHOLD = 200;
const ROW_HEIGHT_ESTIMATE = 36; // px — used for windowing math
const WINDOW_OVERSCAN = 10; // extra rows above/below visible window

// ---- filter state -----------------------------------------------------------

const selectedSubsystems = reactive(new Set<Subsystem>(ALL_SUBSYSTEMS));
const selectedLevels = reactive(new Set<string>());
const selectedStatePath = ref<string | null>(null);
const collapsedTurns = reactive(new Set<number>());
const expandedRows = reactive(new Set<number>());

// ---- virtualisation state ---------------------------------------------------

const bodyRef = ref<HTMLElement | null>(null);
const scrollTop = ref(0);
const clientHeight = ref(600); // sensible default; updated on scroll

// ---- helpers ----------------------------------------------------------------

function subsystemFromMsg(msg: string): Subsystem {
  const prefix = msg.split(".")[0] ?? "";
  switch (prefix) {
    case "turn":    return "turn";
    case "harness": return "harness";
    case "machine": return "machine";
    case "host":    return "host";
    case "oracle":  return "oracle";
    default:        return "other";
  }
}

function formatTime(iso: string): string {
  try {
    const d = new Date(iso);
    return d.toISOString().replace("T", " ").replace("Z", "").slice(11); // HH:MM:SS.mmm
  } catch {
    return iso;
  }
}

// ---- derived ----------------------------------------------------------------

// All available levels in the event stream.
const availableLevels = computed(() => {
  const s = new Set<string>();
  for (const e of props.events) s.add(e.level);
  return [...s].sort();
});

// All available state paths.
const availableStatePaths = computed(() => {
  const s = new Set<string>();
  for (const e of props.events) if (e.state_path) s.add(e.state_path);
  return [...s].sort();
});

const hasActiveFilters = computed(() => {
  const allSysSelected = ALL_SUBSYSTEMS.every((s) => selectedSubsystems.has(s));
  const noLevelFilter = selectedLevels.size === 0;
  const noStateFilter = selectedStatePath.value === null;
  return !allSysSelected || !noLevelFilter || !noStateFilter;
});

// Filtered + annotated events (preserving original index).
interface AnnotatedEvent {
  index: number;
  event: TraceEvent;
  subsystem: Subsystem;
}

const filteredEvents = computed<AnnotatedEvent[]>(() => {
  return props.events
    .map((e, i) => ({ index: i, event: e, subsystem: subsystemFromMsg(e.msg) }))
    .filter(({ event, subsystem }) => {
      if (!selectedSubsystems.has(subsystem)) return false;
      if (selectedLevels.size > 0 && !selectedLevels.has(event.level)) return false;
      if (selectedStatePath.value !== null && event.state_path !== selectedStatePath.value) return false;
      return true;
    });
});

// Group by turn, descending.
interface TurnGroup {
  turn: number;
  events: AnnotatedEvent[];
}

const groupedTurns = computed<TurnGroup[]>(() => {
  const map = new Map<number, AnnotatedEvent[]>();
  for (const ae of filteredEvents.value) {
    const arr = map.get(ae.event.turn) ?? [];
    arr.push(ae);
    map.set(ae.event.turn, arr);
  }
  return [...map.entries()]
    .sort(([a], [b]) => b - a) // descending turn
    .map(([turn, events]) => ({ turn, events }));
});

// ---- virtualisation ---------------------------------------------------------

const useVirtualisation = computed(() => filteredEvents.value.length > VIRTUALISATION_THRESHOLD);

/**
 * For virtualisation we need to know how many "rows" each group takes:
 * 1 (header) + N (events, if not collapsed).
 */
interface FlatItem {
  type: "header" | "row";
  turn: number;
  groupIndex: number;
  rowIndex?: number; // within the group
  annotatedEvent?: AnnotatedEvent;
}

const flatItems = computed<FlatItem[]>(() => {
  const items: FlatItem[] = [];
  for (let gi = 0; gi < groupedTurns.value.length; gi++) {
    const g = groupedTurns.value[gi]!;
    items.push({ type: "header", turn: g.turn, groupIndex: gi });
    if (!collapsedTurns.has(g.turn)) {
      for (let ri = 0; ri < g.events.length; ri++) {
        items.push({ type: "row", turn: g.turn, groupIndex: gi, rowIndex: ri, annotatedEvent: g.events[ri] });
      }
    }
  }
  return items;
});

const totalHeight = computed(() => flatItems.value.length * ROW_HEIGHT_ESTIMATE);

const visibleStart = computed(() => {
  if (!useVirtualisation.value) return 0;
  return Math.max(0, Math.floor(scrollTop.value / ROW_HEIGHT_ESTIMATE) - WINDOW_OVERSCAN);
});

const visibleEnd = computed(() => {
  if (!useVirtualisation.value) return flatItems.value.length;
  const end = Math.ceil((scrollTop.value + clientHeight.value) / ROW_HEIGHT_ESTIMATE) + WINDOW_OVERSCAN;
  return Math.min(flatItems.value.length, end);
});

const topSpacerHeight = computed(() => visibleStart.value * ROW_HEIGHT_ESTIMATE);
const bottomSpacerHeight = computed(() => (flatItems.value.length - visibleEnd.value) * ROW_HEIGHT_ESTIMATE);

// Re-collapse flat items back into groups for the template, preserving only the visible window.
const visibleGroups = computed<TurnGroup[]>(() => {
  if (!useVirtualisation.value) return groupedTurns.value;

  const slice = flatItems.value.slice(visibleStart.value, visibleEnd.value);
  const map = new Map<number, AnnotatedEvent[]>();
  const order: number[] = [];

  for (const item of slice) {
    if (item.type === "header") {
      if (!map.has(item.turn)) {
        map.set(item.turn, []);
        order.push(item.turn);
      }
    } else if (item.annotatedEvent) {
      const arr = map.get(item.turn) ?? [];
      arr.push(item.annotatedEvent);
      map.set(item.turn, arr);
    }
  }

  return order.map((turn) => ({ turn, events: map.get(turn) ?? [] }));
});

// ---- event handlers ---------------------------------------------------------

function toggleSubsystem(sys: Subsystem): void {
  if (selectedSubsystems.has(sys)) {
    selectedSubsystems.delete(sys);
  } else {
    selectedSubsystems.add(sys);
  }
}

function toggleLevel(lvl: string): void {
  if (selectedLevels.has(lvl)) {
    selectedLevels.delete(lvl);
  } else {
    selectedLevels.add(lvl);
  }
}

function onStatePathChange(e: Event): void {
  const val = (e.target as HTMLSelectElement).value;
  selectedStatePath.value = val === "" ? null : val;
}

function clearFilters(): void {
  ALL_SUBSYSTEMS.forEach((s) => selectedSubsystems.add(s));
  selectedLevels.clear();
  selectedStatePath.value = null;
}

function toggleTurnCollapse(turn: number): void {
  if (collapsedTurns.has(turn)) {
    collapsedTurns.delete(turn);
  } else {
    collapsedTurns.add(turn);
  }
}

function onRowClick(index: number): void {
  emit("select", index);
}

function toggleRowExpand(index: number): void {
  if (expandedRows.has(index)) {
    expandedRows.delete(index);
  } else {
    expandedRows.add(index);
  }
}

function onScroll(e: Event): void {
  const el = e.target as HTMLElement;
  scrollTop.value = el.scrollTop;
  clientHeight.value = el.clientHeight;
}

async function copyAttrs(event: TraceEvent): Promise<void> {
  try {
    await navigator.clipboard.writeText(JSON.stringify(event.attrs, null, 2));
  } catch {
    // Clipboard API may be unavailable in some environments; silently ignore.
  }
}
</script>

<style scoped>
.trace-timeline {
  display: flex;
  flex-direction: column;
  height: 100%;
  background: #0f172a;
  border: 1px solid #1e293b;
  border-radius: 6px;
  overflow: hidden;
  font-size: 0.8125rem;
}

/* --- Filters --- */
.trace-timeline__filters {
  display: flex;
  flex-wrap: wrap;
  gap: 0.375rem 0.5rem;
  padding: 0.5rem;
  border-bottom: 1px solid #1e293b;
  background: #0f172a;
}

.trace-timeline__filter-group {
  display: flex;
  align-items: center;
  gap: 0.25rem;
  flex-wrap: wrap;
}

.trace-timeline__filter-label {
  color: #64748b;
  font-size: 0.75rem;
  white-space: nowrap;
}

.trace-timeline__chip {
  padding: 0.1rem 0.4rem;
  border: 1px solid #334155;
  border-radius: 999px;
  background: #1e293b;
  color: #94a3b8;
  cursor: pointer;
  font-size: 0.75rem;
  transition: background 0.1s, color 0.1s, border-color 0.1s;
}

.trace-timeline__chip.active {
  background: #1d4ed8;
  border-color: #3b82f6;
  color: #eff6ff;
}

.trace-timeline__chip--clear {
  background: #7f1d1d;
  border-color: #ef4444;
  color: #fee2e2;
}

.trace-timeline__select {
  background: #1e293b;
  border: 1px solid #334155;
  color: #e2e8f0;
  font-size: 0.75rem;
  padding: 0.1rem 0.3rem;
  border-radius: 4px;
  max-width: 140px;
}

/* --- Body --- */
.trace-timeline__body {
  flex: 1;
  overflow-y: auto;
  overflow-x: hidden;
}

/* --- Turn header --- */
.trace-timeline__turn-header {
  display: flex;
  align-items: center;
  gap: 0.4rem;
  padding: 0.3rem 0.6rem;
  background: #1e293b;
  border-bottom: 1px solid #0f172a;
  cursor: pointer;
  user-select: none;
  position: sticky;
  top: 0;
  z-index: 1;
}

.trace-timeline__turn-header:hover {
  background: #293548;
}

.trace-timeline__turn-caret {
  color: #64748b;
  font-size: 0.7rem;
}

.trace-timeline__turn-label {
  font-weight: 600;
  color: #94a3b8;
}

.trace-timeline__turn-count {
  margin-left: auto;
  color: #475569;
  font-size: 0.7rem;
}

/* --- Row --- */
.trace-timeline__row {
  border-bottom: 1px solid #1a2337;
  cursor: pointer;
}

.trace-timeline__row:hover .trace-timeline__row-main {
  background: #162032;
}

.trace-timeline__row.selected .trace-timeline__row-main {
  background: #1e3a5f;
  border-left: 2px solid #60a5fa;
}

.trace-timeline__row-main {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.25rem 0.6rem;
  transition: background 0.1s;
}

/* Subsystem chip */
.trace-timeline__subsystem-chip {
  display: inline-block;
  min-width: 4.5rem;
  text-align: center;
  padding: 0.05rem 0.3rem;
  border-radius: 3px;
  font-size: 0.7rem;
  font-weight: 600;
  background: #1e293b;
  color: #94a3b8;
}

.trace-timeline__subsystem-chip[data-subsystem="turn"]    { background: #1e3a5f; color: #93c5fd; }
.trace-timeline__subsystem-chip[data-subsystem="machine"] { background: #14532d; color: #86efac; }
.trace-timeline__subsystem-chip[data-subsystem="host"]    { background: #4a1d96; color: #c4b5fd; }
.trace-timeline__subsystem-chip[data-subsystem="oracle"]  { background: #7c2d12; color: #fdba74; }
.trace-timeline__subsystem-chip[data-subsystem="harness"] { background: #1e3a5f; color: #7dd3fc; }

.trace-timeline__msg {
  flex: 1;
  color: #e2e8f0;
  font-family: ui-monospace, monospace;
  font-size: 0.775rem;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.trace-timeline__level {
  color: #64748b;
  font-size: 0.7rem;
  min-width: 2.5rem;
  text-align: right;
}
.trace-timeline__level[data-level="warn"]  { color: #fbbf24; }
.trace-timeline__level[data-level="error"] { color: #f87171; }
.trace-timeline__level[data-level="debug"] { color: #475569; }

.trace-timeline__time {
  color: #475569;
  font-size: 0.7rem;
  font-family: ui-monospace, monospace;
  white-space: nowrap;
}

.trace-timeline__expand-btn {
  background: none;
  border: 1px solid #334155;
  color: #64748b;
  cursor: pointer;
  padding: 0.05rem 0.3rem;
  border-radius: 3px;
  font-size: 0.75rem;
  line-height: 1;
}

.trace-timeline__expand-btn:hover {
  background: #1e293b;
  color: #e2e8f0;
}

/* --- Expanded row body --- */
.trace-timeline__row-body {
  padding: 0.4rem 0.6rem;
  background: #080f1a;
  border-top: 1px solid #1e293b;
}

.trace-timeline__attrs-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 0.3rem;
  color: #64748b;
  font-size: 0.75rem;
}

.trace-timeline__copy-btn {
  background: #1e293b;
  border: 1px solid #334155;
  color: #94a3b8;
  cursor: pointer;
  padding: 0.1rem 0.4rem;
  border-radius: 3px;
  font-size: 0.7rem;
}

.trace-timeline__copy-btn:hover {
  background: #334155;
  color: #e2e8f0;
}

.trace-timeline__attrs-pre {
  color: #7dd3fc;
  font-family: ui-monospace, monospace;
  font-size: 0.75rem;
  white-space: pre-wrap;
  word-break: break-word;
  margin: 0;
}

/* --- Empty state --- */
.trace-timeline__empty {
  padding: 1rem;
  color: #475569;
  font-size: 0.875rem;
  text-align: center;
}
</style>
