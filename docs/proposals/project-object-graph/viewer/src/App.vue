<script setup lang="ts">
import { computed, ref } from "vue";
import { parse } from "yaml";
import seedYaml from "../../seed-objects.yaml?raw";

type EdgeValue = string | string[] | null | undefined;

interface TypeDef {
  id: string;
  schema: string;
  derives_from: string | null;
  summary: string;
  edge_fields?: Array<{
    id: string;
    target_type: string;
    cardinality: string;
  }>;
}

interface GraphNode {
  schema: string;
  id: string;
  title: string;
  status: string;
  visibility: string;
  summary?: string;
  statement?: string;
  desired_outcome?: string;
  goal?: string;
  evidence_kind?: string;
  proposal_kind?: string;
  implementation_kind?: string;
  executor?: string;
  sources?: string[];
  edges?: Record<string, EdgeValue>;
}

interface SeedCatalog {
  catalog: {
    title: string;
    status: string;
    purpose: string;
    source_window: {
      sampled_at: string;
      inputs: Array<{ id: string; kind: string; path: string }>;
    };
  };
  type_registry: TypeDef[];
  nodes: GraphNode[];
}

const data = parse(seedYaml) as SeedCatalog;
const selectedId = ref("feature-project-object-graph");
const filterText = ref("");
const activeType = ref("all");

const nodeById = computed(() => new Map(data.nodes.map((node) => [node.id, node])));
const sourceById = computed(
  () => new Map(data.catalog.source_window.inputs.map((source) => [source.id, source])),
);
const typeById = computed(() => new Map(data.type_registry.map((type) => [type.id, type])));

const typeCounts = computed(() => {
  const counts = new Map<string, number>();
  for (const node of data.nodes) {
    counts.set(nodeType(node), (counts.get(nodeType(node)) ?? 0) + 1);
  }
  return counts;
});

const filteredNodes = computed(() => {
  const needle = filterText.value.trim().toLowerCase();
  return data.nodes.filter((node) => {
    const typeMatch = activeType.value === "all" || nodeType(node) === activeType.value;
    if (!typeMatch) return false;
    if (!needle) return true;
    return [node.id, node.title, node.status, node.visibility, node.summary, node.statement, node.goal]
      .filter(Boolean)
      .some((value) => String(value).toLowerCase().includes(needle));
  });
});

const groupedNodes = computed(() => {
  const groups = new Map<string, GraphNode[]>();
  for (const node of filteredNodes.value) {
    const type = nodeType(node);
    if (!groups.has(type)) groups.set(type, []);
    groups.get(type)?.push(node);
  }
  return [...groups.entries()].sort(([a], [b]) => a.localeCompare(b));
});

const selectedNode = computed(() => nodeById.value.get(selectedId.value) ?? filteredNodes.value[0]);
const selectedType = computed(() => selectedNode.value ? typeById.value.get(nodeType(selectedNode.value)) : undefined);

const outgoingEdges = computed(() => {
  const node = selectedNode.value;
  if (!node?.edges) return [];
  return Object.entries(node.edges)
    .flatMap(([label, value]) => edgeTargets(value).map((target) => ({ label, target })))
    .filter((edge) => edge.target);
});

const incomingEdges = computed(() => {
  const current = selectedNode.value?.id;
  if (!current) return [];
  const edges: Array<{ from: string; label: string }> = [];
  for (const node of data.nodes) {
    for (const [label, value] of Object.entries(node.edges ?? {})) {
      if (edgeTargets(value).includes(current)) edges.push({ from: node.id, label });
    }
  }
  return edges;
});

const typeLinks = computed(() => {
  return data.type_registry.map((type) => ({
    ...type,
    children: data.type_registry.filter((candidate) => candidate.derives_from === type.id),
  }));
});

function nodeType(node: GraphNode): string {
  return node.schema.split("/")[1] ?? node.schema;
}

function edgeTargets(value: EdgeValue): string[] {
  if (!value) return [];
  return Array.isArray(value) ? value : [value];
}

function selectNode(id: string) {
  if (nodeById.value.has(id)) selectedId.value = id;
}

function selectType(type: string) {
  activeType.value = type;
}

function nodeDescription(node: GraphNode): string {
  return node.summary ?? node.statement ?? node.desired_outcome ?? node.goal ?? "No description field on this node.";
}
</script>

