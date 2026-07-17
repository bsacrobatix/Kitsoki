package graph

// This file adds the read-side traversal/index helpers the graph-mcp plan's
// P1 read ops (host.graph.get/find/neighbors) need — graph-mcp-plan.md §3.3.
// It is deliberately a NEW, separate surface from internal/materialize's
// ContextClosure (materialize.go), not a relocation of it: materialize.go's
// existing (single, tested) caller keeps its outgoing-only/unbounded-depth
// contract unchanged, and internal/host must not import internal/materialize
// to reach a shared helper. Every function here reads edges exclusively via
// Node.EdgeTargets(decl) against the registry decl — never the raw Edges
// map — per the plan's §3.3 spec rule (top_level-storage edge fields, e.g.
// change.depends_on, must be visible to these reads).

// EdgeRef names one resolved edge field a node carries — the vocabulary
// unit UNKNOWN_EDGE-style errors and per-type edge census rows use.
type EdgeRef struct {
	Field      EdgeField
	TargetType string
}

// RefIn is one inbound reference to a node: another node that targets it via
// a named edge field.
type RefIn struct {
	Node      NodeID
	EdgeField EdgeField
}

// ReverseIndex maps a node id to every inbound reference reaching it —
// built by a single O(nodes * edge fields) forward pass over the catalog
// (there is no cached reverse-edge map on Catalog). Shared by graph.get's
// refs_in, graph.find's no_inbound/no_outbound filters, and
// Neighbors' inbound/both directions, so all three agree on IsA/top_level
// handling instead of drifting across three ad hoc scans.
type ReverseIndex map[NodeID][]RefIn

// BuildReverseIndex walks every node's effective edge fields (via
// EdgeTargets, never node.Edges directly) and indexes each target's inbound
// references. Iteration order is cat.SortedNodeIDs() so results are
// reproducible across runs, matching every other read op's determinism
// discipline.
func BuildReverseIndex(cat *Catalog) ReverseIndex {
	idx := make(ReverseIndex)
	for _, id := range cat.SortedNodeIDs() {
		node := cat.Nodes[id]
		eff, ok := cat.Registry.Effective(node.TypeID)
		if !ok {
			continue
		}
		for _, decl := range eff.EdgeFields {
			for _, target := range node.EdgeTargets(decl) {
				idx[target] = append(idx[target], RefIn{Node: id, EdgeField: decl.ID})
			}
		}
	}
	return idx
}

// NeighborTriple is one (from, edge, to) hop discovered by Neighbors, along
// with the BFS depth at which "to" was first reached and the direction the
// hop was walked in ("out" | "in").
type NeighborTriple struct {
	From      NodeID
	EdgeField EdgeField
	To        NodeID
	Direction string
	Depth     int
}

// NeighborsDirection selects which edge direction Neighbors walks.
type NeighborsDirection string

const (
	DirectionOut  NeighborsDirection = "out"
	DirectionIn   NeighborsDirection = "in"
	DirectionBoth NeighborsDirection = "both"
)

