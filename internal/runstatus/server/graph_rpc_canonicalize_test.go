package server

import (
	"os"
	"path/filepath"
	"testing"
)

// nonCanonicalFixture mirrors internal/graph's blockScalarFixture: a
// single-file catalog whose hand-wrapped folded block scalar used to make
// every lifecycle verb reject until someone canonicalized it out-of-band.
const nonCanonicalFixture = `schema: project-object-graph/seed-catalog/v0
catalog:
  id: canon-rpc-fixture
type_registry:
  - id: core-node
    schema: graph-type/v0
    required_fields: [id, schema, title, status, visibility]
  - id: requirement
    schema: graph-type/v0
    extends: core-node
  - id: changeset
    schema: graph-type/v0
    extends: core-node
nodes:
  - schema: graph/requirement/v0
    id: req-block
    title: Requirement with a block scalar
    status: draft
    visibility: internal
    statement: >-
      This is a folded block scalar that has been hand-wrapped at a
      narrow width by a human editor, spanning several short lines
      instead of one long line, to keep diffs readable in review —
      yaml.v3's re-marshal collapses this onto one line, the exact
      reflow hazard the canonicality guard exists to catch.
`

func writeNonCanonicalFixture(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "catalog.yaml")
	if err := os.WriteFile(dst, []byte(nonCanonicalFixture), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return dst
}

func proposeRPC(t *testing.T, s *Server, root, id string) map[string]any {
	t.Helper()
	result, rerr := s.graphProposeRPC(map[string]any{
		"catalog_path": root,
		"title":        "write against a non-canonical catalog",
		"operations": []any{
			map[string]any{
				"kind":  "added",
				"after": map[string]any{"schema": "graph/requirement/v0", "id": id, "title": "Lands", "status": "draft", "visibility": "internal"},
			},
		},
	})
	if rerr != nil {
		t.Fatalf("graphProposeRPC: %+v", rerr)
	}
	return result.(map[string]any)
}

// TestGraphRPC_ProposeAutoHealsNonCanonicalCatalog: the portal's write path
// must not be freezable by a hand-edited catalog either. The propose lands
// and reports the heal; the portal no longer needs a fix-it button standing
// between a user and their own write.
func TestGraphRPC_ProposeAutoHealsNonCanonicalCatalog(t *testing.T) {
	root := writeNonCanonicalFixture(t)
	s := &Server{}

	m := proposeRPC(t, s, root, "req-rpc-heals")
	if rejected, _ := m["rejected"].(bool); rejected {
		t.Fatalf("a non-canonical catalog must not block a propose: %#v", m)
	}
	if c, _ := m["canonicalized"].(bool); !c {
		t.Errorf("expected canonicalized:true, got: %#v", m)
	}
	if files, _ := m["canonicalized_files"].([]any); len(files) != 1 {
		t.Errorf("expected one canonicalized file, got: %#v", m["canonicalized_files"])
	}

	// Healed once; the next write has nothing to tidy.
	m = proposeRPC(t, s, root, "req-rpc-second")
	if rejected, _ := m["rejected"].(bool); rejected {
		t.Fatalf("second propose rejected: %#v", m)
	}
	if c, _ := m["canonicalized"].(bool); c {
		t.Errorf("second propose must not claim a reformat: %#v", m)
	}
}

// TestGraphRPC_CanonicalizeVerb keeps the explicit verb honest: it heals the
// catalog, and a second call reports already_canonical without touching
// bytes (the writer-format stability the portal's pinned binaries rely on).
func TestGraphRPC_CanonicalizeVerb(t *testing.T) {
	root := writeNonCanonicalFixture(t)
	s := &Server{}

	result, rerr := s.graphCanonicalizeRPC(map[string]any{"catalog_path": root})
	if rerr != nil {
		t.Fatalf("graphCanonicalizeRPC: %+v", rerr)
	}
	cm := result.(map[string]any)
	if changed, _ := cm["changed_files"].([]any); len(changed) != 1 {
		t.Fatalf("expected one changed file, got: %#v", cm)
	}
	healed, err := os.ReadFile(root)
	if err != nil {
		t.Fatal(err)
	}

	result, rerr = s.graphCanonicalizeRPC(map[string]any{"catalog_path": root})
	if rerr != nil {
		t.Fatalf("second graphCanonicalizeRPC: %+v", rerr)
	}
	if already, _ := result.(map[string]any)["already_canonical"].(bool); !already {
		t.Errorf("second canonicalize should report already_canonical, got: %#v", result)
	}
	stable, err := os.ReadFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if string(healed) != string(stable) {
		t.Error("re-canonicalizing a canonical catalog changed its bytes")
	}
}

// TestGraphRPC_RejectDetailsStillClassify: reject_details is the portal's
// structured reject channel, and it must keep classifying the reasons that
// can still occur. Canonicality no longer produces one from a merely
// unformatted file, so this exercises the classifier directly rather than
// pretending a formatting reject is still reachable.
func TestGraphRPC_RejectDetailsStillClassify(t *testing.T) {
	root := writeNonCanonicalFixture(t)
	s := &Server{}

	// A stale `before` guard is a genuine, still-reachable rejection.
	result, rerr := s.graphProposeRPC(map[string]any{
		"catalog_path": root,
		"title":        "stale guard",
		"operations": []any{
			map[string]any{
				"kind": "modified",
				"node": "req-block",
				"changes": []any{
					map[string]any{"path": []any{"status"}, "before": "published", "after": "active"},
				},
			},
		},
	})
	if rerr != nil {
		t.Fatalf("graphProposeRPC: %+v", rerr)
	}
	m := result.(map[string]any)
	if rejected, _ := m["rejected"].(bool); !rejected {
		t.Fatalf("expected a stale-guard rejection, got: %#v", m)
	}
	details, _ := m["reject_details"].([]any)
	if len(details) == 0 {
		t.Fatalf("expected reject_details alongside reject_reasons, got: %#v", m)
	}
	if code, _ := details[0].(map[string]any)["code"].(string); code == "" {
		t.Errorf("every reject detail must carry a code, got: %#v", details[0])
	}
}
