<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { parse } from "yaml";
import seedYaml from "../../seed-objects.yaml?raw";
import GraphCanvas from "./graph/GraphCanvas.vue";
import { seedWireGraph } from "./graph/seed-graph";
// @ts-ignore -- research mock graphs kept as plain JS
import { graphExamples } from "./graph/mock-graphs.js";
import {
  layers,
  nodeType,
  nodeLayerId,
  edgeTargets,
  typeLabel,
  edgeLabel,
  typeOrder,
  lifecycleBucket,
  lifecycleLabel,
  nodeText,
  type EdgeValue,
  type GraphNode,
  type SeedCatalog,
  type TypeDef,
} from "./catalog-model";

const data = parse(seedYaml) as SeedCatalog;
const selectedId = ref("sitepage-feature-agent-actions");
const selectedLayerId = ref("site");
const selectedTypeId = ref("all");
const query = ref("");
const draftFields = ref({ title: "", tagline: "", summary: "" });
const requestStarted = ref(false);

const viewMode = ref<"catalog" | "graph">("catalog");
const graphSourceKey = ref("seed");
const graphDirection = ref("RIGHT");
const graphRadius = ref(2);
const graphSelection = ref<Record<string, unknown> | null>(null);
const exampleFocusId = ref("");

const seedGraph = computed(() => seedWireGraph(data));
const graphSources = computed(() => [
  { key: "seed", title: data.catalog.title, family: "Seed catalog", graph: seedGraph.value },
  ...graphExamples.map((item: { key: string; title: string; family: string; graph: object }) => ({
    key: item.key,
    title: item.title,
    family: item.family,
    graph: item.graph,
  })),
]);
const activeGraphSource = computed(
  () => graphSources.value.find((source) => source.key === graphSourceKey.value) ?? graphSources.value[0],
);
const graphFocusId = computed(() => (graphSourceKey.value === "seed" ? selectedId.value : exampleFocusId.value));

watch(graphSourceKey, () => {
  exampleFocusId.value = "";
  graphSelection.value = null;
});

function onGraphFocus(id: string) {
  if (graphSourceKey.value === "seed") {
    selectNode(id);
  } else {
    exampleFocusId.value = id;
  }
}

const nodeById = computed(() => new Map(data.nodes.map((node) => [node.id, node])));
const typeById = computed(() => new Map(data.type_registry.map((type) => [type.id, type])));
const sourceById = computed(
  () => new Map(data.catalog.source_window.inputs.map((source) => [source.id, source])),
);

const selectedNode = computed(() => nodeById.value.get(selectedId.value) ?? data.nodes[0]);
const selectedType = computed(() => typeById.value.get(nodeType(selectedNode.value)));
const selectedLayer = computed(() => layers.find((layer) => layer.id === selectedLayerId.value) ?? layers[0]);

const typeCounts = computed(() => {
  const counts = new Map<string, number>();
  for (const node of data.nodes) counts.set(nodeType(node), (counts.get(nodeType(node)) ?? 0) + 1);
  return counts;
});

const lifecycleSummaries = computed(() => {
  return ["available", "active", "proof", "roadmap", "candidate"].map((bucket) => ({
    bucket,
    label: lifecycleLabel(bucket),
    count: data.nodes.filter((node) => lifecycleBucket(node) === bucket).length,
  }));
});

const layerSummaries = computed(() => {
  return layers.map((layer) => {
    const nodes = data.nodes.filter((node) => layer.types.includes(nodeType(node)));
    const selected = nodes.some((node) => node.id === selectedNode.value.id);
    return {
      ...layer,
      count: nodes.length,
      selected,
      typeCounts: layer.types.map((type) => ({ type, count: typeCounts.value.get(type) ?? 0 })),
      lifecycleCounts: ["available", "active", "proof", "roadmap", "candidate"]
        .map((bucket) => ({
          bucket,
          label: lifecycleLabel(bucket),
          count: nodes.filter((node) => lifecycleBucket(node) === bucket).length,
        }))
        .filter((entry) => entry.count > 0),
    };
  });
});

