// Converts the parsed seed catalog into the kitsoki.graph/v1 wire shape the
// GraphCanvas renderer consumes (same shape as internal/app/graph/wire.go and
// the mock graphs kept in mock-graphs.js).

import { edgeLabel, edgeTargets, nodeLayerId, nodeType, type SeedCatalog } from "../catalog-model";

export interface WireNode {
  id: string;
  label: string;
  kind: string;
  status?: string;
  ref: { kind: string; ref: string };
  attrs?: Record<string, unknown>;
}

export interface WireEdge {
  id: string;
  source: string;
  target: string;
  label: string;
  kind: string;
  status?: string;
  attrs?: Record<string, unknown>;
}

export interface WireGraph {
  graph_id: string;
  kind: string;
  cyclic: boolean;
  meta?: Record<string, unknown>;
  layout_hints?: { default?: string; rankdir?: string };
  nodes: WireNode[];
  edges: WireEdge[];
}

export function seedWireGraph(data: SeedCatalog): WireGraph {
  const nodes: WireNode[] = data.nodes.map((node) => ({
    id: node.id,
    label: node.title,
    kind: nodeType(node),
    status: node.status,
    ref: { kind: "object", ref: node.id },
    attrs: {
      layer: nodeLayerId(node),
      visibility: node.visibility,
    },
  }));

  const knownIds = new Set(nodes.map((node) => node.id));
  const edges: WireEdge[] = [];
  for (const node of data.nodes) {
    for (const [edgeName, value] of Object.entries(node.edges ?? {})) {
      for (const target of edgeTargets(value)) {
        if (!knownIds.has(target)) continue;
        edges.push({
          id: `${node.id}--${edgeName}--${target}`,
          source: node.id,
          target,
          label: edgeLabel(edgeName),
          kind: edgeName,
          attrs: node.id === target ? { route: "loop" } : {},
        });
      }
    }
  }

  return {
    graph_id: data.catalog.id ?? "project-object-graph-seed",
    kind: "project-object-graph",
    cyclic: true,
    meta: {
      status: data.catalog.status,
      purpose: data.catalog.purpose,
      sampled_at: data.catalog.source_window.sampled_at,
    },
    layout_hints: { default: "layered", rankdir: "RIGHT" },
    nodes,
    edges,
  };
}
