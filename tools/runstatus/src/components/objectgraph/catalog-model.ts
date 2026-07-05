// Shared model helpers for the catalog (list/detail) projection of the
// project object graph. Ported from the deleted W5.0-prototype
// docs/proposals/project-object-graph/viewer/src/catalog-model.ts and
// adapted to read the kitsoki.graph/v1 wire shape (ObjectGraph, served by
// runstatus.objectgraph.load) instead of the raw seed-objects.yaml — the
// per-type scalars that were top-level GraphNode fields there now live in
// `node.attrs.fields` (internal/app/graph's ObjectCatalogGraph puts the
// substrate's Node.Fields there verbatim).
import type { ObjectGraph, ObjectGraphNode } from "../../data/objectgraph.js";

export interface Layer {
  id: string;
  title: string;
  short: string;
  description: string;
  types: string[];
}

// This layer taxonomy is presentation curation over the seed catalog's
// current type set, not something the type registry's `extends` chains
// encode (every seed type extends `core-node` directly — see
// docs/proposals/project-object-graph/seed-objects.yaml). It intentionally
// mirrors the deleted prototype's grouping.
export const layers: Layer[] = [
  {
    id: "actors",
    title: "Actors, agents, and responsibilities",
    short: "Actors",
    description:
      "Who uses, owns, plans, implements, reviews, or automates the work represented in the graph — and the concrete personas that drive persona-based QA.",
    types: ["actor", "agent", "persona"],
  },
  {
    id: "site",
    title: "Public product site",
    short: "Site",
    description:
      "Editable public pages generated from graph-backed capability copy, demo media, and consistency rules.",
    types: ["site-page"],
  },
  {
    id: "capabilities",
    title: "Product capabilities and requirements",
    short: "Capabilities",
    description:
      "What exists or is desired: product features, requirements, and the user scenarios they support.",
    types: ["feature", "requirement", "use-case"],
  },
  {
    id: "delta",
    title: "Change and roadmap work",
    short: "Delta",
    description: "How the project moves from current state to desired state: proposals and work items.",
    types: ["proposal", "change"],
  },
  {
    id: "proof",
    title: "Implementation and proof",
    short: "Proof",
    description:
      "Where shipped capabilities live and what verifies them: code, stories, demos, fixtures, and evidence.",
    types: ["evidence", "implementation"],
  },
];

export function nodeFields(node: ObjectGraphNode): Record<string, unknown> {
  return (node.attrs?.fields as Record<string, unknown>) ?? {};
}

export function nodeVisibility(node: ObjectGraphNode): string {
  return (node.attrs?.visibility as string) ?? "internal";
}

export function nodeSources(node: ObjectGraphNode): string[] {
  return (node.attrs?.sources as string[]) ?? [];
}

export function nodeTypeChain(node: ObjectGraphNode): string[] {
  return (node.attrs?.type_chain as string[]) ?? [node.kind];
}

export function nodeLayerId(node: ObjectGraphNode): string {
  return layers.find((layer) => layer.types.includes(node.kind))?.id ?? "capabilities";
}

export function typeLabel(type: string): string {
  const labels: Record<string, string> = {
    actor: "Actors",
    agent: "Agents",
    persona: "Personas",
    "site-page": "Site pages",
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

export function edgeLabel(edge: string): string {
  const labels: Record<string, string> = {
    actor: "actor",
    uses: "uses",
    owns: "owns",
    participates_in: "participates in",
    assigned_changes: "assigned changes",
    owned_by: "owned by",
    assigned_to: "assigned to",
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
    persona_of: "persona of",
    qa_evidence: "QA evidence",
    from_persona: "from persona",
  };
  return labels[edge] ?? edge.replaceAll("_", " ");
}

export function typeOrder(type: string): number {
  const index = [
    "actor",
    "agent",
    "persona",
    "site-page",
    "feature",
    "requirement",
    "use-case",
    "proposal",
    "change",
    "evidence",
    "implementation",
  ].indexOf(type);
  return index === -1 ? 99 : index;
}

export function lifecycleBucket(node: ObjectGraphNode): string {
  const status = node.status ?? "";
  if (["shipped", "satisfied", "supported"].includes(status)) return "available";
  if (status === "active") return "active";
  if (status === "current") return "proof";
  if (status === "proposed") return "roadmap";
  return "candidate";
}

export function lifecycleLabel(bucket: string): string {
  const labels: Record<string, string> = {
    available: "Available",
    active: "Active",
    proof: "Proof",
    roadmap: "Roadmap",
    candidate: "Candidate",
  };
  return labels[bucket] ?? bucket;
}

export function nodeText(node: ObjectGraphNode): string {
  const fields = nodeFields(node);
  return (
    (fields.summary as string) ??
    (fields.statement as string) ??
    (fields.desired_outcome as string) ??
    (fields.goal as string) ??
    (fields.rationale as string) ??
    "No description yet."
  );
}

export interface RelationshipGroup {
  name: string;
  label: string;
  nodes: ObjectGraphNode[];
}

// Outgoing/incoming are computed from the wire graph's edge list (edge.kind
// is the edge field name, e.g. "presents", "evidence", "persona_of" — see
// internal/app/graph's ObjectCatalogGraph) rather than from a node's own
// `edges` map, since the wire shape flattens all edges to one list shared
// with story-room graphs.
export function outgoingGroups(graph: ObjectGraph, nodeId: string): RelationshipGroup[] {
  const nodeById = new Map(graph.nodes.map((n) => [n.id, n]));
  const grouped = new Map<string, ObjectGraphNode[]>();
  for (const edge of graph.edges) {
    if (edge.source !== nodeId) continue;
    const target = nodeById.get(edge.target);
    if (!target) continue;
    if (!grouped.has(edge.kind)) grouped.set(edge.kind, []);
    grouped.get(edge.kind)?.push(target);
  }
  return [...grouped.entries()].map(([name, nodes]) => ({ name, label: edgeLabel(name), nodes }));
}

export function incomingGroups(graph: ObjectGraph, nodeId: string): RelationshipGroup[] {
  const nodeById = new Map(graph.nodes.map((n) => [n.id, n]));
  const grouped = new Map<string, ObjectGraphNode[]>();
  for (const edge of graph.edges) {
    if (edge.target !== nodeId) continue;
    const source = nodeById.get(edge.source);
    if (!source) continue;
    if (!grouped.has(edge.kind)) grouped.set(edge.kind, []);
    grouped.get(edge.kind)?.push(source);
  }
  return [...grouped.entries()].map(([name, nodes]) => ({ name, label: edgeLabel(name), nodes }));
}

export function edgeTargetsByKind(graph: ObjectGraph, nodeId: string, kind: string): ObjectGraphNode[] {
  const nodeById = new Map(graph.nodes.map((n) => [n.id, n]));
  return graph.edges
    .filter((edge) => edge.source === nodeId && edge.kind === kind)
    .map((edge) => nodeById.get(edge.target))
    .filter((node): node is ObjectGraphNode => Boolean(node));
}
