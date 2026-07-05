<template>
  <div class="objectgraph-page" data-testid="objectgraph-page">
    <header class="objectgraph-page__bar">
      <span class="objectgraph-page__title">Project object graph</span>
      <span
        v-if="graph"
        class="objectgraph-page__count"
        data-testid="objectgraph-count"
      >{{ graph.nodes.length }} nodes / {{ graph.edges.length }} edges</span>
      <span class="objectgraph-page__view-toggle" role="group" aria-label="View mode">
        <button
          type="button"
          :class="{ active: viewMode === 'catalog' }"
          data-testid="objectgraph-view-catalog"
          @click="viewMode = 'catalog'"
        >Catalog</button>
        <button
          type="button"
          :class="{ active: viewMode === 'graph' }"
          data-testid="objectgraph-view-graph"
          @click="viewMode = 'graph'"
        >Graph</button>
      </span>
    </header>

    <div v-if="loading" class="objectgraph-page__loading" data-testid="objectgraph-loading">
      Loading…
    </div>
    <div v-else-if="error" class="objectgraph-page__error" data-testid="objectgraph-error">
      {{ error }}
    </div>
    <CatalogPanel
      v-else-if="graph && viewMode === 'catalog'"
      :graph="graph"
      :selected-id="selectedId"
      data-testid="objectgraph-catalog"
      @update:selected-id="selectedId = $event"
    />
    <GraphCanvas
      v-else-if="graph"
      :graph="graph"
      :focus-id="selectedId"
      data-testid="objectgraph-canvas"
      @update:focus-id="selectedId = $event"
    />
  </div>
</template>

<script setup lang="ts">
/**
 * ObjectGraphPage — W5.0: one graph viewer in kitsoki web for the project
 * object graph catalog (internal/graph, W1.0/W1.1), consolidating the two
 * prior prototypes (the proposal-local Vue app and the Vue Flow + ELK
 * mockup in .artifacts/graph-viewer-library-research/) into runstatus.
 *
 * Catalog selected via `?catalog=<path>` (default: the seed review
 * fixture), loaded through runstatus.objectgraph.load — the same
 * kitsoki.graph/v1 wire shape story room graphs use (../components/
 * objectgraph/GraphCanvas.vue is the same renderer as the two merged
 * prototypes, not a rebuild). CatalogPanel.vue restores the prototype's
 * other projection — the layered list/detail catalog — sharing this page's
 * one fetch and selection (picking an object in the catalog focuses it on
 * the canvas and vice versa, same as the prototype).
 */
import { computed, onMounted, ref, watch } from "vue";
import { useRoute } from "vue-router";
import { LiveSource } from "../data/live-source.js";
import type { ObjectGraph } from "../data/objectgraph.js";
import CatalogPanel from "../components/objectgraph/CatalogPanel.vue";
// GraphCanvas.vue is a plain-JS SFC (copied unmodified from the merged W5.0
// viewer prototypes — see components/objectgraph/shims.d.ts); vue-tsc's .vue
// module resolution doesn't pick up ambient `declare module` overrides for
// .vue specifiers the way it does for .js, so this one import is untyped.
// @ts-expect-error — untyped plain-JS SFC, see comment above.
import GraphCanvas from "../components/objectgraph/GraphCanvas.vue";
import "@vue-flow/core/dist/style.css";
import "@vue-flow/core/dist/theme-default.css";
import "@vue-flow/controls/dist/style.css";
import "../components/objectgraph/graph.css";

const route = useRoute();
const source = new LiveSource("/");

const graph = ref<ObjectGraph | null>(null);
const loading = ref(false);
const error = ref("");
// Default stays "graph" — the existing Playwright gate
// (object-graph.spec.ts) asserts objectgraph-canvas renders on first load;
// Catalog is one click away via the toggle.
const viewMode = ref<"catalog" | "graph">("graph");
const selectedId = ref("");

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
.objectgraph-page__view-toggle {
  display: flex;
  gap: 0.4rem;
  margin-left: auto;
}
.objectgraph-page__view-toggle button {
  background: #fff;
  border: 1px solid #d8ddd6;
  border-radius: 999px;
  color: #46534d;
  cursor: pointer;
  font-size: 0.8rem;
  font-weight: 700;
  padding: 0.35rem 0.9rem;
}
.objectgraph-page__view-toggle button.active {
  background: #1d2a24;
  border-color: #1d2a24;
  color: #f4f7f3;
}
.objectgraph-page > :last-child {
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
:deep(.graph-canvas-host) {
  flex: 1;
  min-height: 0;
}
</style>
