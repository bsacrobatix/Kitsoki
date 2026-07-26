package graphsrv_test

// The MCP surface's half of the canonicalization contract: a catalog a human
// left non-canonical must not block graph.propose (or any other write tool),
// and the heal a write performs must be visible in the tool result. Plus
// graph.canonicalize, the explicit verb agents get so they can land the
// reflow as its own change rather than folding it into a content proposal.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/mcp/graphsrv"
)

// nonCanonicalFixture is a single-file catalog carrying a hand-wrapped
// folded block scalar — the exact shape that used to make every write tool
// return NEEDS_CANONICALIZATION until a human ran the CLI canonicalizer.
const nonCanonicalFixture = `schema: project-object-graph/seed-catalog/v0
catalog:
  id: canon-mcp-fixture
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
      reflow hazard the canonicality machinery exists to handle.
`

func writeNonCanonicalFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.yaml")
	if err := os.WriteFile(path, []byte(nonCanonicalFixture), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func addReqOp(id, title string) map[string]any {
	return map[string]any{
		"kind":  "added",
		"after": map[string]any{"schema": "graph/requirement/v0", "id": id, "title": title, "status": "draft", "visibility": "internal"},
	}
}

// TestGraphServer_ProposeAutoHealsNonCanonicalCatalog: the POG incident,
// through MCP. The propose must succeed and say that it canonicalized.
func TestGraphServer_ProposeAutoHealsNonCanonicalCatalog(t *testing.T) {
	path := writeNonCanonicalFixture(t)
	cs, done := connectGraphServer(t, graphsrv.Config{CatalogFlags: []string{path}, Mode: graphsrv.ModePropose})
	defer done()

	m, isErr := callTool(t, cs, "graph.propose", proposeArgs("lands despite the block scalar", []map[string]any{
		addReqOp("req-mcp-heals", "Lands"),
	}))
	if isErr {
		t.Fatalf("graph.propose must not be blocked by a non-canonical catalog: %+v", m)
	}
	if id, _ := m["changeset_id"].(string); id == "" {
		t.Fatalf("expected a changeset_id, got %+v", m)
	}
	if c, _ := m["canonicalized"].(bool); !c {
		t.Errorf("expected canonicalized:true in the tool result, got %+v", m)
	}
	files, _ := m["canonicalized_files"].([]any)
	if len(files) != 1 {
		t.Errorf("expected one canonicalized file, got %+v", m["canonicalized_files"])
	}

	// A second propose has nothing left to heal.
	m2, isErr := callTool(t, cs, "graph.propose", proposeArgs("second write", []map[string]any{
		addReqOp("req-mcp-second", "Also lands"),
	}))
	if isErr {
		t.Fatalf("second graph.propose: %+v", m2)
	}
	if c, _ := m2["canonicalized"].(bool); c {
		t.Errorf("catalog was already healed; second propose must not claim a reformat: %+v", m2)
	}
}

// TestGraphServer_ProposeValidateOnlyOnNonCanonicalCatalog: validate_only
// against a non-canonical catalog is a check, not a write, and it must
// answer the check rather than refuse it.
func TestGraphServer_ProposeValidateOnlyOnNonCanonicalCatalog(t *testing.T) {
	path := writeNonCanonicalFixture(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cs, done := connectGraphServer(t, graphsrv.Config{CatalogFlags: []string{path}, Mode: graphsrv.ModePropose})
	defer done()

	args := proposeArgs("validate against a non-canonical catalog", []map[string]any{
		addReqOp("req-mcp-validated", "Never written"),
	})
	args["validate_only"] = true
	m, isErr := callTool(t, cs, "graph.propose", args)
	if isErr {
		t.Fatalf("validate_only must never reject on formatting alone: %+v", m)
	}
	if v, _ := m["validated_only"].(bool); !v {
		t.Errorf("expected validated_only:true, got %+v", m)
	}
	if c, _ := m["canonicalized"].(bool); !c {
		t.Errorf("validate_only should report the heal the real write would do, got %+v", m)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("validate_only wrote to disk")
	}
}

// TestGraphServer_CanonicalizeTool covers the explicit verb: it heals, it
// reports the file, and a second call is a no-op reporting already_canonical.
func TestGraphServer_CanonicalizeTool(t *testing.T) {
	path := writeNonCanonicalFixture(t)
	cs, done := connectGraphServer(t, graphsrv.Config{CatalogFlags: []string{path}, Mode: graphsrv.ModeSteward})
	defer done()

	// Dry run first: reports without touching disk.
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m, isErr := callTool(t, cs, "graph.canonicalize", map[string]any{"dry_run": true})
	if isErr {
		t.Fatalf("graph.canonicalize dry_run: %+v", m)
	}
	if files, _ := m["changed_files"].([]any); len(files) != 1 {
		t.Errorf("dry run should name the file it would rewrite, got %+v", m)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("dry run touched disk")
	}

	// Real run.
	m, isErr = callTool(t, cs, "graph.canonicalize", map[string]any{})
	if isErr {
		t.Fatalf("graph.canonicalize: %+v", m)
	}
	if files, _ := m["changed_files"].([]any); len(files) != 1 {
		t.Fatalf("expected exactly one rewritten file, got %+v", m)
	}
	if already, _ := m["already_canonical"].(bool); already {
		t.Error("already_canonical must be false on a run that rewrote a file")
	}

	// Second run: nothing to do. This is also the byte-stability check on
	// the MCP path — an already-canonical catalog is never rewritten, so a
	// pinned downstream binary's writer format cannot drift underneath it.
	healed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m, isErr = callTool(t, cs, "graph.canonicalize", map[string]any{})
	if isErr {
		t.Fatalf("second graph.canonicalize: %+v", m)
	}
	if already, _ := m["already_canonical"].(bool); !already {
		t.Errorf("second run should report already_canonical, got %+v", m)
	}
	if files, _ := m["changed_files"].([]any); len(files) != 0 {
		t.Errorf("second run must rewrite nothing, got %+v", m)
	}
	stable, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(healed) != string(stable) {
		t.Error("re-canonicalizing a canonical catalog changed its bytes")
	}
}

// TestGraphServer_CanonicalizeToolIsAvailableInProposeMode: the whole point
// of shipping this on MCP is that an agent, not only a human at a shell, can
// perform the explicit heal.
func TestGraphServer_CanonicalizeToolIsAvailableInProposeMode(t *testing.T) {
	path := writeNonCanonicalFixture(t)
	cs, done := connectGraphServer(t, graphsrv.Config{CatalogFlags: []string{path}, Mode: graphsrv.ModePropose})
	defer done()

	m, isErr := callTool(t, cs, "graph.canonicalize", map[string]any{})
	if isErr {
		t.Fatalf("graph.canonicalize should be callable in propose mode: %+v", m)
	}
	files, _ := m["changed_files"].([]any)
	if len(files) != 1 {
		t.Fatalf("expected the fixture to be rewritten, got %+v", m)
	}
	if name, _ := files[0].(string); !strings.HasSuffix(name, "catalog.yaml") {
		t.Errorf("changed_files should name the catalog file, got %v", files)
	}
}