// Neighbors is the generalized, depth-bounded, direction-aware BFS behind
// host.graph.neighbors — the graph-mcp-plan.md §3.3 "generalized
// ContextClosure" item. Unlike materialize.ContextClosure (outgoing-only,
// unbounded depth, flat id-list result), Neighbors:
//   - bounds depth at maxDepth (1..3 in the op contract; a caller passing 0
//     or negative is treated as depth 1, matching the op's documented
//     default),
//   - supports "out", "in", and "both" (union) directions, "in" resolved via
//     a freshly built ReverseIndex (no cached reverse map exists),
//   - filters to edgeKinds when non-empty (empty = every edge field on the
//     type registry, both declared and inherited),
//   - returns (from, edge, to, direction, depth) triples, not just a
//     reached-id list, so a client can render the path, not merely the
//     frontier.
//
// The root itself is never included in the result. When limit > 0, the walk
// stops once len(triples) reaches limit (a hard cap, not a per-level cap) —
// deterministic because BFS order is deterministic (SortedNodeIDs()-ordered
// tie-breaking at each level).
func Neighbors(cat *Catalog, root NodeID, direction NeighborsDirection, edgeKinds []EdgeField, maxDepth int, limit int) []NeighborTriple {
	if maxDepth <= 0 {
		maxDepth = 1
	}
	if direction == "" {
		direction = DirectionBoth
	}
	kindSet := make(map[EdgeField]bool, len(edgeKinds))
	for _, k := range edgeKinds {
		kindSet[k] = true
	}

	var revIdx ReverseIndex
	if direction == DirectionIn || direction == DirectionBoth {
		revIdx = BuildReverseIndex(cat)
	}

	type frontierEntry struct {
		id    NodeID
		depth int
	}

	visited := map[NodeID]bool{root: true}
	queue := []frontierEntry{{id: root, depth: 0}}
	var triples []NeighborTriple

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth >= maxDepth {
			continue
		}

		var hops []NeighborTriple

		if direction == DirectionOut || direction == DirectionBoth {
			node, ok := cat.Nodes[cur.id]
			if ok {
				if eff, ok := cat.Registry.Effective(node.TypeID); ok {
					for _, decl := range eff.EdgeFields {
						if len(kindSet) > 0 && !kindSet[decl.ID] {
							continue
						}
						for _, target := range node.EdgeTargets(decl) {
							hops = append(hops, NeighborTriple{
								From: cur.id, EdgeField: decl.ID, To: target,
								Direction: string(DirectionOut), Depth: cur.depth + 1,
							})
						}
					}
				}
			}
		}

		if direction == DirectionIn || direction == DirectionBoth {
			for _, ref := range revIdx[cur.id] {
				if len(kindSet) > 0 && !kindSet[ref.EdgeField] {
					continue
				}
				hops = append(hops, NeighborTriple{
					From: ref.Node, EdgeField: ref.EdgeField, To: cur.id,
					Direction: string(DirectionIn), Depth: cur.depth + 1,
				})
			}
		}

		// Deterministic ordering: by target/source id then edge field.
		sortNeighborHops(hops)

		for _, hop := range hops {
			if limit > 0 && len(triples) >= limit {
				return triples
			}
			triples = append(triples, hop)
			// The "other end" of the hop relative to cur is the node to
			// continue the BFS from.
			var next NodeID
			if hop.Direction == string(DirectionOut) {
				next = hop.To
			} else {
				next = hop.From
			}
			if !visited[next] {
				visited[next] = true
				queue = append(queue, frontierEntry{id: next, depth: cur.depth + 1})
			}
		}
	}
	return triples
}

// ImpactedNode is one node transitively reachable from a TransitiveImpact
// root by walking inbound edges — i.e. a node whose removal/retype could
// ripple through to the root. Unlike NeighborTriple (one row per edge),
// each node the closure reaches appears exactly once, described by the
// discovery hop that first reached it during the BFS.
type ImpactedNode struct {
	// Node is the impacted node's id.
	Node NodeID
	// EdgeField is the edge field of the discovery hop: the declared edge
	// that Node itself carries, pointing at Parent (Path[1]).
	EdgeField EdgeField
	// Depth is the BFS depth from the root (1 = a direct inbound
	// reference to root).
	Depth int
	// Path is the discovery chain from Node down to the root, inclusive
	// of both ends: Path[0] == Node, Path[len(Path)-1] == root.
	Path []NodeID
}

