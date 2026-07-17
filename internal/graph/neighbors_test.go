package graph

import (
	"sort"
	"testing"
)

const readsFixturePath = "../host/testdata/graph-reads-fixture.yaml"
const impactFixturePath = "../host/testdata/graph-impact-fixture.yaml"

func loadReadsFixture(t *testing.T) *Catalog {
	t.Helper()
	cat, err := LoadCatalog(readsFixturePath)
	if err != nil {
		t.Fatalf("LoadCatalog(%s): %v", readsFixturePath, err)
	}
	return cat
}

func loadImpactFixture(t *testing.T) *Catalog {
	t.Helper()
	cat, err := LoadCatalog(impactFixturePath)
	if err != nil {
		t.Fatalf("LoadCatalog(%s): %v", impactFixturePath, err)
	}
	return cat
}

func TestBuildReverseIndex_TopLevelStorageEdgeVisible(t *testing.T) {
	cat := loadReadsFixture(t)
	idx := BuildReverseIndex(cat)

	// change-leaf --depends_on--> change-root is a storage:top_level edge
	// (read from Fields, not Edges) — this is the exact hazard the plan's
	// §3.3 spec rule calls out: a reverse index built off node.Edges alone
	// would silently miss it.
	refs := idx["change-root"]
	var found bool
	for _, r := range refs {
		if r.Node == "change-leaf" && r.EdgeField == "depends_on" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected change-root's reverse index to include change-leaf via top_level depends_on, got %+v", refs)
	}
}

func TestBuildReverseIndex_NormalEdgeStorage(t *testing.T) {
	cat := loadReadsFixture(t)
	idx := BuildReverseIndex(cat)

	refs := idx["req-alpha"]
	var found bool
	for _, r := range refs {
		if r.Node == "uc-alpha" && r.EdgeField == "covers" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected req-alpha's reverse index to include uc-alpha via covers, got %+v", refs)
	}
}

func TestNeighbors_OutgoingRespectsDepthAndEdgeFilter(t *testing.T) {
	cat := loadReadsFixture(t)

	// depth 1, out, only "acceptance": req-alpha -> uc-alpha.
	triples := Neighbors(cat, "req-alpha", DirectionOut, []EdgeField{"acceptance"}, 1, 0)
	if len(triples) != 1 || triples[0].To != "uc-alpha" || triples[0].EdgeField != "acceptance" {
		t.Fatalf("unexpected triples: %+v", triples)
	}

	// depth 2, out, unfiltered: req-alpha -acceptance-> uc-alpha -covers-> req-alpha (cycle, already visited so not re-walked further, but the hop itself is recorded once).
	triples2 := Neighbors(cat, "req-alpha", DirectionOut, nil, 2, 0)
	if len(triples2) == 0 {
		t.Fatal("expected at least one triple at depth 2")
	}
	var sawDepth2 bool
	for _, tr := range triples2 {
		if tr.Depth == 2 {
			sawDepth2 = true
		}
		if tr.Depth > 2 {
			t.Errorf("triple %+v exceeds requested depth 2", tr)
		}
	}
	if !sawDepth2 {
		t.Errorf("expected a depth-2 hop, got %+v", triples2)
	}
}

func TestNeighbors_InboundViaTopLevelEdge(t *testing.T) {
	cat := loadReadsFixture(t)

	// change-root has no outgoing depends_on, but change-leaf depends on it
	// (top_level storage) — direction "in" must surface that hop.
	triples := Neighbors(cat, "change-root", DirectionIn, []EdgeField{"depends_on"}, 1, 0)
	if len(triples) != 1 || triples[0].From != "change-leaf" || triples[0].To != "change-root" {
		t.Fatalf("expected one inbound depends_on triple from change-leaf, got %+v", triples)
	}
}

func TestNeighbors_BothDirectionsUnion(t *testing.T) {
	cat := loadReadsFixture(t)
	triples := Neighbors(cat, "req-alpha", DirectionBoth, nil, 1, 0)
	var directions []string
	for _, tr := range triples {
		directions = append(directions, tr.Direction)
	}
	sort.Strings(directions)
	var hasOut, hasIn bool
	for _, d := range directions {
		if d == "out" {
			hasOut = true
		}
		if d == "in" {
			hasIn = true
		}
	}
	if !hasOut || !hasIn {
		t.Fatalf("expected both out and in hops from req-alpha, got directions %v (triples=%+v)", directions, triples)
	}
}

