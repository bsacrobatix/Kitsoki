package graph

import (
	objectgraph "kitsoki/internal/graph"
)

// ObjectCatalogGraph adapts a loaded project object graph catalog
// (internal/graph, the W1.0/W1.1 substrate) into the renderer-neutral
// kitsoki.graph/v1 wire shape this package already defines for story room
// graphs (W5.0's "keep the wire shape from internal/app/graph/wire.go so
// story graphs and object graphs share a renderer" decision).
func ObjectCatalogGraph(cat *objectgraph.Catalog, graphID string) KitsokiGraph {
	g := KitsokiGraph{
		Schema:   SchemaV1,
		GraphID:  graphID,
		Kind:     "object-graph",
		Directed: true,
	}
	for _, id := range cat.SortedNodeIDs() {
		node := cat.Nodes[id]
		sources := make([]string, 0, len(node.Sources))
		for _, s := range node.Sources {
			sources = append(sources, string(s))
		}
		attrs := map[string]any{
			"visibility": string(node.Visibility),
			"sources":    sources,
			// fields carries every type-specific value the shared envelope
			// doesn't promote (summary, statement, goal, content_fields,
			// media, ...) so catalog-style clients (CatalogPanel.vue) can
			// render node detail generically, without the Go side
			// hardcoding a per-type field list.
			"fields": node.Fields,
		}

		eff, ok := cat.Registry.Effective(node.TypeID)
		if ok {
			attrs["type_chain"] = eff.Ancestry
		}

		g.Nodes = append(g.Nodes, GraphNode{
			ID:     string(node.ID),
			Kind:   node.TypeID,
			Label:  node.Title,
			Ref:    GraphRef{Kind: "object-graph-node", Ref: string(node.ID)},
			Status: node.Status,
			Attrs:  attrs,
		})

		if !ok {
			continue
		}
		for _, decl := range eff.EdgeFields {
			var edgeAttrs map[string]any
			if decl.NestsUnder {
				// Threads the type registry's nests_under marker (W6.2
				// follow-up) onto the wire edge so a generic list/detail UI
				// projection (unlike a graph canvas, where the edge itself
				// already draws the relationship) can nest a source node
				// under its target without a hand-maintained kind->edge
				// table that silently drifts from the type registry.
				edgeAttrs = map[string]any{"nests_under": true}
			}
			for _, target := range node.EdgeTargets(decl) {
				g.Edges = append(g.Edges, GraphEdge{
					ID:     string(node.ID) + ":" + string(decl.ID) + ":" + string(target),
					Kind:   string(decl.ID),
					Source: string(node.ID),
					Target: string(target),
					Attrs:  edgeAttrs,
				})
			}
		}
	}
	return g
}
