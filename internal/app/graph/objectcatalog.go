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
		g.Nodes = append(g.Nodes, catalogGraphNode(cat, node))
		g.Edges = append(g.Edges, catalogGraphEdges(cat, node)...)
	}
	return g
}

// ObjectCatalogDiffGraph adapts a (current, desired) catalog pair into one
// kitsoki.graph/v1 graph for diff-mode rendering: every node carries a
// "diff_kind" attr ("added" | "modified" | "removed" | "unchanged") from
// objectgraph.DiffNodes, so a diff-mode UI badges nodes without
// recomputing the classification itself. Desired's data wins for
// added/modified/unchanged nodes; nodes present only in current (removed)
// are appended from current so they stay visible in diff mode.
func ObjectCatalogDiffGraph(current, desired *objectgraph.Catalog, graphID string) KitsokiGraph {
	g := ObjectCatalogGraph(desired, graphID)
	g.Kind = "object-graph-diff"

	kindByID := make(map[string]objectgraph.GapKind, len(desired.Nodes))
	var removed []objectgraph.NodeDiff
	for _, d := range objectgraph.DiffNodes(current, desired) {
		kindByID[string(d.ID)] = d.Kind
		if d.Kind == objectgraph.GapRemoved {
			removed = append(removed, d)
		}
	}

	for i := range g.Nodes {
		kind, ok := kindByID[g.Nodes[i].ID]
		if !ok {
			kind = "unchanged"
		}
		g.Nodes[i].Attrs["diff_kind"] = string(kind)
	}

	for _, d := range removed {
		node := current.Nodes[d.ID]
		gn := catalogGraphNode(current, node)
		gn.Attrs["diff_kind"] = string(objectgraph.GapRemoved)
		g.Nodes = append(g.Nodes, gn)
		g.Edges = append(g.Edges, catalogGraphEdges(current, node)...)
	}
	return g
}

// catalogGraphNode builds one node's wire representation — shared by
// ObjectCatalogGraph's normal pass and ObjectCatalogDiffGraph's synthesis of
// removed nodes from the current catalog.
func catalogGraphNode(cat *objectgraph.Catalog, node *objectgraph.Node) GraphNode {
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
	if eff, ok := cat.Registry.Effective(node.TypeID); ok {
		attrs["type_chain"] = eff.Ancestry
	}
	return GraphNode{
		ID:     string(node.ID),
		Kind:   node.TypeID,
		Label:  node.Title,
		Ref:    GraphRef{Kind: "object-graph-node", Ref: string(node.ID)},
		Status: node.Status,
		Attrs:  attrs,
	}
}

// catalogGraphEdges builds one node's outgoing wire edges — shared the same
// way catalogGraphNode is.
func catalogGraphEdges(cat *objectgraph.Catalog, node *objectgraph.Node) []GraphEdge {
	eff, ok := cat.Registry.Effective(node.TypeID)
	if !ok {
		return nil
	}
	var edges []GraphEdge
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
			edges = append(edges, GraphEdge{
				ID:     string(node.ID) + ":" + string(decl.ID) + ":" + string(target),
				Kind:   string(decl.ID),
				Source: string(node.ID),
				Target: string(target),
				Attrs:  edgeAttrs,
			})
		}
	}
	return edges
}
