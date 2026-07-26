package host

import (
	"fmt"
	"sort"

	objectgraph "kitsoki/internal/graph"
)

const (
	graphSnapshotAudiencePublic   = "public"
	graphSnapshotAudienceInternal = "internal"
)

var graphSnapshotEnvelopeFields = map[string]func(*objectgraph.Node) any{
	"title":      func(node *objectgraph.Node) any { return node.Title },
	"status":     func(node *objectgraph.Node) any { return node.Status },
	"visibility": func(node *objectgraph.Node) any { return string(node.Visibility) },
}

// graphSnapshotOp returns a deterministic, bounded catalog projection for
// story-owned application data. It deliberately omits source provenance,
// materialization declarations, and every field the caller did not name.
func graphSnapshotOp(args map[string]any) (Result, error) {
	audience := graphStringArg(args, "audience")
	if audience != graphSnapshotAudiencePublic && audience != graphSnapshotAudienceInternal {
		return Result{}, fmt.Errorf(
			"host.graph.snapshot: %q must be one of %q or %q, got %q",
			"audience", graphSnapshotAudiencePublic, graphSnapshotAudienceInternal, audience,
		)
	}

	fields, err := graphStringListArg(args, "fields")
	if err != nil {
		return Result{}, fmt.Errorf("host.graph.snapshot: %w", err)
	}
	fields, err = normalizeGraphSnapshotFields(fields)
	if err != nil {
		return Result{}, err
	}

	rawMax, ok := args["max_nodes"]
	if !ok {
		return Result{}, fmt.Errorf("host.graph.snapshot: missing required arg %q", "max_nodes")
	}
	maxNodes, err := graphIntArg(rawMax)
	if err != nil {
		return Result{}, fmt.Errorf("host.graph.snapshot: %q must be an integer: %w", "max_nodes", err)
	}
	if maxNodes < 1 {
		return Result{}, fmt.Errorf("host.graph.snapshot: %q must be >= 1, got %d", "max_nodes", maxNodes)
	}

	cat, err := loadCatalogArg(args)
	if err != nil {
		return Result{}, err
	}

	included := make(map[objectgraph.NodeID]struct{}, len(cat.Nodes))
	for _, id := range cat.SortedNodeIDs() {
		node := cat.Nodes[id]
		if audience == graphSnapshotAudiencePublic && node.Visibility != objectgraph.VisibilityPublic {
			continue
		}
		included[id] = struct{}{}
	}
	if len(included) > maxNodes {
		return Result{}, fmt.Errorf(
			"host.graph.snapshot: selected %d nodes exceeds %q %d; refusing to truncate",
			len(included), "max_nodes", maxNodes,
		)
	}

	nodes := make([]any, 0, len(included))
	usedTypes := make(map[string]struct{})
	for _, id := range cat.SortedNodeIDs() {
		if _, ok := included[id]; !ok {
			continue
		}
		node := cat.Nodes[id]
		row := map[string]any{
			"id":      string(node.ID),
			"type_id": node.TypeID,
			"schema":  string(node.Schema),
		}
		for _, field := range fields {
			if envelope, ok := graphSnapshotEnvelopeFields[field]; ok {
				row[field] = envelope(node)
				continue
			}
			value, ok := node.Fields[field]
			if !ok {
				continue
			}
			if !isGraphSnapshotScalar(value) {
				return Result{}, fmt.Errorf(
					"host.graph.snapshot: field %q on node %q is not a scalar",
					field, node.ID,
				)
			}
			row[field] = value
		}
		nodes = append(nodes, row)
		usedTypes[node.TypeID] = struct{}{}
	}

	edges := graphSnapshotEdges(cat, included)
	types := graphSnapshotTypes(cat, usedTypes)
	return Result{Data: map[string]any{
		"snapshot": map[string]any{
			"schema":         "kitsoki/graph-snapshot/v1",
			"audience":       audience,
			"catalog_digest": cat.ContentDigest,
			"nodes":          nodes,
			"edges":          edges,
			"types":          types,
		},
	}}, nil
}

func normalizeGraphSnapshotFields(fields []string) ([]string, error) {
	seen := make(map[string]struct{}, len(fields))
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if field == "" {
			return nil, fmt.Errorf("host.graph.snapshot: %q entries must not be empty", "fields")
		}
		if field == "id" || field == "type_id" || field == "schema" || field == "sources" || field == "edges" {
			return nil, fmt.Errorf("host.graph.snapshot: field %q is structural and may not be requested", field)
		}
		if _, duplicate := seen[field]; duplicate {
			return nil, fmt.Errorf("host.graph.snapshot: field %q is duplicated", field)
		}
		seen[field] = struct{}{}
		out = append(out, field)
	}
	sort.Strings(out)
	return out, nil
}

func isGraphSnapshotScalar(value any) bool {
	switch value.(type) {
	case nil, string, bool, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, float32, float64:
		return true
	default:
		return false
	}
}

func graphSnapshotEdges(cat *objectgraph.Catalog, included map[objectgraph.NodeID]struct{}) []any {
	edges := make([]map[string]string, 0)
	for _, sourceID := range cat.SortedNodeIDs() {
		if _, ok := included[sourceID]; !ok {
			continue
		}
		source := cat.Nodes[sourceID]
		effective, ok := cat.Registry.Effective(source.TypeID)
		if !ok {
			continue
		}
		for _, decl := range effective.EdgeFields {
			targets := append([]objectgraph.NodeID(nil), source.EdgeTargets(decl)...)
			sort.Slice(targets, func(i, j int) bool { return targets[i] < targets[j] })
			for _, targetID := range targets {
				if _, ok := included[targetID]; !ok {
					continue
				}
				edges = append(edges, map[string]string{
					"source": string(sourceID),
					"field":  string(decl.ID),
					"target": string(targetID),
				})
			}
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i]["source"] != edges[j]["source"] {
			return edges[i]["source"] < edges[j]["source"]
		}
		if edges[i]["field"] != edges[j]["field"] {
			return edges[i]["field"] < edges[j]["field"]
		}
		return edges[i]["target"] < edges[j]["target"]
	})
	out := make([]any, len(edges))
	for i := range edges {
		out[i] = edges[i]
	}
	return out
}

func graphSnapshotTypes(cat *objectgraph.Catalog, used map[string]struct{}) []any {
	included := make(map[string]struct{}, len(used))
	for typeID := range used {
		effective, ok := cat.Registry.Effective(typeID)
		if !ok {
			continue
		}
		for _, ancestor := range effective.Ancestry {
			included[ancestor] = struct{}{}
		}
	}
	types := make([]any, 0, len(included))
	for _, def := range cat.Registry.All() {
		if _, ok := included[def.ID]; !ok {
			continue
		}
		types = append(types, map[string]any{
			"id":      def.ID,
			"schema":  string(def.Schema),
			"extends": nilIfEmpty(def.Extends),
		})
	}
	return types
}