const availableTypes = computed(() => {
  return selectedLayer.value.types
    .filter((type) => typeCounts.value.has(type))
    .map((type) => ({ type, label: typeLabel(type), count: typeCounts.value.get(type) ?? 0 }));
});

const filteredNodes = computed(() => {
  const needle = query.value.trim().toLowerCase();
  return data.nodes
    .filter((node) => selectedLayer.value.types.includes(nodeType(node)))
    .filter((node) => selectedTypeId.value === "all" || nodeType(node) === selectedTypeId.value)
    .filter((node) => {
      if (!needle) return true;
      return [node.title, node.id, node.summary, node.statement, node.goal, node.desired_outcome]
        .filter(Boolean)
        .some((value) => String(value).toLowerCase().includes(needle));
    })
    .sort((a, b) => typeOrder(nodeType(a)) - typeOrder(nodeType(b)) || a.title.localeCompare(b.title));
});

const groupedNodes = computed(() => {
  const grouped = new Map<string, GraphNode[]>();
  for (const node of filteredNodes.value) {
    const type = nodeType(node);
    if (!grouped.has(type)) grouped.set(type, []);
    grouped.get(type)?.push(node);
  }
  return [...grouped.entries()].map(([type, nodes]) => ({ type, label: typeLabel(type), nodes }));
});

const outgoingGroups = computed(() => groupEdges(selectedNode.value.edges ?? {}));
const incomingGroups = computed(() => {
  const currentId = selectedNode.value.id;
  const grouped = new Map<string, GraphNode[]>();
  for (const node of data.nodes) {
    for (const [edgeName, value] of Object.entries(node.edges ?? {})) {
      if (!edgeTargets(value).includes(currentId)) continue;
      if (!grouped.has(edgeName)) grouped.set(edgeName, []);
      grouped.get(edgeName)?.push(node);
    }
  }
  return [...grouped.entries()].map(([name, nodes]) => ({ name, label: edgeLabel(name), nodes }));
});

const typeChain = computed(() => {
  const chain: TypeDef[] = [];
  let current = selectedType.value;
  while (current) {
    chain.unshift(current);
    current = current.derives_from ? typeById.value.get(current.derives_from) : undefined;
  }
  return chain;
});

const sourceRows = computed(() => {
  return (selectedNode.value.sources ?? []).map((sourceId) => sourceById.value.get(sourceId)).filter(Boolean);
});

const sitePageForSelection = computed(() => {
  if (nodeType(selectedNode.value) === "site-page") return selectedNode.value;
  const selected = selectedNode.value.id;
  return data.nodes.find((node) => nodeType(node) === "site-page" && edgeTargets(node.edges?.presents).includes(selected));
});

const demoEvidenceForSelection = computed(() => {
  const page = sitePageForSelection.value;
  const mediaEvidenceId = edgeTargets(page?.edges?.has_media)[0];
  if (mediaEvidenceId) return nodeById.value.get(mediaEvidenceId);
  const evidenceId = edgeTargets(selectedNode.value.edges?.evidence)[0];
  return evidenceId ? nodeById.value.get(evidenceId) : undefined;
});

const originalSiteFields = computed(() => {
  const page = sitePageForSelection.value;
  return {
    title: page?.content_fields?.title ?? page?.title ?? selectedNode.value.title,
    tagline: page?.content_fields?.tagline ?? page?.tagline ?? "",
    summary: page?.content_fields?.summary ?? page?.summary ?? nodeText(selectedNode.value),
  };
});

const changedFields = computed(() => {
  return (["title", "tagline", "summary"] as const).filter(
    (field) => draftFields.value[field].trim() !== originalSiteFields.value[field].trim(),
  );
});

