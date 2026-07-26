package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const graphSnapshotFixture = "../graph/testdata/good/minimal.yaml"

func TestGraphSnapshotPublicIsFilteredBoundedAndDeterministic(t *testing.T) {
	args := map[string]any{
		"op":           "snapshot",
		"catalog_path": graphSnapshotFixture,
		"audience":     "public",
		"fields":       []any{"visibility", "statement", "title", "status"},
		"max_nodes":    2,
	}

	first, err := GraphHandler(context.Background(), args)
	if err != nil {
		t.Fatalf("GraphHandler(snapshot): %v", err)
	}
	second, err := GraphHandler(context.Background(), args)
	if err != nil {
		t.Fatalf("GraphHandler(snapshot) second call: %v", err)
	}
	firstJSON, err := json.Marshal(first.Data)
	if err != nil {
		t.Fatalf("marshal first snapshot: %v", err)
	}
	secondJSON, err := json.Marshal(second.Data)
	if err != nil {
		t.Fatalf("marshal second snapshot: %v", err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("snapshot bytes differ across repeated loads:\nfirst:  %s\nsecond: %s", firstJSON, secondJSON)
	}

	snapshot := first.Data["snapshot"].(map[string]any)
	if got := snapshot["schema"]; got != "kitsoki/graph-snapshot/v1" {
		t.Fatalf("schema = %v", got)
	}
	if got := snapshot["audience"]; got != "public" {
		t.Fatalf("audience = %v", got)
	}
	if digest, _ := snapshot["catalog_digest"].(string); digest == "" {
		t.Fatal("catalog_digest is empty")
	}

	nodes := snapshot["nodes"].([]any)
	if len(nodes) != 2 {
		t.Fatalf("public nodes = %d, want 2", len(nodes))
	}
	wantIDs := []string{"feature-one", "req-one"}
	for i, raw := range nodes {
		node := raw.(map[string]any)
		if node["id"] != wantIDs[i] {
			t.Fatalf("nodes[%d].id = %v, want %q", i, node["id"], wantIDs[i])
		}
		if node["visibility"] != "public" {
			t.Fatalf("nodes[%d].visibility = %v", i, node["visibility"])
		}
		if _, leaked := node["sources"]; leaked {
			t.Fatalf("nodes[%d] leaked sources: %v", i, node)
		}
		if _, leaked := node["clause_ref"]; leaked {
			t.Fatalf("nodes[%d] leaked undeclared field clause_ref: %v", i, node)
		}
	}

	edges := snapshot["edges"].([]any)
	if len(edges) != 2 {
		t.Fatalf("public edges = %d, want 2 reciprocal public edges: %v", len(edges), edges)
	}
	for _, raw := range edges {
		edge := raw.(map[string]string)
		if edge["source"] == "clause-one" || edge["target"] == "clause-one" {
			t.Fatalf("public edge leaked internal endpoint: %v", edge)
		}
	}
}

func TestGraphSnapshotInternalIncludesAllowedScalarOnly(t *testing.T) {
	result, err := GraphHandler(context.Background(), map[string]any{
		"op":           "snapshot",
		"catalog_path": graphSnapshotFixture,
		"audience":     "internal",
		"fields":       []string{"clause_ref"},
		"max_nodes":    3,
	})
	if err != nil {
		t.Fatalf("GraphHandler(snapshot): %v", err)
	}
	nodes := result.Data["snapshot"].(map[string]any)["nodes"].([]any)
	if len(nodes) != 3 {
		t.Fatalf("internal nodes = %d, want 3", len(nodes))
	}
	clause := nodes[0].(map[string]any)
	if clause["id"] != "clause-one" || clause["clause_ref"] != "8.2.1" {
		t.Fatalf("internal clause projection = %v", clause)
	}
	if _, leaked := clause["title"]; leaked {
		t.Fatalf("undeclared title leaked: %v", clause)
	}
}

func TestGraphSnapshotFailsInsteadOfTruncating(t *testing.T) {
	_, err := GraphHandler(context.Background(), map[string]any{
		"op":           "snapshot",
		"catalog_path": graphSnapshotFixture,
		"audience":     "internal",
		"fields":       []string{"title"},
		"max_nodes":    2,
	})
	if err == nil || !strings.Contains(err.Error(), "refusing to truncate") {
		t.Fatalf("error = %v, want refusing-to-truncate failure", err)
	}
}

func TestGraphSnapshotRejectsUnsafeOrInvalidArguments(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "unknown audience",
			args: map[string]any{"audience": "partner", "fields": []string{}, "max_nodes": 3},
			want: "audience",
		},
		{
			name: "missing bound",
			args: map[string]any{"audience": "public", "fields": []string{}},
			want: "max_nodes",
		},
		{
			name: "source provenance",
			args: map[string]any{"audience": "public", "fields": []string{"sources"}, "max_nodes": 3},
			want: "structural",
		},
		{
			name: "duplicate field",
			args: map[string]any{"audience": "public", "fields": []string{"title", "title"}, "max_nodes": 3},
			want: "duplicated",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.args["op"] = "snapshot"
			test.args["catalog_path"] = graphSnapshotFixture
			_, err := GraphHandler(context.Background(), test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}