// TransitiveImpact walks inbound edges from root transitively — BFS,
// cycle-safe, deterministic — optionally restricted to edgeKinds (empty
// means every declared edge field, matching Neighbors' convention). It is
// the closure host.graph.query's impact mode's `transitive: true` option
// needs (graph-mcp: transitive impact closure): "who ultimately depends on
// root, directly or indirectly, and by what path."
//
// root itself is never included, even if a cycle loops back to it. Each
// reachable node appears exactly once, at the shallowest depth it is first
// discovered, with the edge field and path of that discovery hop — the
// same single-discovery-per-node BFS-tree semantics Neighbors already
// implements for direction "in" (see its doc comment), reused here rather
// than reimplemented: TransitiveImpact calls Neighbors(root, DirectionIn,
// edgeKinds, <depth bound covering the whole graph>, 0) and folds its
// edge-level triples down to one row per node by keeping only the first
// (deterministically ordered) triple that discovers each node — exactly
// the triple Neighbors itself used to decide whether to enqueue that node,
// so the fold requires no BFS logic of its own.
//
// The depth bound passed to Neighbors is len(cat.Nodes): with a visited
// set preventing revisits, no simple path can exceed that many hops, so
// this is "unbounded" for any real catalog without needing a sentinel
// value Neighbors would have to special-case.
func TransitiveImpact(cat *Catalog, root NodeID, edgeKinds []EdgeField) []ImpactedNode {
	maxDepth := len(cat.Nodes)
	if maxDepth < 1 {
		maxDepth = 1
	}
	triples := Neighbors(cat, root, DirectionIn, edgeKinds, maxDepth, 0)

	// path[n] is the discovery chain from n down to root, inclusive of
	// both ends. Seeded with root itself so a depth-1 child's path can
	// append onto it uniformly with every deeper child.
	path := map[NodeID][]NodeID{root: {root}}
	discovered := map[NodeID]bool{root: true}

	var out []ImpactedNode
	for _, t := range triples {
		child, parent := t.From, t.To
		if discovered[child] {
			// Either an extra (non-tree) inbound edge onto an
			// already-discovered node, or a cycle hop back onto root —
			// in both cases the node keeps its first (shallowest)
			// discovery, per the doc comment.
			continue
		}
		discovered[child] = true

		childPath := make([]NodeID, 0, len(path[parent])+1)
		childPath = append(childPath, child)
		childPath = append(childPath, path[parent]...)
		path[child] = childPath

		out = append(out, ImpactedNode{
			Node:      child,
			EdgeField: t.EdgeField,
			Depth:     t.Depth,
			Path:      childPath,
		})
	}
	return out
}

// sortNeighborHops orders hops deterministically: by the "other end" node
// id, then edge field, then direction — so Neighbors' output (and BFS
// enqueue order) never depends on map iteration order.
func sortNeighborHops(hops []NeighborTriple) {
	otherEnd := func(h NeighborTriple) NodeID {
		if h.Direction == string(DirectionOut) {
			return h.To
		}
		return h.From
	}
	// insertion sort is fine here: hop counts per BFS level are small
	// (bounded by fan-out * edge-field count), and this keeps the file
	// dependency-free.
	for i := 1; i < len(hops); i++ {
		j := i
		for j > 0 {
			a, b := hops[j-1], hops[j]
			less := otherEnd(a) < otherEnd(b) ||
				(otherEnd(a) == otherEnd(b) && a.EdgeField < b.EdgeField) ||
				(otherEnd(a) == otherEnd(b) && a.EdgeField == b.EdgeField && a.Direction < b.Direction)
			if less {
				break
			}
			hops[j-1], hops[j] = hops[j], hops[j-1]
			j--
		}
	}
}

// NearestIDs returns up to n candidate ids from cat closest to want by a
// simple, deterministic prefix/Levenshtein-lite distance — the "nearest-id
// suggestions" graph.get's `missing` list needs for unknown ids. Not a
// general-purpose spellchecker: cheap, deterministic, good enough to
// suggest "req-alpha" for a typo'd "req-alph".
func NearestIDs(cat *Catalog, want string, n int) []string {
	type scored struct {
		id    string
		score int
	}
	var candidates []scored
	for _, id := range cat.SortedNodeIDs() {
		candidates = append(candidates, scored{id: string(id), score: levenshteinLite(want, string(id))})
	}
	// Deterministic sort: by score, then lexicographic id (SortedNodeIDs
	// already gives id order, so a stable sort on score alone preserves it).
	for i := 1; i < len(candidates); i++ {
		j := i
		for j > 0 && candidates[j-1].score > candidates[j].score {
			candidates[j-1], candidates[j] = candidates[j], candidates[j-1]
			j--
		}
	}
	if n <= 0 || n > len(candidates) {
		n = len(candidates)
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, candidates[i].id)
	}
	return out
}

// levenshteinLite is a standard Levenshtein edit distance (insert/delete/
// substitute, unit cost) — "lite" only in that it makes no attempt at
// transposition/phonetic matching, which is unnecessary for kebab-id typo
// suggestions.
func levenshteinLite(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	la, lb := len(ra), len(rb)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	cur := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		cur[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := cur[j-1] + 1
			sub := prev[j-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			cur[j] = m
		}
		prev, cur = cur, prev
	}
	return prev[lb]
}
