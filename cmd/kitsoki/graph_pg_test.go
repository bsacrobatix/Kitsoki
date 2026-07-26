package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"kitsoki/internal/dbruntime/pgtest"
	"kitsoki/internal/graph"
	"kitsoki/internal/graph/pgcatalog"
)

// pogCatalogPathFromCmd is pog/catalog.yaml relative to this package's test
// working directory (cmd/kitsoki), the same real catalog the pgcatalog parity
// tests use.
const pogCatalogPathFromCmd = "../../pog/catalog.yaml"

// TestGraphImportExportRoundTripPog is the round-trip contract for the
// import/export pair on the real pog catalog: import pog/catalog.yaml, export
// it, import the export under a second id, export again — the two exports
// must be byte-identical, and the exported file must load and lint exactly
// like the original.
func TestGraphImportExportRoundTripPog(t *testing.T) {
	db := pgtest.Open(t) // skips when no server can start here
	ctx := context.Background()

	orig, err := graph.LoadCatalog(pogCatalogPathFromCmd)
	if err != nil {
		t.Fatalf("load pog catalog: %v", err)
	}

	res, err := graphPGImport(ctx, db, pogCatalogPathFromCmd, "pog", false)
	if err != nil {
		t.Fatalf("import pog: %v", err)
	}
	if res.Replaced {
		t.Error("fresh import reported Replaced")
	}
	if res.Nodes != len(orig.Nodes) || res.Types != len(orig.Registry.All()) {
		t.Errorf("import counts = %d nodes, %d types; want %d, %d",
			res.Nodes, res.Types, len(orig.Nodes), len(orig.Registry.All()))
	}

	exportA, err := graphPGExportYAML(ctx, db, "pog")
	if err != nil {
		t.Fatalf("export pog: %v", err)
	}
	pathA := filepath.Join(t.TempDir(), "pog-export.yaml")
	if err := os.WriteFile(pathA, exportA, 0o644); err != nil {
		t.Fatalf("write export: %v", err)
	}

	if _, err := graphPGImport(ctx, db, pathA, "pog-roundtrip", false); err != nil {
		t.Fatalf("re-import export: %v", err)
	}
	exportB, err := graphPGExportYAML(ctx, db, "pog-roundtrip")
	if err != nil {
		t.Fatalf("export round-tripped catalog: %v", err)
	}
	if !bytes.Equal(exportA, exportB) {
		t.Errorf("round-trip exports differ:\nfirst %d bytes, second %d bytes", len(exportA), len(exportB))
	}

	// The exported file is a real catalog: LoadCatalog reads it and it lints
	// exactly like the original (node ids, edges, and type structure all
	// survived the pg round trip).
	reloaded, err := graph.LoadCatalog(pathA)
	if err != nil {
		t.Fatalf("LoadCatalog(exported): %v", err)
	}
	if !reflect.DeepEqual(reloaded.SortedNodeIDs(), orig.SortedNodeIDs()) {
		t.Error("exported catalog's node id set differs from the original")
	}
	origIssues := graph.Lint(orig)
	reloadedIssues := graph.Lint(reloaded)
	if !reflect.DeepEqual(origIssues, reloadedIssues) {
		t.Errorf("lint parity broken:\noriginal: %+v\nexported: %+v", origIssues, reloadedIssues)
	}
}

// writeFixtureCatalog writes a tiny valid single-file catalog and returns its
// path — enough structure for import/replace semantics without the pog
// catalog's weight.
func writeFixtureCatalog(t *testing.T) string {
	t.Helper()
	const fixture = `schema: project-object-graph/seed-catalog/v0
type_registry:
  - id: thing
    schema: graph-type/v0
    summary: A test thing.
nodes:
  - schema: project-object-graph/thing/v0
    id: alpha
    title: Alpha
    status: active
    visibility: public
`
	path := filepath.Join(t.TempDir(), "catalog.yaml")
	if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write fixture catalog: %v", err)
	}
	return path
}

