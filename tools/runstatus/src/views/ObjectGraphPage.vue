<template>
  <div class="objectgraph-page" data-testid="objectgraph-page">
    <header class="objectgraph-page__bar">
      <span class="objectgraph-page__title">Project object graph</span>
      <span
        v-if="graph"
        class="objectgraph-page__count"
        data-testid="objectgraph-count"
      >{{ graph.nodes.length }} nodes / {{ graph.edges.length }} edges</span>
      <button
        type="button"
        class="objectgraph-page__full-graph-link"
        data-testid="objectgraph-open-full-graph"
        @click="fullGraphOpen = true"
      >View full graph ⤢</button>
    </header>

    <div v-if="loading" class="objectgraph-page__loading" data-testid="objectgraph-loading">
      Loading…
    </div>
    <div v-else-if="error" class="objectgraph-page__error" data-testid="objectgraph-error">
      {{ error }}
    </div>
    <CatalogPanel
      v-else-if="graph"
      :graph="graph"
      :selected-id="selectedId"
      data-testid="objectgraph-catalog"
      @update:selected-id="selectedId = $event"
    />

    <div
      v-if="fullGraphOpen && graph"
      class="objectgraph-page__modal"
      data-testid="objectgraph-graph-modal"
      @keydown.esc="fullGraphOpen = false"
    >
      <div class="objectgraph-page__modal-bar">
        <span>Full project object graph</span>
        <div class="objectgraph-page__modal-actions">
          <label v-if="catalogHasAreas" class="objectgraph-page__group-toggle">
            Group by
            <select v-model="groupMode" data-testid="objectgraph-group-mode">
              <option value="type">Type layers</option>
              <option value="area">Areas</option>
            </select>
          </label>
          <button type="button" data-testid="objectgraph-close-full-graph" @click="fullGraphOpen = false">Close</button>
        </div>
      </div>
      <GraphView
        :graph="graph"
        :focus-id="selectedId"
        :group-by-layer="groupByLayerFn"
        :group-label="groupLabelFn"
        class="objectgraph-page__modal-view"
        @update:focus-id="selectedId = $event"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * ObjectGraphPage — the project object graph catalog (internal/graph,
 * W1.0/W1.1) as one integrated view in kitsoki web: CatalogPanel is the
 * primary surface (layer map, object picker, focus detail), and every
 * object's "points to" / "points here" relationships render inline as a
 * Cytoscape neighborhood graph (see CatalogPanel's relationship-graph
 * section) rather than behind a separate mode switch. A full-graph overlay
 * (GraphView.vue over the whole catalog) is available but deliberately
 * de-emphasized — a small link, not a primary toggle.
 *
 * Catalog selected via `?catalog=<path>` (default: the seed review
 * fixture), loaded through runstatus.objectgraph.load — the same
 * kitsoki.graph/v1 wire shape story room graphs use.
 */
import { computed, onMounted, ref, watch } from "vue";
import { useRoute } from "vue-router";
import { LiveSource } from "../data/live-source.js";
import type { ObjectGraph } from "../data/objectgraph.js";
import CatalogPanel from "../components/objectgraph/CatalogPanel.vue";
import GraphView from "../components/objectgraph/GraphView.vue";
import {
  areaGroupLabel,
  buildAreaGroupResolver,
  hasAreaNodes,
  nodeLayerId,
} from "../components/objectgraph/catalog-model.js";

const route = useRoute();
const source = new LiveSource("/");

const graph = ref<ObjectGraph | null>(null);
const loading = ref(false);
const error = ref("");
const selectedId = ref("");
const fullGraphOpen = ref(false);

// Full-graph "group by" mode: the default 'type' mode is the existing
// hardcoded presentation layers (nodeLayerId); 'area' is the data-driven
// grouping over whatever area nodes/in_area edges the catalog actually has
// (design doc §4.4). Falls back to 'type' when a catalog has no area nodes.
type GroupMode = "type" | "area";
const groupMode = ref<GroupMode>("type");
const catalogHasAreas = computed(() => (graph.value ? hasAreaNodes(graph.value) : false));
const areaResolver = computed(() => (graph.value ? buildAreaGroupResolver(graph.value) : null));
const groupByLayerFn = computed(() =>
  groupMode.value === "area" && areaResolver.value ? areaResolver.value : nodeLayerId,
);
const groupLabelFn = computed(() =>
  groupMode.value === "area" && graph.value ? areaGroupLabel(graph.value) : undefined,
);
watch(catalogHasAreas, (hasAreas) => {
  if (!hasAreas) groupMode.value = "type";
});

const catalogPath = computed<string>(() => {
  const p = route.query.catalog;
  return typeof p === "string" && p
    ? p
    : "docs/proposals/project-object-graph/seed-objects.yaml";
});

async function load(): Promise<void> {
  loading.value = true;
  error.value = "";
  try {
    graph.value = await source.loadObjectGraph(catalogPath.value);
    selectedId.value = graph.value.nodes[0]?.id ?? "";
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
    graph.value = null;
  } finally {
    loading.value = false;
  }
}

onMounted(load);
watch(catalogPath, load);
</script>

<style scoped>
.objectgraph-page {
  display: flex;
  flex-direction: column;
  height: 100vh;
  overflow: hidden;
}
.objectgraph-page__bar {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.5rem 1rem;
  border-bottom: 1px solid var(--border-color, #d6dde8);
}
.objectgraph-page__title {
  font-weight: 600;
}
.objectgraph-page__count {
  color: var(--muted-color, #667);
  font-size: 0.85rem;
}
.objectgraph-page__full-graph-link {
  background: none;
  border: none;
  color: #46534d;
  cursor: pointer;
  font-size: 0.8rem;
  margin-left: auto;
  text-decoration: underline;
}
:deep(.catalog-panel) {
  flex: 1;
  min-height: 0;
  overflow: auto;
}
.objectgraph-page__loading,
.objectgraph-page__error {
  padding: 1rem;
}
.objectgraph-page__error {
  color: var(--error-color, #b00020);
}
.objectgraph-page__modal {
  position: fixed;
  inset: 0;
  background: #fff;
  display: flex;
  flex-direction: column;
  z-index: 20;
}
.objectgraph-page__modal-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0.5rem 1rem;
  border-bottom: 1px solid var(--border-color, #d6dde8);
  font-weight: 600;
}
.objectgraph-page__modal-actions {
  display: flex;
  align-items: center;
  gap: 0.75rem;
}
.objectgraph-page__group-toggle {
  display: flex;
  align-items: center;
  gap: 0.35rem;
  font-size: 0.8rem;
  font-weight: 400;
}
.objectgraph-page__modal-bar button {
  background: #1d2a24;
  border: none;
  border-radius: 6px;
  color: #f4f7f3;
  cursor: pointer;
  font-weight: 700;
  padding: 0.35rem 0.9rem;
}
.objectgraph-page__modal-view {
  flex: 1;
  min-height: 0;
}
</style>
