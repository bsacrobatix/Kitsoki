package main

// The CLI's share of the canonicalization contract. `kitsoki graph propose`
// against a catalog a human left with a hand-wrapped block scalar must
// succeed and print what it reformatted — the shell surface of the same
// guarantee the MCP and JSON-RPC surfaces make.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/graph"
)

const cliNonCanonicalFixture = `schema: project-object-graph/seed-catalog/v0
catalog:
  id: canon-cli-fixture
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

const cliCanonProposeDoc = `title: proposed against a non-canonical catalog
operations:
  - kind: added
    after:
      schema: graph/requirement/v0
      id: req-cli-canon
      title: Lands despite the block scalar
      status: draft
      visibility: internal
`

func writeCLINonCanonicalFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.yaml")
	if err := os.WriteFile(path, []byte(cliNonCanonicalFixture), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestGraphProposeCmd_AutoHealsNonCanonicalCatalog(t *testing.T) {
	path := writeCLINonCanonicalFixture(t)

	out, err := runGraphProposeCmd(t, cliCanonProposeDoc, path)
	if err != nil {
		t.Fatalf("a non-canonical catalog must not block `graph propose`, got: %v\n%s", err, out)
	}
	if !strings.Contains(out, "canonicalized") {
		t.Errorf("propose must report the reformat it performed, got:\n%s", out)
	}
	if !strings.Contains(out, "appended") {
		t.Errorf("expected the changeset to be appended, got:\n%s", out)
	}

	cat, err := graph.LoadCatalog(path)
	if err != nil {
		t.Fatalf("reload catalog: %v", err)
	}
	if len(changesetNodeIDs(cat)) != 1 {
		t.Fatalf("expected one changeset node after propose, got %v", changesetNodeIDs(cat))
	}
	if _, ok := cat.Nodes["req-block"]; !ok {
		t.Error("pre-existing node lost during the heal")
	}

	// A second propose has nothing left to tidy, and says nothing about it.
	out, err = runGraphProposeCmd(t, strings.Replace(cliCanonProposeDoc, "req-cli-canon", "req-cli-canon-2", 1), path)
	if err != nil {
		t.Fatalf("second propose: %v\n%s", err, out)
	}
	if strings.Contains(out, "canonicalized") {
		t.Errorf("catalog was already healed; second propose must not claim a reformat:\n%s", out)
	}
}

func TestGraphProposeCmd_ValidateOnlyOnNonCanonicalCatalog(t *testing.T) {
	path := writeCLINonCanonicalFixture(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	out, err := runGraphProposeCmd(t, cliCanonProposeDoc, path, "--validate-only")
	if err != nil {
		t.Fatalf("validate-only must never fail on formatting alone, got: %v\n%s", err, out)
	}
	if !strings.Contains(out, "would canonicalize") {
		t.Errorf("validate-only should report the heal a real write would do, got:\n%s", out)
	}
	if !strings.Contains(out, "validate-only clean, nothing written") {
		t.Errorf("expected the validate-only summary, got:\n%s", out)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("validate-only wrote to disk")
	}
}