func TestNeighbors_LimitCaps(t *testing.T) {
	cat := loadReadsFixture(t)
	triples := Neighbors(cat, "req-alpha", DirectionBoth, nil, 2, 1)
	if len(triples) != 1 {
		t.Fatalf("expected exactly 1 triple with limit=1, got %d: %+v", len(triples), triples)
	}
}

func TestNeighbors_DeterministicAcrossRuns(t *testing.T) {
	cat := loadReadsFixture(t)
	first := Neighbors(cat, "req-alpha", DirectionBoth, nil, 2, 0)
	for i := 0; i < 5; i++ {
		again := Neighbors(cat, "req-alpha", DirectionBoth, nil, 2, 0)
		if len(again) != len(first) {
			t.Fatalf("run %d: length differs: %d vs %d", i, len(again), len(first))
		}
		for j := range first {
			if again[j] != first[j] {
				t.Fatalf("run %d: triple %d differs: %+v vs %+v", i, j, again[j], first[j])
			}
		}
	}
}

// TestTransitiveImpact_ThreeLevelChain exercises the linear inbound chain
// in graph-impact-fixture.yaml: c1 (depth 1) <- c2 (depth 2) <- c3 (depth
// 3), all via depends_on, and checks each node's reported depth, edge
// field, and discovery path (node down to root, inclusive of both ends).
func TestTransitiveImpact_ThreeLevelChain(t *testing.T) {
	cat := loadImpactFixture(t)
	closure := TransitiveImpact(cat, "target", nil)

	byNode := map[NodeID]ImpactedNode{}
	for _, n := range closure {
		byNode[n.Node] = n
	}

	c1, ok := byNode["c1"]
	if !ok || c1.Depth != 1 || c1.EdgeField != "depends_on" {
		t.Fatalf("expected c1 at depth 1 via depends_on, got %+v (ok=%v)", c1, ok)
	}
	if got, want := c1.Path, []NodeID{"c1", "target"}; !pathEqual(got, want) {
		t.Errorf("c1.Path = %v, want %v", got, want)
	}

	c2, ok := byNode["c2"]
	if !ok || c2.Depth != 2 || c2.EdgeField != "depends_on" {
		t.Fatalf("expected c2 at depth 2 via depends_on, got %+v (ok=%v)", c2, ok)
	}
	if got, want := c2.Path, []NodeID{"c2", "c1", "target"}; !pathEqual(got, want) {
		t.Errorf("c2.Path = %v, want %v", got, want)
	}

	c3, ok := byNode["c3"]
	if !ok || c3.Depth != 3 || c3.EdgeField != "depends_on" {
		t.Fatalf("expected c3 at depth 3 via depends_on, got %+v (ok=%v)", c3, ok)
	}
	if got, want := c3.Path, []NodeID{"c3", "c2", "c1", "target"}; !pathEqual(got, want) {
		t.Errorf("c3.Path = %v, want %v", got, want)
	}
}

// TestTransitiveImpact_CycleSafe exercises the cy1<->cy2 inbound cycle:
// both depend on each other (in addition to cy1 depending directly on
// target), so a naive walk would loop forever. Each must appear exactly
// once, at its shallowest depth (cy1=1, cy2=2), and the whole call must
// terminate.
func TestTransitiveImpact_CycleSafe(t *testing.T) {
	cat := loadImpactFixture(t)
	closure := TransitiveImpact(cat, "target", nil)

	counts := map[NodeID]int{}
	byNode := map[NodeID]ImpactedNode{}
	for _, n := range closure {
		counts[n.Node]++
		byNode[n.Node] = n
	}

	if counts["cy1"] != 1 {
		t.Fatalf("expected cy1 exactly once in the closure, got %d: %+v", counts["cy1"], closure)
	}
	if counts["cy2"] != 1 {
		t.Fatalf("expected cy2 exactly once in the closure, got %d: %+v", counts["cy2"], closure)
	}
	if cy1 := byNode["cy1"]; cy1.Depth != 1 {
		t.Errorf("cy1.Depth = %d, want 1 (direct referencer of target)", cy1.Depth)
	}
	if cy2 := byNode["cy2"]; cy2.Depth != 2 {
		t.Errorf("cy2.Depth = %d, want 2", cy2.Depth)
	}
	// The cycle-back edge (cy1 depends_on cy2, discovered while walking
	// inbound from cy2) must not resurrect an already-discovered node —
	// i.e. target itself must never appear as "impacted".
	if _, ok := byNode["target"]; ok {
		t.Errorf("root (target) must never appear in its own closure, got %+v", closure)
	}
}

