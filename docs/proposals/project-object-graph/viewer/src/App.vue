<script setup lang="ts">
import { computed, ref } from "vue";
import { parse } from "yaml";
import seedYaml from "../../seed-objects.yaml?raw";

type EdgeValue = string | string[] | null | undefined;

interface TypeDef {
  id: string;
  derives_from: string | null;
  summary: string;
}

interface GraphNode {
  schema: string;
  id: string;
  title: string;
  status: string;
  visibility: string;
  summary?: string;
  statement?: string;
  rationale?: string;
  desired_outcome?: string;
  trigger?: string;
  goal?: string;
  evidence_kind?: string;
  proposal_kind?: string;
  implementation_kind?: string;
  executor?: string;
  actor?: string;
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
const query = ref("");
const selectedFamily = ref("all");

const nodeById = computed(() => new Map(data.nodes.map((node) => [node.id, node])));
const typeById = computed(() => new Map(data.type_registry.map((type) => [type.id, type])));
const sourceById = computed(
  () => new Map(data.catalog.source_window.inputs.map((source) => [source.id, source])),
);

const selectedNode = computed(() => nodeById.value.get(selectedId.value) ?? data.nodes[0]);
const selectedType = computed(() => typeById.value.get(nodeType(selectedNode.value)));

const families = computed(() => {
  const counts = new Map<string, number>();
  for (const node of data.nodes) counts.set(nodeType(node), (counts.get(nodeType(node)) ?? 0) + 1);
  return [...counts.entries()]
    .sort(([a], [b]) => familyOrder(a) - familyOrder(b) || a.localeCompare(b))
    .map(([id, count]) => ({ id, label: typeLabel(id), count }));
});

const filteredNodes = computed(() => {
  const needle = query.value.trim().toLowerCase();
  return data.nodes
    .filter((node) => selectedFamily.value === "all" || nodeType(node) === selectedFamily.value)
    .filter((node) => {
      if (!needle) return true;
      return [node.title, node.id, node.summary, node.statement, node.goal, node.desired_outcome]
        .filter(Boolean)
        .some((value) => String(value).toLowerCase().includes(needle));
    })
    .sort((a, b) => familyOrder(nodeType(a)) - familyOrder(nodeType(b)) || a.title.localeCompare(b.title));
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

function nodeType(node: GraphNode): string {
  return node.schema.split("/")[1] ?? node.schema;
}

function edgeTargets(value: EdgeValue): string[] {
  if (!value) return [];
  return Array.isArray(value) ? value : [value];
}

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

function selectNode(id: string) {
  selectedId.value = id;
}

function typeLabel(type: string): string {
  const labels: Record<string, string> = {
    feature: "Features",
    requirement: "Requirements",
    "use-case": "Use cases",
    proposal: "Proposals",
    evidence: "Evidence",
    implementation: "Implementations",
    change: "Work items",
  };
  return labels[type] ?? type;
}

function edgeLabel(edge: string): string {
  const labels: Record<string, string> = {
    requirements: "must satisfy",
    use_cases: "used by",
    evidence: "proved by",
    proposed_by: "proposed by",
    implemented_by: "implemented by",
    required_by: "required by",
    motivated_by: "motivated by",
    verified_by: "verified by",
    exercises: "exercises",
    acceptance: "acceptance",
    demonstrates: "demonstrates",
    verifies: "verifies",
    implements: "implements",
    creates_requirements: "creates requirements",
    creates_use_cases: "creates use cases",
    proposes: "proposes",
    decomposes_to: "breaks into",
    satisfies: "satisfies",
  };
  return labels[edge] ?? edge.replaceAll("_", " ");
}

function familyOrder(type: string): number {
  return ["feature", "requirement", "use-case", "proposal", "change", "evidence", "implementation"].indexOf(type);
}

function nodeText(node: GraphNode): string {
  return node.summary ?? node.statement ?? node.desired_outcome ?? node.goal ?? node.rationale ?? "No description yet.";
}
</script>

<template>
  <main class="page">
    <header class="masthead">
      <div>
        <p class="kicker">Data object graph seed</p>
        <h1>What is connected to what?</h1>
        <p class="intro">
          This is the first object-graph fixture rendered as data. Pick an object, then follow its typed
          relationships. The prose is just a field on the object.
        </p>
      </div>
      <div class="metric-strip">
        <div><strong>{{ data.nodes.length }}</strong><span>objects</span></div>
        <div><strong>{{ families.length }}</strong><span>object types</span></div>
        <div><strong>{{ data.catalog.source_window.inputs.length }}</strong><span>source files</span></div>
      </div>
    </header>

    <section class="map-strip" aria-label="Catalog map">
      <button
        v-for="family in families"
        :key="family.id"
        :class="{ active: selectedFamily === family.id }"
        @click="selectedFamily = selectedFamily === family.id ? 'all' : family.id"
      >
        <span>{{ family.label }}</span>
        <strong>{{ family.count }}</strong>
      </button>
    </section>

    <section class="workspace">
      <aside class="object-picker">
        <div class="picker-head">
          <h2>Objects</h2>
          <button v-if="selectedFamily !== 'all'" @click="selectedFamily = 'all'">Clear type</button>
        </div>
        <input v-model="query" type="search" placeholder="Search objects" aria-label="Search objects" />
        <div class="object-list">
          <button
            v-for="node in filteredNodes"
            :key="node.id"
            :class="{ selected: selectedNode.id === node.id }"
            @click="selectNode(node.id)"
          >
            <span class="object-kind">{{ typeLabel(nodeType(node)) }}</span>
            <strong>{{ node.title }}</strong>
            <small>{{ node.id }}</small>
          </button>
        </div>
      </aside>

      <section class="focus">
        <article class="focus-card">
          <div class="focus-top">
            <div>
              <p class="kicker">{{ typeLabel(nodeType(selectedNode)) }}</p>
              <h2>{{ selectedNode.title }}</h2>
            </div>
            <div class="chips">
              <span>{{ selectedNode.status }}</span>
              <span>{{ selectedNode.visibility }}</span>
            </div>
          </div>

          <p class="body-text">{{ nodeText(selectedNode) }}</p>

          <div v-if="selectedNode.trigger || selectedNode.actor || selectedNode.executor" class="fact-row">
            <div v-if="selectedNode.actor"><span>Actor</span><strong>{{ selectedNode.actor }}</strong></div>
            <div v-if="selectedNode.executor"><span>Executor</span><strong>{{ selectedNode.executor }}</strong></div>
            <div v-if="selectedNode.trigger"><span>Trigger</span><strong>{{ selectedNode.trigger }}</strong></div>
          </div>

          <div class="type-chain">
            <span>Type chain</span>
            <button v-for="type in typeChain" :key="type.id" @click="selectedFamily = type.id">
              {{ type.id }}
            </button>
          </div>
        </article>

        <div class="relationship-board">
          <section>
            <h3>Links out from this object</h3>
            <div v-if="!outgoingGroups.length" class="empty">No outgoing relationships.</div>
            <article v-for="group in outgoingGroups" :key="group.name" class="relationship-group">
              <p>{{ group.label }}</p>
              <button v-for="node in group.nodes" :key="node.id" @click="selectNode(node.id)">
                <span>{{ typeLabel(nodeType(node)) }}</span>
                <strong>{{ node.title }}</strong>
              </button>
            </article>
          </section>

          <section>
            <h3>Links into this object</h3>
            <div v-if="!incomingGroups.length" class="empty">No incoming relationships.</div>
            <article v-for="group in incomingGroups" :key="group.name" class="relationship-group incoming">
              <p>{{ group.label }}</p>
              <button v-for="node in group.nodes" :key="node.id" @click="selectNode(node.id)">
                <span>{{ typeLabel(nodeType(node)) }}</span>
                <strong>{{ node.title }}</strong>
              </button>
            </article>
          </section>
        </div>

        <section class="sources">
          <h3>Where this came from</h3>
          <div v-if="!sourceRows.length" class="empty">No source refs.</div>
          <div v-for="source in sourceRows" :key="source?.id" class="source-row">
            <span>{{ source?.kind }}</span>
            <code>{{ source?.path }}</code>
          </div>
        </section>
      </section>
    </section>
  </main>
</template>
