package graph

import (
	"encoding/json"
	"reflect"
	"testing"
)

// canonJSON renders a value as canonical JSON (map keys sorted by
// encoding/json) so semantically equal values compare equal regardless of
// int-vs-float or map ordering differences.
func canonJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %v: %v", v, err)
	}
	return string(raw)
}

// assertCatalogsEquivalent compares two catalogs semantically: same nodes
// (envelope, sources, fields, and every declared edge read via EdgeTargets —
// which covers storage:top_level), same resolved types, same lint results.
func assertCatalogsEquivalent(t *testing.T, want, got *Catalog) {
	t.Helper()
	if want.Schema != got.Schema {
		t.Errorf("schema: want %q, got %q", want.Schema, got.Schema)
	}
	if len(want.Nodes) != len(got.Nodes) {
		t.Errorf("node count: want %d, got %d", len(want.Nodes), len(got.Nodes))
	}
	for _, id := range want.SortedNodeIDs() {
		wn := want.Nodes[id]
		gn, ok := got.Nodes[id]
		if !ok {
			t.Errorf("node %q missing", id)
			continue
		}
		if wn.Schema != gn.Schema || wn.Title != gn.Title || wn.Status != gn.Status ||
			wn.Visibility != gn.Visibility || wn.TypeID != gn.TypeID {
			t.Errorf("node %q envelope mismatch: want %+v, got %+v", id, wn, gn)
		}
		if canonJSON(t, wn.Sources) != canonJSON(t, gn.Sources) {
			t.Errorf("node %q sources: want %v, got %v", id, wn.Sources, gn.Sources)
		}
		weff, ok := want.Registry.Effective(wn.TypeID)
		if !ok {
			t.Fatalf("node %q: no effective type %q in want catalog", id, wn.TypeID)
		}
		for _, decl := range weff.EdgeFields {
			wt, gt := wn.EdgeTargets(decl), gn.EdgeTargets(decl)
			if canonJSON(t, wt) != canonJSON(t, gt) {
				t.Errorf("node %q edge %q: want %v, got %v", id, decl.ID, wt, gt)
			}
		}
		// Undeclared edges-map entries must survive too.
		for field, targets := range wn.Edges {
			if canonJSON(t, targets) != canonJSON(t, gn.Edges[field]) {
				t.Errorf("node %q raw edge %q: want %v, got %v", id, field, targets, gn.Edges[field])
			}
		}
		if canonJSON(t, wn.Fields) != canonJSON(t, gn.Fields) {
			t.Errorf("node %q fields: want %s, got %s", id, canonJSON(t, wn.Fields), canonJSON(t, gn.Fields))
		}
	}
	wantDefs, gotDefs := want.Registry.All(), got.Registry.All()
	if len(wantDefs) != len(gotDefs) {
		t.Errorf("type count: want %d, got %d", len(wantDefs), len(gotDefs))
	}
	for i := range wantDefs {
		ww, err := TypeDefToWire(wantDefs[i])
		if err != nil {
			t.Fatalf("TypeDefToWire(want %s): %v", wantDefs[i].ID, err)
		}
		gw, err := TypeDefToWire(gotDefs[i])
		if err != nil {
			t.Fatalf("TypeDefToWire(got %s): %v", gotDefs[i].ID, err)
		}
		if canonJSON(t, ww) != canonJSON(t, gw) {
			t.Errorf("type %q: want %s, got %s", wantDefs[i].ID, canonJSON(t, ww), canonJSON(t, gw))
		}
	}
	if !reflect.DeepEqual(Lint(want), Lint(got)) {
		t.Errorf("lint mismatch:\nwant %v\ngot  %v", Lint(want), Lint(got))
	}
}

func TestExportDataRoundTrip_Minimal(t *testing.T) {
	cat, err := LoadCatalog("testdata/good/minimal.yaml")
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	data, err := cat.ExportData()
	if err != nil {
		t.Fatalf("ExportData: %v", err)
	}
	rebuilt, err := BuildCatalogFromData("mem:minimal", data)
	if err != nil {
		t.Fatalf("BuildCatalogFromData: %v", err)
	}
	if rebuilt.RootPath != "mem:minimal" {
		t.Errorf("RootPath = %q, want mem:minimal", rebuilt.RootPath)
	}
	if rebuilt.ContentDigest != "" {
		t.Errorf("ContentDigest = %q, want empty for a data-built catalog", rebuilt.ContentDigest)
	}
	assertCatalogsEquivalent(t, cat, rebuilt)
}