// TestGraphImportReplaceSemantics pins the idempotent re-import contract: a
// stored catalog still at rev 1 is silently replaced; one with commits beyond
// the import (rev > 1) is refused without --force and replaced with it.
func TestGraphImportReplaceSemantics(t *testing.T) {
	db := pgtest.Open(t)
	ctx := context.Background()
	path := writeFixtureCatalog(t)

	if _, err := graphPGImport(ctx, db, path, "fixture", false); err != nil {
		t.Fatalf("fresh import: %v", err)
	}

	// Rev 1 re-import: idempotent, no --force needed.
	res, err := graphPGImport(ctx, db, path, "fixture", false)
	if err != nil {
		t.Fatalf("rev-1 re-import must succeed without --force: %v", err)
	}
	if !res.Replaced || res.PrevRev != 1 {
		t.Errorf("rev-1 re-import = {Replaced:%v PrevRev:%d}; want {true 1}", res.Replaced, res.PrevRev)
	}

	// A real commit through the store bumps the catalog to rev 2.
	st, err := pgcatalog.Open(db, "fixture")
	if err != nil {
		t.Fatalf("pgcatalog.Open: %v", err)
	}
	_, rev, err := st.Load(ctx)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	commitRes, err := st.Commit(ctx, rev, []graph.Operation{{
		Kind:    graph.OpModified,
		Node:    "alpha",
		Changes: []graph.FieldChange{{Path: []string{"status"}, After: "done"}},
	}}, graph.CommitOptions{})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if commitRes.Rejected() {
		t.Fatalf("commit rejected: %+v", commitRes)
	}

	// Rev 2: refuse without --force, naming the rev and the escape hatch.
	if _, err := graphPGImport(ctx, db, path, "fixture", false); err == nil {
		t.Fatal("re-import over rev 2 succeeded without --force; want refusal")
	} else if !strings.Contains(err.Error(), "rev 2") || !strings.Contains(err.Error(), "--force") {
		t.Errorf("refusal error %q must name the stale rev and --force", err)
	}

	// --force replaces and resets to rev 1 with the file's contents.
	res, err = graphPGImport(ctx, db, path, "fixture", true)
	if err != nil {
		t.Fatalf("forced re-import: %v", err)
	}
	if !res.Replaced || res.PrevRev != 2 {
		t.Errorf("forced re-import = {Replaced:%v PrevRev:%d}; want {true 2}", res.Replaced, res.PrevRev)
	}
	cat, rev, err := st.Load(ctx)
	if err != nil {
		t.Fatalf("load after forced re-import: %v", err)
	}
	if rev != graph.CatalogRev("1") {
		t.Errorf("rev after forced re-import = %q; want \"1\"", rev)
	}
	if got := cat.Nodes["alpha"].Status; got != "active" {
		t.Errorf("alpha status after forced re-import = %q; want %q (file contents win)", got, "active")
	}
}

// TestGraphImportExportCmd_SQLiteBackendRefused pins the CLI-level contract:
// on the default sqlite backend both commands fail fast with an error that
// names the postgres backends, before touching any catalog file.
func TestGraphImportExportCmd_SQLiteBackendRefused(t *testing.T) {
	resetDBBackend(t)
	t.Setenv(envDBBackend, "")
	dbBackendFlag = ""

	for _, args := range [][]string{
		{"graph", "import", "--catalog", "does-not-exist.yaml", "--catalog-id", "x"},
		{"graph", "export", "--catalog-id", "x"},
	} {
		root := newRootCmd()
		var stdout, stderr bytes.Buffer
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		root.SetArgs(args)
		err := root.Execute()
		if err == nil {
			t.Fatalf("%v on sqlite backend succeeded; want refusal", args)
		}
		if !strings.Contains(err.Error(), "postgres") || !strings.Contains(err.Error(), "--db-backend") {
			t.Errorf("%v error %q must point at --db-backend postgres", args, err)
		}
	}
}

// TestRenderCatalogExportYAML_DeterministicAndLoadable exercises the renderer
// without a database: the same in-memory catalog renders byte-identically
// twice, and the output loads back through graph.LoadCatalog.
func TestRenderCatalogExportYAML_DeterministicAndLoadable(t *testing.T) {
	cat, err := graph.LoadCatalog(pogCatalogPathFromCmd)
	if err != nil {
		t.Fatalf("load pog catalog: %v", err)
	}
	first, err := renderCatalogExportYAML(cat)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	second, err := renderCatalogExportYAML(cat)
	if err != nil {
		t.Fatalf("render again: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Error("two renders of the same catalog differ")
	}
	path := filepath.Join(t.TempDir(), "export.yaml")
	if err := os.WriteFile(path, first, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	reloaded, err := graph.LoadCatalog(path)
	if err != nil {
		t.Fatalf("LoadCatalog(rendered): %v", err)
	}
	if !reflect.DeepEqual(reloaded.SortedNodeIDs(), cat.SortedNodeIDs()) {
		t.Error("rendered catalog's node id set differs")
	}
}
