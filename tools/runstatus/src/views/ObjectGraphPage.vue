<template>
  <div class="objectgraph-page" data-testid="objectgraph-page">
    <header class="objectgraph-page__bar">
      <span class="objectgraph-page__title">Project object graph</span>
      <span
        v-if="graph"
        class="objectgraph-page__count"
        data-testid="objectgraph-count"
      >{{ graph.nodes.length }} nodes / {{ graph.edges.length }} edges</span>
    </header>

    <div v-if="loading" class="objectgraph-page__loading" data-testid="objectgraph-loading">
      Loading…
    </div>
    <div v-else-if="error" class="objectgraph-page__error" data-testid="objectgraph-error">
      {{ error }}
    </div>
    <GraphCanvas
      v-else-if="graph"
      :graph="graph"
      data-testid="objectgraph-canvas"
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
 * prototypes, not a rebuild).
 */
import { computed, onMounted, ref, watch } from "vue";
import { useRoute } from "vue-router";
import { LiveSource } from "../data/live-source.js";
import type { ObjectGraph } from "../data/objectgraph.js";
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
