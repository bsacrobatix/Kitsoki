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
