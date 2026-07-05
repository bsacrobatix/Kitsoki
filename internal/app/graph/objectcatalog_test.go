package graph

import (
	"testing"

	objectgraph "kitsoki/internal/graph"
)

// TestObjectCatalogGraph_NestsUnderThreadsToWireEdge is the W6.2 follow-up's
// wire-level regression check: an edge field the type registry marks
// nests_under: true (e.g. proposal.child_of) must carry attrs.nests_under
// on the wire GraphEdge, so a generic UI list projection (CatalogPanel.vue)
// can derive its nesting table from the served graph instead of a
// hand-maintained kind->edge map.
func TestObjectCatalogGraph_NestsUnderThreadsToWireEdge(t *testing.T) {
	cat, err := objectgraph.LoadCatalog("../../../docs/proposals/project-object-graph/seed-objects.yaml")
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	g := ObjectCatalogGraph(cat, "test")

	var found bool
	for _, e := range g.Edges {
		if e.Kind != "child_of" {
			continue
		}
		found = true
		if e.Attrs["nests_under"] != true {
			t.Errorf("child_of edge %s: attrs[nests_under] = %v, want true", e.ID, e.Attrs["nests_under"])
		}
	}
	if !found {
		t.Fatal("expected at least one child_of edge in the wire graph")
	}

	// A non-nesting edge (e.g. proposes) must not carry the marker at all.
	for _, e := range g.Edges {
		if e.Kind != "proposes" {
			continue
		}
		if e.Attrs["nests_under"] == true {
			t.Errorf("proposes edge %s unexpectedly carries attrs[nests_under]=true", e.ID)
		}
	}
}

// TestObjectCatalogDiffGraph_BadgesTheOverlayAddition exercises diff mode
// end to end against the real seed catalog + its ui-declutter overlay
// (docs/proposals/project-object-graph/seed-objects.overlay-ui-declutter.yaml):
// every node keeps desired's data, gets a diff_kind attr, and the one node
// the overlay actually adds is badged "added" — everything else "unchanged".
func TestObjectCatalogDiffGraph_BadgesTheOverlayAddition(t *testing.T) {
	const base = "../../../docs/proposals/project-object-graph/seed-objects.yaml"
	const overlay = "../../../docs/proposals/project-object-graph/seed-objects.overlay-ui-declutter.yaml"

	current, err := objectgraph.LoadCatalog(base)
	if err != nil {
		t.Fatalf("LoadCatalog(base): %v", err)
	}
	desired, err := objectgraph.LoadCatalogWithOverlay(base, overlay)
	if err != nil {
		t.Fatalf("LoadCatalogWithOverlay: %v", err)
	}

	g := ObjectCatalogDiffGraph(current, desired, "test-diff")
	if g.Kind != "object-graph-diff" {
		t.Errorf("g.Kind = %q, want object-graph-diff", g.Kind)
	}
	if len(g.Nodes) != len(desired.Nodes) {
		t.Fatalf("diff graph has %d nodes, want %d (no removed nodes in this fixture pair)", len(g.Nodes), len(desired.Nodes))
	}

	var added, unchanged int
	for _, n := range g.Nodes {
		switch n.Attrs["diff_kind"] {
		case "added":
			added++
			if n.ID != "evidence-object-graph-ui-persona-review" {
				t.Errorf("unexpected added node %q", n.ID)
			}
		case "unchanged":
			unchanged++
		default:
			t.Errorf("node %q has unexpected diff_kind %v", n.ID, n.Attrs["diff_kind"])
		}
	}
	if added != 1 {
		t.Errorf("added = %d, want exactly 1", added)
	}
	if unchanged != len(current.Nodes) {
		t.Errorf("unchanged = %d, want %d (every base node)", unchanged, len(current.Nodes))
	}
}