<template>
  <main class="app-shell">
    <header class="topbar">
      <div>
        <p class="eyebrow">{{ data.catalog.purpose }} / {{ data.catalog.status }}</p>
        <h1>{{ data.catalog.title }}</h1>
      </div>
      <div class="stats" aria-label="Catalog statistics">
        <span>{{ data.nodes.length }} nodes</span>
        <span>{{ data.type_registry.length }} types</span>
        <span>{{ data.catalog.source_window.inputs.length }} sources</span>
      </div>
    </header>

    <section class="toolbar" aria-label="Filters">
      <label class="search">
        <span>Search</span>
        <input v-model="filterText" type="search" placeholder="node id, title, requirement text" />
      </label>
      <div class="type-tabs" aria-label="Node type filter">
        <button :class="{ active: activeType === 'all' }" @click="selectType('all')">
          All <span>{{ data.nodes.length }}</span>
        </button>
        <button
          v-for="type in data.type_registry.filter((entry) => typeCounts.has(entry.id))"
          :key="type.id"
          :class="{ active: activeType === type.id }"
          @click="selectType(type.id)"
        >
          {{ type.id }} <span>{{ typeCounts.get(type.id) }}</span>
        </button>
      </div>
    </section>

    <section class="layout">
      <aside class="panel type-panel">
        <h2>Types</h2>
        <div class="type-list">
          <article v-for="type in typeLinks" :key="type.id" class="type-card">
            <button class="type-name" @click="selectType(type.id)">
              {{ type.id }}
            </button>
            <p>{{ type.summary }}</p>
            <div v-if="type.derives_from" class="type-relation">
              extends
              <button @click="selectType(type.derives_from)">{{ type.derives_from }}</button>
            </div>
            <div v-if="type.children.length" class="type-relation">
              composed by
              <button v-for="child in type.children" :key="child.id" @click="selectType(child.id)">
                {{ child.id }}
              </button>
            </div>
          </article>
        </div>
      </aside>

      <section class="panel node-panel">
        <h2>Nodes</h2>
        <div v-if="!filteredNodes.length" class="empty">No nodes match the current filters.</div>
        <section v-for="[type, nodes] in groupedNodes" :key="type" class="node-group">
          <div class="group-heading">
            <h3>{{ type }}</h3>
            <span>{{ nodes.length }}</span>
          </div>
          <button
            v-for="node in nodes"
            :key="node.id"
            class="node-row"
            :class="{ selected: selectedNode?.id === node.id }"
            @click="selectNode(node.id)"
          >
            <span class="node-title">{{ node.title }}</span>
            <span class="node-meta">{{ node.id }}</span>
            <span class="badges">
              <span>{{ node.status }}</span>
              <span>{{ node.visibility }}</span>
            </span>
          </button>
        </section>
      </section>

      <section class="panel detail-panel" aria-live="polite">
        <template v-if="selectedNode">
          <div class="detail-heading">
            <div>
              <p class="eyebrow">{{ selectedNode.schema }}</p>
              <h2>{{ selectedNode.title }}</h2>
            </div>
            <span class="status">{{ selectedNode.status }}</span>
          </div>
          <p class="description">{{ nodeDescription(selectedNode) }}</p>

          <dl class="facts">
            <div>
              <dt>ID</dt>
              <dd>{{ selectedNode.id }}</dd>
            </div>
            <div>
              <dt>Visibility</dt>
              <dd>{{ selectedNode.visibility }}</dd>
            </div>
            <div v-if="selectedType">
              <dt>Type</dt>
              <dd>
                <button class="inline-link" @click="selectType(selectedType.id)">{{ selectedType.id }}</button>
                <span v-if="selectedType.derives_from">
                  extends
                  <button class="inline-link" @click="selectType(selectedType.derives_from)">
                    {{ selectedType.derives_from }}
                  </button>
                </span>
              </dd>
            </div>
          </dl>

          <section class="edge-section">
            <h3>Outgoing edges</h3>
            <div v-if="!outgoingEdges.length" class="empty compact">No outgoing edges.</div>
            <button
              v-for="edge in outgoingEdges"
              :key="`${edge.label}:${edge.target}`"
              class="edge-row"
              @click="selectNode(edge.target)"
            >
              <span>{{ edge.label }}</span>
              <strong>{{ nodeById.get(edge.target)?.title ?? edge.target }}</strong>
              <small>{{ edge.target }}</small>
            </button>
          </section>

          <section class="edge-section">
            <h3>Incoming edges</h3>
            <div v-if="!incomingEdges.length" class="empty compact">No incoming edges.</div>
            <button
              v-for="edge in incomingEdges"
              :key="`${edge.from}:${edge.label}`"
              class="edge-row reverse"
              @click="selectNode(edge.from)"
            >
              <span>{{ edge.label }}</span>
              <strong>{{ nodeById.get(edge.from)?.title ?? edge.from }}</strong>
              <small>{{ edge.from }}</small>
            </button>
          </section>

          <section class="sources">
            <h3>Sources</h3>
            <div v-for="sourceId in selectedNode.sources ?? []" :key="sourceId" class="source-row">
              <span>{{ sourceById.get(sourceId)?.kind ?? "source" }}</span>
              <code>{{ sourceById.get(sourceId)?.path ?? sourceId }}</code>
            </div>
          </section>
        </template>
      </section>
    </section>
  </main>
</template>
