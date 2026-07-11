package host

import (
	"context"
	"testing"
)

const readsFixturePath = "testdata/graph-reads-fixture.yaml"

func TestGraphHandler_Get(t *testing.T) {
	res, err := GraphHandler(context.Background(), map[string]any{
		"op":           "get",
		"catalog_path": readsFixturePath,
		"ids":          []any{"req-alpha", "does-not-exist"},
	})
	if err != nil {
		t.Fatalf("GraphHandler(get): %v", err)
	}
	nodes, ok := res.Data["nodes"].([]any)
	if !ok || len(nodes) != 1 {
		t.Fatalf("expected 1 found node, got %v", res.Data["nodes"])
	}
	envelope := nodes[0].(map[string]any)
	if envelope["id"] != "req-alpha" {
		t.Errorf("id = %v, want req-alpha", envelope["id"])
	}
	edges, ok := envelope["edges"].(map[string]any)
	if !ok {
		t.Fatalf("expected edges map, got %T", envelope["edges"])
	}
	acceptance, ok := edges["acceptance"].([]any)
	if !ok || len(acceptance) != 1 || acceptance[0] != "uc-alpha" {
		t.Errorf("edges[acceptance] = %v, want [uc-alpha]", edges["acceptance"])
	}
	refsIn, ok := envelope["refs_in"].([]any)
	if !ok || len(refsIn) == 0 {
		t.Fatalf("expected refs_in to be populated (uc-alpha -covers-> req-alpha), got %v", envelope["refs_in"])
	}

	missing, ok := res.Data["missing"].([]any)
	if !ok || len(missing) != 1 {
		t.Fatalf("expected 1 missing entry, got %v", res.Data["missing"])
	}
	missEntry := missing[0].(map[string]any)
	if missEntry["id"] != "does-not-exist" {
		t.Errorf("missing[0].id = %v, want does-not-exist", missEntry["id"])
	}
	if _, ok := missEntry["suggestions"].([]any); !ok {
		t.Errorf("expected missing[0].suggestions to be present, got %v", missEntry["suggestions"])
	}
}

func TestGraphHandler_Get_RefsInSeesTopLevelStorageEdge(t *testing.T) {
	res, err := GraphHandler(context.Background(), map[string]any{
		"op":           "get",
		"catalog_path": readsFixturePath,
		"ids":          []any{"change-root"},
	})
	if err != nil {
		t.Fatalf("GraphHandler(get): %v", err)
	}
	nodes := res.Data["nodes"].([]any)
	envelope := nodes[0].(map[string]any)
	refsIn := envelope["refs_in"].([]any)
	var found bool
	for _, r := range refsIn {
		ref := r.(map[string]any)
		if ref["node"] == "change-leaf" && ref["edge_field"] == "depends_on" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected refs_in for change-root to include change-leaf via top_level depends_on, got %v", refsIn)
	}
}

func TestGraphHandler_Get_TooManyIDs(t *testing.T) {
	ids := make([]any, 21)
	for i := range ids {
		ids[i] = "req-alpha"
	}
	_, err := GraphHandler(context.Background(), map[string]any{
		"op":           "get",
		"catalog_path": readsFixturePath,
		"ids":          ids,
	})
	if err == nil {
		t.Fatal("expected error for >20 ids")
	}
}

func TestGraphHandler_Find_TypeIsAAware(t *testing.T) {
	res, err := GraphHandler(context.Background(), map[string]any{
		"op":           "find",
		"catalog_path": readsFixturePath,
		"type":         "requirement",
	})
	if err != nil {
		t.Fatalf("GraphHandler(find): %v", err)
	}
	total, _ := res.Data["total"].(int)
	// req-alpha, req-beta, req-iso-gamma (iso-clause extends requirement).
	if total != 3 {
		t.Fatalf("total = %v, want 3 (IsA-aware: iso-clause extends requirement)", res.Data["total"])
	}
	rows := res.Data["rows"].([]any)
	var sawIso bool
	for _, r := range rows {
		row := r.(map[string]any)
		if row["id"] == "req-iso-gamma" {
			sawIso = true
		}
	}
	if !sawIso {
		t.Errorf("expected req-iso-gamma (a requirement subtype) in find(type=requirement) rows, got %v", rows)
	}
}

func TestGraphHandler_Find_NoInboundFindsUnaccepted(t *testing.T) {
	res, err := GraphHandler(context.Background(), map[string]any{
		"op":           "find",
		"catalog_path": readsFixturePath,
		"type":         "requirement",
		"no_inbound":   map[string]any{"edge": "covers"},
	})
	if err != nil {
		t.Fatalf("GraphHandler(find no_inbound): %v", err)
	}
	rows := res.Data["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 requirement with no inbound covers edge, got %v", rows)
	}
	if rows[0].(map[string]any)["id"] != "req-beta" {
		t.Errorf("expected req-beta (uncovered), got %v", rows[0])
	}
}