const changeEvaluation = computed(() => {
  const combined = Object.values(draftFields.value).join(" ").toLowerCase();
  const changed = changedFields.value;
  const checks = [
    {
      id: "content",
      label: "Product-site content",
      state: changed.length ? "changed" : "unchanged",
      detail: changed.length ? `${changed.length} structured field changes` : "No page field changed yet",
    },
    {
      id: "feature",
      label: "Feature impact",
      state: /\b(new|add|adds|support|supports|integrat|automate|launch|workflow)\b/.test(combined)
        ? "review"
        : "clear",
      detail: "Flags copy that appears to promise a new or expanded capability.",
    },
    {
      id: "consistency",
      label: "Graph consistency",
      state: sitePageForSelection.value && demoEvidenceForSelection.value ? "clear" : "review",
      detail: sitePageForSelection.value
        ? "Page is linked to a feature and demo evidence object."
        : "No site-page object is linked to the selected record.",
    },
    {
      id: "policy",
      label: "Non-negotiable review",
      state: /\b(guarantee|compliance|regulation|regulated|privacy|security|legal|hipaa|gdpr|soc2|soc 2)\b/.test(combined)
        ? "review"
        : "clear",
      detail: "Flags legal, privacy, security, compliance, or absolute claims.",
    },
  ];
  const route = checks.some((check) => check.state === "review")
    ? "Evaluate before delivery"
    : changed.length
      ? "Content-only site update"
      : "No change request";
  return { route, checks };
});

const generatedChangeRequest = computed(() => ({
  schema: "graph/change-request/v0",
  target: sitePageForSelection.value?.id ?? selectedNode.value.id,
  changed_fields: changedFields.value,
  route: changeEvaluation.value.route,
  checks: changeEvaluation.value.checks.map((check) => ({
    id: check.id,
    state: check.state,
  })),
}));

watch(
  originalSiteFields,
  (fields) => {
    draftFields.value = { ...fields };
    requestStarted.value = false;
  },
  { immediate: true },
);

watch(changedFields, () => {
  requestStarted.value = false;
});

function groupEdges(edges: Record<string, EdgeValue>) {
  return Object.entries(edges)
    .map(([name, value]) => ({
      name,
      label: edgeLabel(name),
      nodes: edgeTargets(value)
        .map((id) => nodeById.value.get(id))
        .filter((node): node is GraphNode => Boolean(node)),
    }))
    .filter((group) => group.nodes.length > 0);
}

function selectLayer(layerId: string) {
  selectedLayerId.value = layerId;
  selectedTypeId.value = "all";
}

function selectLayerType(layerId: string, typeId: string) {
  selectedLayerId.value = layerId;
  selectedTypeId.value = typeId;
}

function selectNode(id: string) {
  graphSourceKey.value = "seed";
  selectedId.value = id;
  selectedLayerId.value = nodeLayerId(nodeById.value.get(id) ?? selectedNode.value);
  selectedTypeId.value = "all";
}

</script>

