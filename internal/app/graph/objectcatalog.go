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
		g.Nodes = append(g.Nodes, GraphNode{
			ID:     string(node.ID),
			Kind:   node.TypeID,
			Label:  node.Title,
			Ref:    GraphRef{Kind: "object-graph-node", Ref: string(node.ID)},
			Status: node.Status,
			Attrs:  map[string]any{"visibility": string(node.Visibility)},
		})

		eff, ok := cat.Registry.Effective(node.TypeID)
		if !ok {
			continue
		}
		for _, decl := range eff.EdgeFields {
			for _, target := range node.EdgeTargets(decl) {
				g.Edges = append(g.Edges, GraphEdge{
					ID:     string(node.ID) + ":" + string(decl.ID) + ":" + string(target),
					Kind:   string(decl.ID),
					Source: string(node.ID),
					Target: string(target),
				})
			}
		}
	}
	return g
}
