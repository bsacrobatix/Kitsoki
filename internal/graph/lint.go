package graph

import (
	"fmt"
	"sort"
)

// LintIssue is one catalog-level invariant violation. Lint returns every
// issue found rather than failing on the first, so a caller (CLI, CI) can
// report the whole picture at once.
type LintIssue struct {
	Node    NodeID
	Kind    string // "dangling-ref" | "type-mismatch" | "cycle" | "visibility-leak"
	Message string
}

func (i LintIssue) Error() string {
	return fmt.Sprintf("%s: %s: %s", i.Node, i.Kind, i.Message)
}

// Lint validates a fully-loaded catalog's cross-node invariants that a
// single node's own load-time validation cannot see: dangling edge
// references, edge target type-assignability, cycles on edges the type
// registry marks Acyclic, and an internal node reachable from a public
// node's edges. LoadCatalog does not call this — callers (the `kitsoki
// graph lint` CLI, tests) call it explicitly once a catalog is assembled.
func Lint(cat *Catalog) []LintIssue {
	var issues []LintIssue
	issues = append(issues, lintEdgeTargets(cat)...)
	issues = append(issues, lintCycles(cat)...)
	issues = append(issues, lintVisibilityLeaks(cat)...)
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Node != issues[j].Node {
			return issues[i].Node < issues[j].Node
		}
		return issues[i].Message < issues[j].Message
	})
	return issues
}

func lintEdgeTargets(cat *Catalog) []LintIssue {
	var issues []LintIssue
	for _, id := range cat.SortedNodeIDs() {
		node := cat.Nodes[id]
		eff, ok := cat.Registry.Effective(node.TypeID)
		if !ok {
			continue // unknown type would already have failed LoadCatalog
		}
		for _, decl := range eff.EdgeFields {
			for _, target := range node.EdgeTargets(decl) {
				targetNode, exists := cat.Nodes[target]
				if !exists {
					issues = append(issues, LintIssue{
						Node: id, Kind: "dangling-ref",
						Message: fmt.Sprintf("edge %q points at %q, which does not exist", decl.ID, target),
					})
					continue
				}
				if decl.TargetType != "" && !cat.Registry.IsA(targetNode.TypeID, decl.TargetType) {
					issues = append(issues, LintIssue{
						Node: id, Kind: "type-mismatch",
						Message: fmt.Sprintf("edge %q points at %q (type %q), which is not a %q", decl.ID, target, targetNode.TypeID, decl.TargetType),
					})
				}
			}
		}
	}
	return issues
}

func lintCycles(cat *Catalog) []LintIssue {
	var issues []LintIssue
	seenCycleNodes := map[NodeID]bool{}
	for _, id := range cat.SortedNodeIDs() {
		if seenCycleNodes[id] {
			continue
		}
		if cyclePath, ok := findCycleFrom(cat, id); ok {
			for _, n := range cyclePath {
				if seenCycleNodes[n] {
					continue
				}
				seenCycleNodes[n] = true
				issues = append(issues, LintIssue{
					Node: n, Kind: "cycle",
					Message: fmt.Sprintf("acyclic edge forms a cycle: %v", cyclePath),
				})
			}
		}
	}
	return issues
}

// findCycleFrom does a DFS over acyclic-marked edges starting at start,
// returning the cycle (start of loop .. back to start) if one is found.
func findCycleFrom(cat *Catalog, start NodeID) ([]NodeID, bool) {
	var path []NodeID
	onPath := map[NodeID]bool{}
	var visit func(id NodeID) ([]NodeID, bool)
	visit = func(id NodeID) ([]NodeID, bool) {
		if onPath[id] {
			// Found the cycle: trim path to the repeated node.
			for i, n := range path {
				if n == id {
					return append(append([]NodeID{}, path[i:]...), id), true
				}
			}
			return nil, true
		}
		node, ok := cat.Nodes[id]
		if !ok {
			return nil, false
		}
		eff, ok := cat.Registry.Effective(node.TypeID)
		if !ok {
			return nil, false
		}
		onPath[id] = true
		path = append(path, id)
		for _, decl := range eff.EdgeFields {
			if !decl.Acyclic {
				continue
			}
			for _, target := range node.EdgeTargets(decl) {
				if cyclePath, found := visit(target); found {
					return cyclePath, true
				}
			}
		}
		onPath[id] = false
		path = path[:len(path)-1]
		return nil, false
	}
	return visit(start)
}

// lintVisibilityLeaks walks edges outward from every public node and reports
// any internal node it can reach — the invariant public site codegen (G3)
// depends on: an internal node must never be rendered because it was
// transitively linked from a public one. Traversal does not continue past
// an internal node (site codegen would not recurse into it either).
func lintVisibilityLeaks(cat *Catalog) []LintIssue {
	var issues []LintIssue
	reportedFrom := map[NodeID]map[NodeID]bool{} // internal node -> set of public roots that reach it
	for _, rootID := range cat.SortedNodeIDs() {
		root := cat.Nodes[rootID]
		if root.Visibility != VisibilityPublic {
			continue
		}
		visited := map[NodeID]bool{rootID: true}
		queue := []NodeID{rootID}
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			node := cat.Nodes[id]
			eff, ok := cat.Registry.Effective(node.TypeID)
			if !ok {
				continue
			}
			for _, decl := range eff.EdgeFields {
				if !decl.Renders {
					continue // traceability edge, not something codegen renders through
				}
				for _, target := range node.EdgeTargets(decl) {
					if visited[target] {
						continue
					}
					visited[target] = true
					targetNode, exists := cat.Nodes[target]
					if !exists {
						continue // dangling-ref lint already reports this
					}
					if targetNode.Visibility == VisibilityInternal {
						if reportedFrom[target] == nil {
							reportedFrom[target] = map[NodeID]bool{}
						}
						reportedFrom[target][rootID] = true
						continue // don't recurse past an internal node
					}
					queue = append(queue, target)
				}
			}
		}
	}
	leakIDs := make([]NodeID, 0, len(reportedFrom))
	for id := range reportedFrom {
		leakIDs = append(leakIDs, id)
	}
	sort.Slice(leakIDs, func(i, j int) bool { return leakIDs[i] < leakIDs[j] })
	for _, id := range leakIDs {
		roots := make([]string, 0, len(reportedFrom[id]))
		for r := range reportedFrom[id] {
			roots = append(roots, string(r))
		}
		sort.Strings(roots)
		issues = append(issues, LintIssue{
			Node: id, Kind: "visibility-leak",
			Message: fmt.Sprintf("internal node reachable from public node(s) %v", roots),
		})
	}
	return issues
}