<template>
  <main class="page">
    <header class="masthead">
      <div>
        <p class="kicker">Project object graph</p>
        <h1>Current capabilities, gaps, work, and proof.</h1>
        <p class="intro">
          The graph is organized by actors, public site pages, product capabilities, change work, and proof. Start
          with the shipped product-site pages to see public copy, demo media, linked capabilities, requirements,
          evidence, and the change-request checks behind an edit.
        </p>
      </div>
      <div class="metric-strip">
        <div><strong>{{ data.nodes.length }}</strong><span>objects</span></div>
        <div><strong>{{ data.type_registry.length }}</strong><span>types</span></div>
        <div><strong>{{ data.catalog.source_window.inputs.length }}</strong><span>sources</span></div>
      </div>
    </header>

    <section class="status-legend" aria-label="Lifecycle status summary">
      <span
        v-for="entry in lifecycleSummaries"
        :key="entry.bucket"
        :class="['status-badge', `life-${entry.bucket}`]"
      >
        {{ entry.label }} {{ entry.count }}
      </span>
    </section>

    <section class="view-toggle" aria-label="View mode">
      <button type="button" :class="{ active: viewMode === 'catalog' }" @click="viewMode = 'catalog'">Catalog</button>
      <button type="button" :class="{ active: viewMode === 'graph' }" @click="viewMode = 'graph'">Graph</button>
    </section>

    <section class="layer-map" aria-label="Project object graph layers">
      <article
        v-for="layer in layerSummaries"
        :key="layer.id"
        :class="{ active: selectedLayerId === layer.id, contains: layer.selected }"
      >
        <button class="layer-main" type="button" @click="selectLayer(layer.id)">
          <span class="step">{{ layer.short }}</span>
          <strong>{{ layer.title }}</strong>
          <small>{{ layer.description }}</small>
        </button>
        <span class="layer-statuses">
          <span
            v-for="entry in layer.lifecycleCounts"
            :key="entry.bucket"
            :class="['status-dot', `life-${entry.bucket}`]"
            :title="`${entry.label}: ${entry.count}`"
          >
            {{ entry.count }}
          </span>
        </span>
        <span class="type-summary">
          <button
            v-for="entry in layer.typeCounts"
            :key="entry.type"
            type="button"
            :class="{ active: selectedLayerId === layer.id && selectedTypeId === entry.type }"
            @click.stop="selectLayerType(layer.id, entry.type)"
          >
            {{ typeLabel(entry.type) }} {{ entry.count }}
          </button>
        </span>
      </article>
    </section>

    <section class="workspace">
      <aside class="object-picker">
        <div class="picker-head">
          <div>
            <p class="kicker">{{ selectedLayer.short }}</p>
            <h2>{{ selectedLayer.title }}</h2>
          </div>
          <button v-if="selectedTypeId !== 'all'" @click="selectedTypeId = 'all'">All types</button>
        </div>

        <p class="picker-context">{{ selectedLayer.description }}</p>

        <div class="type-filter" aria-label="Type filter">
          <button :class="{ active: selectedTypeId === 'all' }" @click="selectedTypeId = 'all'">
            All {{ selectedLayer.short.toLowerCase() }}
          </button>
          <button
            v-for="entry in availableTypes"
            :key="entry.type"
            :class="{ active: selectedTypeId === entry.type }"
            @click="selectedTypeId = entry.type"
          >
            {{ entry.label }} {{ entry.count }}
          </button>
        </div>

        <input v-model="query" type="search" placeholder="Search this layer" aria-label="Search objects" />

        <div class="object-list">
          <section v-for="group in groupedNodes" :key="group.type" class="object-group">
            <h3>{{ group.label }}</h3>
            <button
              v-for="node in group.nodes"
              :key="node.id"
              :class="{ selected: selectedNode.id === node.id }"
              @click="selectNode(node.id)"
            >
              <strong>{{ node.title }}</strong>
              <small>{{ node.id }}</small>
              <span class="node-badges">
                <span :class="['status-badge', `life-${lifecycleBucket(node)}`]">{{ lifecycleLabel(lifecycleBucket(node)) }}</span>
                <span :class="['visibility-badge', `visibility-${node.visibility}`]">{{ node.visibility }}</span>
              </span>
            </button>
          </section>
        </div>
      </aside>

      <section v-if="viewMode === 'catalog'" class="focus">
        <article class="focus-card">
          <div class="focus-top">
            <div>
              <p class="kicker">{{ typeLabel(nodeType(selectedNode)) }} / {{ selectedNode.schema }}</p>
              <h2>{{ selectedNode.title }}</h2>
            </div>
            <div class="chips">
              <span :class="['status-badge', `life-${lifecycleBucket(selectedNode)}`]">
                {{ lifecycleLabel(lifecycleBucket(selectedNode)) }} / {{ selectedNode.status }}
              </span>
              <span :class="['visibility-badge', `visibility-${selectedNode.visibility}`]">
                {{ selectedNode.visibility }}
              </span>
            </div>
          </div>

        <p class="body-text">{{ nodeText(selectedNode) }}</p>

          <div
            v-if="selectedNode.actor_kind || selectedNode.trigger || selectedNode.actor || selectedNode.executor || selectedNode.site_route || selectedNode.page_kind || selectedNode.edit_surface || selectedNode.implementation_kind || selectedNode.evidence_kind"
            class="fact-row"
          >
            <div v-if="selectedNode.actor_kind"><span>Actor kind</span><strong>{{ selectedNode.actor_kind }}</strong></div>
            <div v-if="selectedNode.actor"><span>Actor</span><strong>{{ selectedNode.actor }}</strong></div>
            <div v-if="selectedNode.executor"><span>Executor</span><strong>{{ selectedNode.executor }}</strong></div>
            <div v-if="selectedNode.site_route"><span>Product site</span><strong>{{ selectedNode.site_route }}</strong></div>
            <div v-if="selectedNode.page_kind"><span>Page kind</span><strong>{{ selectedNode.page_kind }}</strong></div>
            <div v-if="selectedNode.edit_surface"><span>Edit surface</span><strong>{{ selectedNode.edit_surface }}</strong></div>
            <div v-if="selectedNode.implementation_kind"><span>Implementation</span><strong>{{ selectedNode.implementation_kind }}</strong></div>
            <div v-if="selectedNode.evidence_kind"><span>Evidence</span><strong>{{ selectedNode.evidence_kind }}</strong></div>
            <div v-if="selectedNode.trigger"><span>Trigger</span><strong>{{ selectedNode.trigger }}</strong></div>
          </div>

          <div class="type-chain">
            <span>Type inheritance</span>
            <button
              v-for="type in typeChain"
              :key="type.id"
              @click="selectedTypeId = type.id; selectedLayerId = layers.find((layer) => layer.types.includes(type.id))?.id ?? selectedLayerId"
            >
              {{ type.id }}
            </button>
          </div>
        </article>

        <section v-if="sitePageForSelection" class="site-workbench">
          <article class="site-preview">
            <div>
              <p class="kicker">Product site projection</p>
              <h3>{{ originalSiteFields.title }}</h3>
              <strong>{{ originalSiteFields.tagline }}</strong>
              <p>{{ originalSiteFields.summary }}</p>
            </div>
            <div class="media-box">
              <span>Demo video</span>
              <strong>{{ sitePageForSelection.media?.videoBase ?? demoEvidenceForSelection?.artifacts?.[0]?.videoBase }}</strong>
              <code>{{ sitePageForSelection.media?.expectedPath ?? "No staged media path recorded" }}</code>
              <small v-if="sitePageForSelection.media?.posterStep">Poster step: {{ sitePageForSelection.media.posterStep }}</small>
            </div>
          </article>

          <article class="change-editor">
            <div class="editor-head">
              <div>
                <p class="kicker">Edit structured fields</p>
                <h3>Change request preview</h3>
              </div>
              <span :class="['request-route', changedFields.length ? 'route-active' : 'route-idle']">
                {{ changeEvaluation.route }}
              </span>
            </div>

            <label>
              <span>Title</span>
              <input v-model="draftFields.title" />
            </label>
            <label>
              <span>Tagline</span>
              <input v-model="draftFields.tagline" />
            </label>
            <label>
              <span>Summary</span>
              <textarea v-model="draftFields.summary" rows="4" />
            </label>

            <div class="evaluation-grid">
              <div
                v-for="check in changeEvaluation.checks"
                :key="check.id"
                :class="['evaluation-check', `check-${check.state}`]"
              >
                <span>{{ check.label }}</span>
                <strong>{{ check.state }}</strong>
                <p>{{ check.detail }}</p>
              </div>
            </div>

            <div class="request-actions">
              <button type="button" :disabled="!changedFields.length" @click="requestStarted = true">
                Start change request
              </button>
              <code v-if="requestStarted">{{ JSON.stringify(generatedChangeRequest) }}</code>
              <small v-else>Edits stay as structured draft fields until a change request is started.</small>
            </div>
          </article>
        </section>

        <div class="relationship-board">
          <section>
            <h3>What this object points to</h3>
            <div v-if="!outgoingGroups.length" class="empty">No outgoing relationships.</div>
            <article v-for="group in outgoingGroups" :key="group.name" class="relationship-group">
              <p>{{ group.label }}</p>
              <button v-for="node in group.nodes" :key="node.id" @click="selectNode(node.id)">
                <span>{{ typeLabel(nodeType(node)) }}</span>
                <strong>{{ node.title }}</strong>
                <em :class="['relationship-state', `life-${lifecycleBucket(node)}`]">{{ lifecycleLabel(lifecycleBucket(node)) }}</em>
              </button>
            </article>
          </section>

          <section>
            <h3>What points here</h3>
            <div v-if="!incomingGroups.length" class="empty">No incoming relationships.</div>
            <article v-for="group in incomingGroups" :key="group.name" class="relationship-group incoming">
              <p>{{ group.label }}</p>
              <button v-for="node in group.nodes" :key="node.id" @click="selectNode(node.id)">
                <span>{{ typeLabel(nodeType(node)) }}</span>
                <strong>{{ node.title }}</strong>
                <em :class="['relationship-state', `life-${lifecycleBucket(node)}`]">{{ lifecycleLabel(lifecycleBucket(node)) }}</em>
              </button>
            </article>
          </section>
        </div>

        <section class="sources">
          <h3>Source records</h3>
          <div v-if="!sourceRows.length" class="empty">No source refs.</div>
          <div v-for="source in sourceRows" :key="source?.id" class="source-row">
            <span>{{ source?.kind }}</span>
            <code>{{ source?.path }}</code>
          </div>
        </section>
      </section>

      <section v-else class="focus graph-pane">
        <div class="graph-toolbar">
          <label class="select-control">
            <span>Graph source</span>
            <select v-model="graphSourceKey">
              <option v-for="source in graphSources" :key="source.key" :value="source.key">
                {{ source.family }} — {{ source.title }}
              </option>
            </select>
          </label>
          <div class="segmented" aria-label="Layout direction">
            <button type="button" :class="{ active: graphDirection === 'RIGHT' }" @click="graphDirection = 'RIGHT'">LR</button>
            <button type="button" :class="{ active: graphDirection === 'LEFT' }" @click="graphDirection = 'LEFT'">RL</button>
            <button type="button" :class="{ active: graphDirection === 'DOWN' }" @click="graphDirection = 'DOWN'">TB</button>
            <button type="button" :class="{ active: graphDirection === 'UP' }" @click="graphDirection = 'UP'">BT</button>
          </div>
          <div class="segmented" aria-label="Visible neighborhood">
            <button type="button" :class="{ active: graphRadius === 1 }" @click="graphRadius = 1">1 edge</button>
            <button type="button" :class="{ active: graphRadius === 2 }" @click="graphRadius = 2">2 edges</button>
            <button type="button" :class="{ active: graphRadius === 3 }" @click="graphRadius = 3">3 edges</button>
            <button type="button" :class="{ active: graphRadius === Infinity }" @click="graphRadius = Infinity">All</button>
          </div>
          <div class="stat-row" aria-label="Topology summary">
            <span>{{ activeGraphSource.graph.nodes.length }} nodes</span>
            <span>{{ activeGraphSource.graph.edges.length }} edges</span>
          </div>
        </div>

        <div class="graph-canvas">
          <GraphCanvas
            :graph="activeGraphSource.graph"
            :direction="graphDirection"
            :radius="graphRadius"
            :focus-id="graphFocusId"
            @update:focus-id="onGraphFocus"
            @element-selected="graphSelection = $event"
          />
        </div>

        <div class="graph-detail">
          <pre v-if="graphSelection">{{ JSON.stringify(graphSelection, null, 2) }}</pre>
          <p v-else class="muted">
            Click a node or edge for raw details. Picking a catalog object on the left focuses it in the seed graph.
          </p>
        </div>
      </section>
    </section>
  </main>
</template>
