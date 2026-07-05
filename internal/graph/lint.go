package graph

import (
	"fmt"
	"sort"
)

// Severity says whether a LintIssue is a hard failure or advisory. The zero
// value (SeverityError) is what every pre-existing check (dangling-ref,
// type-mismatch, cycle, visibility-leak) emits, so old callers that only
// checked len(issues) != 0 keep working unchanged.
type Severity string

const (
	// SeverityError is a hard failure — the catalog is invalid.
	SeverityError Severity = "error"
	// SeverityWarning is advisory — the catalog is incomplete or drifting,
	// but not invalid. Used for staged/migration-era checks (design doc
	// §4.3) that are not mandatory across the whole catalog family yet.
	SeverityWarning Severity = "warning"
)

// LintIssue is one catalog-level invariant violation. Lint returns every
// issue found rather than failing on the first, so a caller (CLI, CI) can
// report the whole picture at once.
type LintIssue struct {
	Node     NodeID
	Kind     string // "dangling-ref" | "type-mismatch" | "cycle" | "visibility-leak" | "orphan-feature" | "initiative-scope"
	Severity Severity
	Message  string
}

func (i LintIssue) Error() string {
	return fmt.Sprintf("%s: %s: %s", i.Node, i.Kind, i.Message)
}

// LintOptions gates the advisory checks added by the
// feature-grouping-taxonomy design (docs §4.3) that stay opt-in.
type LintOptions struct {
	// InitiativeScope enables the initiative-scope check (§4.3.4):
	// initiative.targets must equal the areas derived by walking
	// includes -> change.implements -> feature.in_area. Off by default.
	// Always SeverityWarning — the design doc keeps this one advisory
	// indefinitely, it just keeps the cached `targets` edge honest.
	InitiativeScope bool
}

// Lint validates a fully-loaded catalog's cross-node invariants that a
// single node's own load-time validation cannot see: dangling edge
// references, edge target type-assignability, cycles on edges the type
// registry marks Acyclic, and an internal node reachable from a public
// node's edges. LoadCatalog does not call this — callers (the `kitsoki
// graph lint` CLI, tests) call it explicitly once a catalog is assembled.
//
// opts is variadic so existing call sites (Lint(cat)) are unaffected; pass
// a LintOptions to opt into the staged checks it gates.
func Lint(cat *Catalog, opts ...LintOptions) []LintIssue {
	var opt LintOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	var issues []LintIssue
	issues = append(issues, lintEdgeTargets(cat)...)
	issues = append(issues, lintCycles(cat)...) // covers area-cycle: part_of is Acyclic in the registry
	issues = append(issues, lintVisibilityLeaks(cat)...)
	issues = append(issues, lintOrphanFeature(cat)...)
	if opt.InitiativeScope {
		issues = append(issues, lintInitiativeScope(cat)...)
	}
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
						Node: id, Kind: "dangling-ref", Severity: SeverityError,
						Message: fmt.Sprintf("edge %q points at %q, which does not exist", decl.ID, target),
					})
					continue
				}
				if decl.TargetType != "" && !cat.Registry.IsA(targetNode.TypeID, decl.TargetType) {
					issues = append(issues, LintIssue{
						Node: id, Kind: "type-mismatch", Severity: SeverityError,
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
					Node: n, Kind: "cycle", Severity: SeverityError,
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
			Node: id, Kind: "visibility-leak", Severity: SeverityError,
			Message: fmt.Sprintf("internal node reachable from public node(s) %v", roots),
		})
	}
	return issues
}