// TestTransitiveImpact_EdgeKindsFilter checks that edge_kinds restricts
// which declared edge fields the walk follows: blk1 only reaches target
// via "blocks", so it must be excluded when filtering to "depends_on" and
// present (alone) when filtering to "blocks".
func TestTransitiveImpact_EdgeKindsFilter(t *testing.T) {
	cat := loadImpactFixture(t)

	dependsOnly := TransitiveImpact(cat, "target", []EdgeField{"depends_on"})
	for _, n := range dependsOnly {
		if n.Node == "blk1" {
			t.Fatalf("blk1 (blocks-only referencer) unexpectedly present when filtered to depends_on: %+v", dependsOnly)
		}
	}
	var sawChain bool
	for _, n := range dependsOnly {
		if n.Node == "c1" || n.Node == "c2" || n.Node == "c3" {
			sawChain = true
		}
	}
	if !sawChain {
		t.Fatalf("expected the depends_on chain to survive the depends_on filter, got %+v", dependsOnly)
	}

	blocksOnly := TransitiveImpact(cat, "target", []EdgeField{"blocks"})
	if len(blocksOnly) != 1 || blocksOnly[0].Node != "blk1" || blocksOnly[0].EdgeField != "blocks" {
		t.Fatalf("expected exactly blk1 via blocks when filtered to blocks, got %+v", blocksOnly)
	}
}

// TestTransitiveImpact_UnrelatedComponentExcluded checks that the
// other->other2 component (no path to target) never appears in target's
// closure, and that "other" itself (nothing references it — it's the root
// of its own component) has an empty closure.
func TestTransitiveImpact_UnrelatedComponentExcluded(t *testing.T) {
	cat := loadImpactFixture(t)

	closure := TransitiveImpact(cat, "target", nil)
	for _, n := range closure {
		if n.Node == "other" || n.Node == "other2" {
			t.Fatalf("unrelated node %s unexpectedly present in target's closure: %+v", n.Node, closure)
		}
	}

	empty := TransitiveImpact(cat, "other", nil)
	if len(empty) != 0 {
		t.Fatalf("expected other (no inbound refs) to have an empty closure, got %+v", empty)
	}
}

// TestTransitiveImpact_DeterministicOrder checks the exact emitted order
// (depth-ascending, tied-broken by referencer node id — see Neighbors'
// sortNeighborHops) is stable across repeated calls and matches the
// hand-traced BFS order for the fixture's target node: two direct
// depends_on referencers (c1, cy1) and one direct blocks referencer
// (blk1) at depth 1, sorted by node id (blk1 < c1 < cy1), then their own
// first-discovered referencers at depth 2 in the order their parents were
// processed (c2 via c1, cy2 via cy1), then c3 at depth 3.
func TestTransitiveImpact_DeterministicOrder(t *testing.T) {
	cat := loadImpactFixture(t)
	want := []NodeID{"blk1", "c1", "cy1", "c2", "cy2", "c3"}

	for i := 0; i < 5; i++ {
		closure := TransitiveImpact(cat, "target", nil)
		if len(closure) != len(want) {
			t.Fatalf("run %d: len(closure) = %d, want %d: %+v", i, len(closure), len(want), closure)
		}
		for j, n := range closure {
			if n.Node != want[j] {
				t.Fatalf("run %d: closure[%d].Node = %q, want %q (full: %+v)", i, j, n.Node, want[j], closure)
			}
		}
	}
}

func pathEqual(a, b []NodeID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNearestIDs_TypoSuggestsCloseMatch(t *testing.T) {
	cat := loadReadsFixture(t)
	sugg := NearestIDs(cat, "req-alph", 3)
	if len(sugg) == 0 || sugg[0] != "req-alpha" {
		t.Fatalf("expected req-alpha as nearest suggestion for req-alph, got %v", sugg)
	}
}
