// Shared model helpers for the seed catalog, used by both the catalog
// (list/detail) view and the graph (Vue Flow + ELK) view.

export type EdgeValue = string | string[] | null | undefined;

export interface TypeDef {
  id: string;
  derives_from: string | null;
  summary: string;
}

export interface GraphNode {
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
  executor?: string;
  actor?: string;
  actor_kind?: string;
  experience?: string;
  surface_preference?: string;
  preferred_tools?: string[];
  risk_focus?: string[];
  site_route?: string;
  page_kind?: string;
  edit_surface?: string;
  tagline?: string;
  content_fields?: Record<string, string>;
  media?: Record<string, string>;
  implementation_kind?: string;
  evidence_kind?: string;
  artifacts?: Array<Record<string, string>>;
  sources?: string[];
  edges?: Record<string, EdgeValue>;
}

export interface SeedCatalog {
  catalog: {
    id?: string;
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

export interface Layer {
  id: string;
  title: string;
  short: string;
  description: string;
  types: string[];
}

export const layers: Layer[] = [
  {
    id: "actors",
    title: "Actors, agents, and responsibilities",
    short: "Actors",
    description: "Who uses, owns, plans, implements, reviews, or automates the work represented in the graph — and the concrete personas that drive persona-based QA.",
    types: ["actor", "agent", "persona"],
  },
  {
    id: "site",
    title: "Public product site",
    short: "Site",
    description: "Editable public pages generated from graph-backed capability copy, demo media, and consistency rules.",
    types: ["site-page"],
  },
  {
    id: "capabilities",
    title: "Product capabilities and requirements",
    short: "Capabilities",
    description: "What exists or is desired: product features, requirements, and the user scenarios they support.",
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
    description: "Where shipped capabilities live and what verifies them: code, stories, demos, fixtures, and evidence.",
    types: ["evidence", "implementation"],
  },
];

export function nodeType(node: GraphNode): string {
  return node.schema.split("/")[1] ?? node.schema;
}

export function nodeLayerId(node: GraphNode): string {
  return layers.find((layer) => layer.types.includes(nodeType(node)))?.id ?? "capabilities";
}

export function edgeTargets(value: EdgeValue): string[] {
  if (!value) return [];
  return Array.isArray(value) ? value : [value];
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
  const index = ["actor", "agent", "persona", "site-page", "feature", "requirement", "use-case", "proposal", "change", "evidence", "implementation"].indexOf(type);
  return index === -1 ? 99 : index;
}

export function lifecycleBucket(node: GraphNode): string {
  if (["shipped", "satisfied", "supported"].includes(node.status)) return "available";
  if (node.status === "active") return "active";
  if (node.status === "current") return "proof";
  if (node.status === "proposed") return "roadmap";
  if (node.status === "draft") return "candidate";
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

export function nodeText(node: GraphNode): string {
  return node.summary ?? node.statement ?? node.desired_outcome ?? node.goal ?? node.rationale ?? "No description yet.";
}