// edgeTargetsByFieldID looks up node's effective type, finds the edge field
// declaration named fieldID (present on any type the design doc §4
// registers it on: feature.in_area, change.implements, initiative.includes,
// initiative.targets), and returns its targets plus whether the type
// declares the field at all. A (nil, false) result means the catalog's
// registry never opted into that part of the taxonomy — callers treat that
// as "nothing to check" rather than an error, since dangling-ref /
// type-mismatch already report malformed data elsewhere.
func edgeTargetsByFieldID(cat *Catalog, node *Node, fieldID string) ([]NodeID, bool) {
	eff, ok := cat.Registry.Effective(node.TypeID)
	if !ok {
		return nil, false
	}
	for _, decl := range eff.EdgeFields {
		if string(decl.ID) == fieldID {
			return node.EdgeTargets(decl), true
		}
	}
	return nil, false
}

// lintOrphanFeature reports every public feature with zero in_area targets
// (design doc §4.3.2). Internal features are never flagged — the design's
// migration is incremental (§5), so only the public surface is enforced.
// The check is a hard failure (§5 step 3: the seed catalog is fully
// seeded with in_area, so orphaned public features are now invalid), but
// it only applies to catalogs whose registry declares feature.in_area at
// all — a registry without the area taxonomy has not opted in.
func lintOrphanFeature(cat *Catalog) []LintIssue {
	var issues []LintIssue
	for _, id := range cat.SortedNodeIDs() {
		node := cat.Nodes[id]
		if !cat.Registry.IsA(node.TypeID, "feature") {
			continue
		}
		if node.Visibility != VisibilityPublic {
			continue
		}
		targets, declared := edgeTargetsByFieldID(cat, node, "in_area")
		if !declared || len(targets) > 0 {
			continue
		}
		issues = append(issues, LintIssue{
			Node: id, Kind: "orphan-feature", Severity: SeverityError,
			Message: "public feature has no in_area targets",
		})
	}
	return issues
}

// lintInitiativeScope reports every initiative whose `targets` edge
// disagrees with the derived set of areas reachable by walking:
//
//	initiative.includes -> change.implements -> feature.in_area
//
// (design doc §4.3.4 — the exact traversal implemented here; the design
// doc's parenthetical "feature/implementation" alternative is not walked
// because initiative.includes only targets `change`, and `change.implements`
// already targets `feature` directly — there is no implementation hop on
// this path in the current registry). Always SeverityWarning: the design
// doc keeps this check advisory indefinitely, it exists to catch a cached
// `targets` edge drifting from the changes actually included.
func lintInitiativeScope(cat *Catalog) []LintIssue {
	var issues []LintIssue
	for _, id := range cat.SortedNodeIDs() {
		node := cat.Nodes[id]
		if !cat.Registry.IsA(node.TypeID, "initiative") {
			continue
		}
		derived := map[NodeID]bool{}
		includes, _ := edgeTargetsByFieldID(cat, node, "includes")
		for _, changeID := range includes {
			changeNode, ok := cat.Nodes[changeID]
			if !ok {
				continue // dangling-ref lint already reports this
			}
			implements, _ := edgeTargetsByFieldID(cat, changeNode, "implements")
			for _, featureID := range implements {
				featureNode, ok := cat.Nodes[featureID]
				if !ok {
					continue
				}
				inArea, _ := edgeTargetsByFieldID(cat, featureNode, "in_area")
				for _, areaID := range inArea {
					derived[areaID] = true
				}
			}
		}
		declared := map[NodeID]bool{}
		targets, _ := edgeTargetsByFieldID(cat, node, "targets")
		for _, areaID := range targets {
			declared[areaID] = true
		}
		if setEqual(derived, declared) {
			continue
		}
		issues = append(issues, LintIssue{
			Node: id, Kind: "initiative-scope", Severity: SeverityWarning,
			Message: fmt.Sprintf("targets %v does not match derived area set %v (includes -> change.implements -> feature.in_area)",
				sortedNodeIDKeys(declared), sortedNodeIDKeys(derived)),
		})
	}
	return issues
}

func setEqual(a, b map[NodeID]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func sortedNodeIDKeys(m map[NodeID]bool) []NodeID {
	ids := make([]NodeID, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