func TestGraphHandler_Find_NoOutboundOnTopLevelEdge(t *testing.T) {
	res, err := GraphHandler(context.Background(), map[string]any{
		"op":           "find",
		"catalog_path": readsFixturePath,
		"type":         "change",
		"no_outbound":  map[string]any{"edge": "depends_on"},
	})
	if err != nil {
		t.Fatalf("GraphHandler(find no_outbound): %v", err)
	}
	rows := res.Data["rows"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["id"] != "change-root" {
		t.Fatalf("expected only change-root (empty top_level depends_on), got %v", rows)
	}
}

func TestGraphHandler_Find_EdgeFilter(t *testing.T) {
	res, err := GraphHandler(context.Background(), map[string]any{
		"op":           "find",
		"catalog_path": readsFixturePath,
		"edge":         map[string]any{"field": "depends_on", "to": "change-root"},
	})
	if err != nil {
		t.Fatalf("GraphHandler(find edge filter): %v", err)
	}
	rows := res.Data["rows"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["id"] != "change-leaf" {
		t.Fatalf("expected only change-leaf (depends_on -> change-root, top_level storage), got %v", rows)
	}
}

func TestGraphHandler_Find_TextAndCountOnly(t *testing.T) {
	res, err := GraphHandler(context.Background(), map[string]any{
		"op":           "find",
		"catalog_path": readsFixturePath,
		"text":         "alpha",
		"count_only":   true,
	})
	if err != nil {
		t.Fatalf("GraphHandler(find text/count_only): %v", err)
	}
	total, _ := res.Data["total"].(int)
	if total == 0 {
		t.Fatal("expected at least one text match for \"alpha\"")
	}
	rows, ok := res.Data["rows"].([]any)
	if !ok || len(rows) != 0 {
		t.Errorf("count_only should return empty rows, got %v", res.Data["rows"])
	}
}

func TestGraphHandler_Find_LimitOffsetTruncated(t *testing.T) {
	res, err := GraphHandler(context.Background(), map[string]any{
		"op":           "find",
		"catalog_path": readsFixturePath,
		"limit":        1,
		"offset":       0,
	})
	if err != nil {
		t.Fatalf("GraphHandler(find limit): %v", err)
	}
	rows := res.Data["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 row with limit=1, got %d", len(rows))
	}
	if res.Data["truncated"] != true {
		t.Errorf("expected truncated=true with more rows than limit, got %v", res.Data["truncated"])
	}
}

func TestGraphHandler_Find_DeterministicOrdering(t *testing.T) {
	first, err := GraphHandler(context.Background(), map[string]any{
		"op":           "find",
		"catalog_path": readsFixturePath,
	})
	if err != nil {
		t.Fatalf("GraphHandler(find): %v", err)
	}
	second, err := GraphHandler(context.Background(), map[string]any{
		"op":           "find",
		"catalog_path": readsFixturePath,
	})
	if err != nil {
		t.Fatalf("GraphHandler(find): %v", err)
	}
	rowsA := first.Data["rows"].([]any)
	rowsB := second.Data["rows"].([]any)
	if len(rowsA) != len(rowsB) {
		t.Fatalf("row count differs across calls: %d vs %d", len(rowsA), len(rowsB))
	}
	for i := range rowsA {
		idA := rowsA[i].(map[string]any)["id"]
		idB := rowsB[i].(map[string]any)["id"]
		if idA != idB {
			t.Fatalf("row %d id differs across calls: %v vs %v (non-deterministic ordering)", i, idA, idB)
		}
	}
}

func TestGraphHandler_Neighbors(t *testing.T) {
	res, err := GraphHandler(context.Background(), map[string]any{
		"op":           "neighbors",
		"catalog_path": readsFixturePath,
		"id":           "change-root",
		"direction":    "in",
		"edges":        []any{"depends_on"},
	})
	if err != nil {
		t.Fatalf("GraphHandler(neighbors): %v", err)
	}
	triples, ok := res.Data["triples"].([]any)
	if !ok || len(triples) != 1 {
		t.Fatalf("expected 1 inbound triple, got %v", res.Data["triples"])
	}
	tr := triples[0].(map[string]any)
	if tr["from"] != "change-leaf" || tr["to"] != "change-root" {
		t.Errorf("unexpected triple: %v", tr)
	}
	rows, ok := res.Data["rows"].([]any)
	if !ok || len(rows) != 1 || rows[0].(map[string]any)["id"] != "change-leaf" {
		t.Errorf("expected summary rows to include change-leaf, got %v", res.Data["rows"])
	}
}

func TestGraphHandler_Neighbors_UnknownNode(t *testing.T) {
	_, err := GraphHandler(context.Background(), map[string]any{
		"op":           "neighbors",
		"catalog_path": readsFixturePath,
		"id":           "does-not-exist",
	})
	if err == nil {
		t.Fatal("expected error for unknown node")
	}
}

func TestGraphHandler_Neighbors_DepthOutOfRange(t *testing.T) {
	_, err := GraphHandler(context.Background(), map[string]any{
		"op":           "neighbors",
		"catalog_path": readsFixturePath,
		"id":           "req-alpha",
		"depth":        4,
	})
	if err == nil {
		t.Fatal("expected error for depth > 3")
	}
}

func TestGraphHandler_TypeCensus_OneType(t *testing.T) {
	res, err := GraphHandler(context.Background(), map[string]any{
		"op":           "type_census",
		"catalog_path": readsFixturePath,
		"type_id":      "requirement",
	})
	if err != nil {
		t.Fatalf("GraphHandler(type_census): %v", err)
	}
	if res.Data["type_id"] != "requirement" {
		t.Errorf("type_id = %v, want requirement", res.Data["type_id"])
	}
	count, _ := res.Data["instance_count"].(int)
	if count != 3 {
		t.Errorf("instance_count = %v, want 3 (IsA-aware: includes iso-clause)", res.Data["instance_count"])
	}
	breakdown, ok := res.Data["status_breakdown"].(map[string]any)
	if !ok || breakdown["active"] != 3 {
		t.Errorf("status_breakdown = %v, want active:3", breakdown)
	}
}

func TestGraphHandler_TypeCensus_AllTypes(t *testing.T) {
	res, err := GraphHandler(context.Background(), map[string]any{
		"op":           "type_census",
		"catalog_path": readsFixturePath,
	})
	if err != nil {
		t.Fatalf("GraphHandler(type_census all): %v", err)
	}
	types, ok := res.Data["types"].([]any)
	if !ok || len(types) == 0 {
		t.Fatal("expected non-empty types list")
	}
	for _, ty := range types {
		if ty.(map[string]any)["id"] == "core-node" {
			t.Error("core-node should be filtered out of the type census, matching the registry passthrough convention")
		}
	}
}

func TestGraphHandler_Changeset_List(t *testing.T) {
	res, err := GraphHandler(context.Background(), map[string]any{
		"op":           "changeset",
		"catalog_path": readsFixturePath,
		"action":       "list",
	})
	if err != nil {
		t.Fatalf("GraphHandler(changeset list): %v", err)
	}
	changesets, ok := res.Data["changesets"].([]any)
	if !ok || len(changesets) != 2 {
		t.Fatalf("expected 2 changesets, got %v", res.Data["changesets"])
	}
	counts, ok := res.Data["status_counts"].(map[string]any)
	if !ok || counts["proposed"] != 1 || counts["authorized"] != 1 {
		t.Fatalf("unexpected status_counts: %v", counts)
	}
}

func TestGraphHandler_Changeset_Get(t *testing.T) {
	res, err := GraphHandler(context.Background(), map[string]any{
		"op":           "changeset",
		"catalog_path": readsFixturePath,
		"action":       "get",
		"changeset_id": "cs-1",
	})
	if err != nil {
		t.Fatalf("GraphHandler(changeset get): %v", err)
	}
	if res.Data["id"] != "cs-1" {
		t.Errorf("id = %v, want cs-1", res.Data["id"])
	}
	ops, ok := res.Data["operations"].([]any)
	if !ok || len(ops) != 1 {
		t.Fatalf("expected 1 operation, got %v", res.Data["operations"])
	}
	op := ops[0].(map[string]any)
	if op["node"] != "req-beta" || op["kind"] != "modified" {
		t.Errorf("unexpected operation: %v", op)
	}
}

func TestGraphHandler_Changeset_Touching(t *testing.T) {
	res, err := GraphHandler(context.Background(), map[string]any{
		"op":           "changeset",
		"catalog_path": readsFixturePath,
		"action":       "touching",
		"node_id":      "req-alpha",
	})
	if err != nil {
		t.Fatalf("GraphHandler(changeset touching): %v", err)
	}
	touching, ok := res.Data["touching"].([]any)
	if !ok || len(touching) != 1 || touching[0].(map[string]any)["id"] != "cs-2" {
		t.Fatalf("expected exactly cs-2 touching req-alpha, got %v", res.Data["touching"])
	}
}

func TestGraphHandler_Changeset_UnknownAction(t *testing.T) {
	_, err := GraphHandler(context.Background(), map[string]any{
		"op":           "changeset",
		"catalog_path": readsFixturePath,
		"action":       "bogus",
	})
	if err == nil {
		t.Fatal("expected error for unknown changeset action")
	}
}