func TestExportDataRoundTrip_TopLevelEdgesAndLintParity(t *testing.T) {
	// The cycle fixture uses a storage:top_level acyclic edge AND carries
	// error-severity lint — the round trip must preserve both: depends_on
	// stays inline in Fields, and Lint reports the identical issue set.
	cat, err := LoadCatalog("testdata/lint/cycle.yaml")
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	if len(ErrorIssues(Lint(cat))) == 0 {
		t.Fatal("fixture should carry error-severity lint (cycle)")
	}
	data, err := cat.ExportData()
	if err != nil {
		t.Fatalf("ExportData: %v", err)
	}
	rebuilt, err := BuildCatalogFromData("mem:cycle", data)
	if err != nil {
		t.Fatalf("BuildCatalogFromData: %v", err)
	}
	node := rebuilt.Nodes["change-a"]
	if node == nil {
		t.Fatal("change-a missing after round trip")
	}
	if _, ok := node.Fields["depends_on"]; !ok {
		t.Error("top_level edge depends_on should live inline in Fields after round trip")
	}
	if len(node.Edges) != 0 {
		t.Errorf("top_level edge must not leak into Edges map, got %v", node.Edges)
	}
	assertCatalogsEquivalent(t, cat, rebuilt)
}

// TestApplyOperationsToData_MatchesFileCommit runs the same operation batch
// through the file pipeline (FileCatalogStore -> commitScratchOperations ->
// yaml.Node rewrites) and through the in-memory wire application, then
// asserts the two resulting catalogs are semantically identical.
func TestApplyOperationsToData_MatchesFileCommit(t *testing.T) {
	root := copySingleFileFixture(t)
	before, err := LoadCatalog(root)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	data, err := before.ExportData()
	if err != nil {
		t.Fatalf("ExportData: %v", err)
	}

	ops := []Operation{
		{Kind: OpAdded, Node: "req-extra", After: map[string]any{
			"schema": "graph/requirement/v0", "id": "req-extra", "title": "Extra requirement",
			"status": "draft", "visibility": "internal", "statement": "added via ops",
		}},
		{Kind: OpModified, Node: "req-one", Changes: []FieldChange{
			{Path: []string{"status"}, After: "done"},
			{Path: []string{"fields", "statement"}, After: "rewritten statement"},
			{Path: []string{"edges", "required_by"}, After: []any{"feature-one"}},
		}},
		{Kind: OpRemoved, Node: "clause-one"},
		{Kind: OpRenamed, From: "feature-one", To: "feature-uno"},
	}

	store := NewFileCatalogStore(root)
	_, rev, err := store.Load(t.Context())
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	res, err := store.Commit(t.Context(), rev, ops, CommitOptions{})
	if err != nil {
		t.Fatalf("file Commit: %v", err)
	}
	if res.Rejected() {
		t.Fatalf("file commit rejected: %+v", res)
	}
	fileCat, err := LoadCatalog(root)
	if err != nil {
		t.Fatalf("reload after commit: %v", err)
	}

	candidate, rejects := ApplyOperationsToData(data, ops)
	if len(rejects) > 0 {
		t.Fatalf("ApplyOperationsToData rejected: %v", rejects)
	}
	memCat, err := BuildCatalogFromData("mem:apply", candidate)
	if err != nil {
		t.Fatalf("BuildCatalogFromData: %v", err)
	}
	assertCatalogsEquivalent(t, fileCat, memCat)

	// The input data must not have been mutated by the application.
	if _, _, found := findWireByID(data.Nodes, "req-extra"); found {
		t.Error("ApplyOperationsToData mutated its input (req-extra present)")
	}
	if _, _, found := findWireByID(data.Nodes, "feature-one"); !found {
		t.Error("ApplyOperationsToData mutated its input (feature-one renamed)")
	}
}

func TestApplyOperationsToData_UnappliableOpIsRejectReason(t *testing.T) {
	cat, err := LoadCatalog("testdata/good/minimal.yaml")
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	data, err := cat.ExportData()
	if err != nil {
		t.Fatalf("ExportData: %v", err)
	}
	_, rejects := ApplyOperationsToData(data, []Operation{{
		Kind: OpModified, Node: "no-such-node",
		Changes: []FieldChange{{Path: []string{"status"}, After: "done"}},
	}})
	if len(rejects) == 0 {
		t.Fatal("expected reject reasons for an unappliable op")
	}
}
