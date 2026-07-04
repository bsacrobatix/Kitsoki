package featuresadapter

import (
	"os"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"

	"kitsoki/internal/graph"
)

const seedCatalogPath = "../../../docs/proposals/project-object-graph/seed-objects.yaml"

func TestPublicSitePages_IncludesOperatorAskPilot(t *testing.T) {
	cat, err := graph.LoadCatalog(seedCatalogPath)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	pages := PublicSitePages(cat)
	if len(pages) == 0 {
		t.Fatal("expected at least one public site-page")
	}
	found := false
	for _, p := range pages {
		if p.ID == "sitepage-feature-operator-ask" {
			found = true
		}
		if p.Visibility != graph.VisibilityPublic {
			t.Errorf("PublicSitePages returned a non-public node %q", p.ID)
		}
	}
	if !found {
		t.Error("expected sitepage-feature-operator-ask among the public site-pages")
	}
}

// TestBuildFeatureDoc_MatchesCommittedOperatorAskFeature is W3.0's pilot
// acceptance check: the adapter, built purely from the graph catalog, must
// reconstruct the same feature content as the hand-authored
// features/operator-ask.yaml. This is a structural/semantic equality check
// (decode both sides into the same struct, compare), not a byte-diff —
// YAML formatting (quoting, key order) is not what "emits today's shape"
// is actually asserting; the content and schema shape are.
func TestBuildFeatureDoc_MatchesCommittedOperatorAskFeature(t *testing.T) {
	cat, err := graph.LoadCatalog(seedCatalogPath)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	sitePage, ok := cat.Nodes["sitepage-feature-operator-ask"]
	if !ok {
		t.Fatal("sitepage-feature-operator-ask not found in the seed catalog")
	}

	got, err := BuildFeatureDoc(cat, sitePage)
	if err != nil {
		t.Fatalf("BuildFeatureDoc: %v", err)
	}

	raw, err := os.ReadFile("../../../features/operator-ask.yaml")
	if err != nil {
		t.Fatalf("read committed feature file: %v", err)
	}
	var want FeatureDoc
	if err := yaml.Unmarshal(raw, &want); err != nil {
		t.Fatalf("parse committed feature file: %v", err)
	}

	if !reflect.DeepEqual(got, &want) {
		t.Errorf("adapter output does not match features/operator-ask.yaml.\ngot:  %+v\nwant: %+v", got, &want)
	}
}

func TestBuildFeatureDoc_ErrorsOnMissingPresentsEdge(t *testing.T) {
	cat, err := graph.LoadCatalog(seedCatalogPath)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	// evidence-operator-ask-tour is a real node but not a site-page, so it
	// has no `presents` edge — exercises the join-failure path.
	notASitePage := cat.Nodes["evidence-operator-ask-tour"]
	if notASitePage == nil {
		t.Fatal("fixture assumption broken: evidence-operator-ask-tour missing")
	}
	if _, err := BuildFeatureDoc(cat, notASitePage); err == nil {
		t.Fatal("expected an error joining a node with no presents edge")
	}
}
