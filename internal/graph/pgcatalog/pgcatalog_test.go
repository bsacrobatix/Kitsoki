// Postgres-backend tests for the graph CatalogStore. Uses the embedded
// server via internal/dbruntime/pgtest; skips (never fails) when no Postgres
// can be started in this environment. The YAML catalog stays the reference:
// every test's acceptance bar is behavioral identity with a LoadCatalog /
// FileCatalogStore run over the same fixture.
package pgcatalog

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"kitsoki/internal/dbruntime/pgtest"
	"kitsoki/internal/graph"
)

const pogCatalogPath = "../../../pog/catalog.yaml"

func loadYAML(t *testing.T, path string) *graph.Catalog {
	t.Helper()
	cat, err := graph.LoadCatalog(path)
	if err != nil {
		t.Fatalf("LoadCatalog(%s): %v", path, err)
	}
	return cat
}

// importStore imports cat into a fresh per-test database and opens its store.
func importStore(t *testing.T, cat *graph.Catalog, catalogID string) (*sql.DB, *Store) {
	t.Helper()
	db := pgtest.Open(t)
	if err := ImportCatalog(t.Context(), db, cat, catalogID); err != nil {
		t.Fatalf("ImportCatalog: %v", err)
	}
	store, err := Open(db, catalogID)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return db, store
}

func canonJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %v: %v", v, err)
	}
	return string(raw)
}

// assertCatalogsEquivalent asserts got (pg-loaded) is semantically identical
// to want (YAML-loaded): node-for-node (envelope, sources, fields, every
// declared edge via EdgeTargets — covering storage:top_level), type-for-type
// (wire form of every resolved def), and lint-for-lint.
func assertCatalogsEquivalent(t *testing.T, want, got *graph.Catalog) {
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
			t.Errorf("node %q envelope mismatch", id)
		}
		if canonJSON(t, wn.Sources) != canonJSON(t, gn.Sources) {
			t.Errorf("node %q sources: want %v, got %v", id, wn.Sources, gn.Sources)
		}
		weff, ok := want.Registry.Effective(wn.TypeID)
		if !ok {
			t.Fatalf("node %q: no effective type %q", id, wn.TypeID)
		}
		for _, decl := range weff.EdgeFields {
			wt, gt := wn.EdgeTargets(decl), gn.EdgeTargets(decl)
			if canonJSON(t, wt) != canonJSON(t, gt) {
				t.Errorf("node %q edge %q: want %v, got %v", id, decl.ID, wt, gt)
			}
		}
		for field, targets := range wn.Edges {
			if canonJSON(t, targets) != canonJSON(t, gn.Edges[field]) {
				t.Errorf("node %q raw edge %q: want %v, got %v", id, field, targets, gn.Edges[field])
			}
		}
		if canonJSON(t, wn.Fields) != canonJSON(t, gn.Fields) {
			t.Errorf("node %q fields:\nwant %s\ngot  %s", id, canonJSON(t, wn.Fields), canonJSON(t, gn.Fields))
		}
	}
	wantDefs, gotDefs := want.Registry.All(), got.Registry.All()
	if len(wantDefs) != len(gotDefs) {
		t.Fatalf("type count: want %d, got %d", len(wantDefs), len(gotDefs))
	}
	for i := range wantDefs {
		ww, err := graph.TypeDefToWire(wantDefs[i])
		if err != nil {
			t.Fatalf("TypeDefToWire: %v", err)
		}
		gw, err := graph.TypeDefToWire(gotDefs[i])
		if err != nil {
			t.Fatalf("TypeDefToWire: %v", err)
		}
		if canonJSON(t, ww) != canonJSON(t, gw) {
			t.Errorf("type %q:\nwant %s\ngot  %s", wantDefs[i].ID, canonJSON(t, ww), canonJSON(t, gw))
		}
	}
	if !reflect.DeepEqual(graph.Lint(want), graph.Lint(got)) {
		t.Errorf("lint mismatch:\nwant %v\ngot  %v", graph.Lint(want), graph.Lint(got))
	}
}

// TestImportLoad_PogCatalogParity imports the real pog/catalog.yaml
// (~14 types, ~159 nodes, changeset nodes included) and asserts the
// pg-loaded catalog is semantically identical to the YAML load — including
// the identical lint issue set.
func TestImportLoad_PogCatalogParity(t *testing.T) {
	t.Parallel()
	yamlCat := loadYAML(t, pogCatalogPath)
	_, store := importStore(t, yamlCat, "pog")

	pgCat, rev, err := store.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if rev != "1" {
		t.Errorf("rev = %q, want \"1\"", rev)
	}
	if store.Ref() != "pg:pog" || pgCat.RootPath != "pg:pog" {
		t.Errorf("Ref/RootPath = %q/%q, want pg:pog", store.Ref(), pgCat.RootPath)
	}
	assertCatalogsEquivalent(t, yamlCat, pgCat)
}

// writeFixture writes a single-file catalog with a storage:top_level acyclic
// edge (change.depends_on), a cardinality-one edge (rig.anchor), and a
// changeset type — the surface the parity tests below exercise.
func writeFixture(t *testing.T) string {
	t.Helper()
	const fixture = `schema: project-object-graph/seed-catalog/v0
type_registry:
  - id: core-node
    schema: graph-type/v0
  - id: change
    schema: graph-type/v0
    extends: core-node
    edge_fields:
      - id: depends_on
        target_type: change
        cardinality: many
        storage: top_level
        acyclic: true
  - id: rig
    schema: graph-type/v0
    extends: core-node
    edge_fields:
      - id: anchor
        target_type: change
        cardinality: one
  - id: changeset
    schema: graph-type/v0
    extends: core-node
nodes:
  - schema: graph/change/v1
    id: change-a
    title: Change A
    status: todo
    visibility: internal
    depends_on: ["change-b"]
  - schema: graph/change/v1
    id: change-b
    title: Change B
    status: todo
    visibility: internal
  - schema: graph/rig/v1
    id: rig-a
    title: Rig A
    status: todo
    visibility: internal
    edges:
      anchor: ["change-a"]
`
	path := filepath.Join(t.TempDir(), "catalog.yaml")
	if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// TestTopLevelEdgesNormalizedAndRehydrated proves requirement: ALL edges —
// including storage:top_level ones — are normalized into graph_edges rows,
// and Load rehydrates them back into Fields so the catalog round-trips.
func TestTopLevelEdgesNormalizedAndRehydrated(t *testing.T) {
	t.Parallel()
	yamlCat := loadYAML(t, writeFixture(t))
	db, store := importStore(t, yamlCat, "toplevel")

	var edgeCount int
	if err := db.QueryRow(
		`SELECT count(*) FROM graph.graph_edges WHERE catalog_id = 'toplevel' AND field = 'depends_on'`).
		Scan(&edgeCount); err != nil {
		t.Fatalf("count edges: %v", err)
	}
	if edgeCount != 1 {
		t.Errorf("depends_on edge rows = %d, want 1 (top_level edges must be normalized)", edgeCount)
	}
	var fieldsRaw sql.NullString
	if err := db.QueryRow(
		`SELECT fields FROM graph.graph_nodes WHERE catalog_id = 'toplevel' AND id = 'change-a'`).
		Scan(&fieldsRaw); err != nil {
		t.Fatalf("read fields: %v", err)
	}
	if fieldsRaw.Valid && strings.Contains(fieldsRaw.String, "depends_on") {
		t.Errorf("fields JSONB still holds depends_on (%s); it must be normalized into graph_edges", fieldsRaw.String)
	}

	pgCat, _, err := store.Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	node := pgCat.Nodes["change-a"]
	if got := canonJSON(t, node.Fields["depends_on"]); got != `["change-b"]` {
		t.Errorf("depends_on rehydrated as %s, want [\"change-b\"] inline in Fields", got)
	}
	if len(node.Edges) != 0 {
		t.Errorf("top_level edge leaked into Edges map: %v", node.Edges)
	}
	assertCatalogsEquivalent(t, yamlCat, pgCat)
}

// TestEngineVerbsThroughStore drives the propose -> authorize -> apply
// lifecycle through the pg store using the engine's own changeset machinery
// (ParseChangeset/ValidateChangeset) — the same commit shapes proposeOnce/
// authorizeOnce/applyOnce produce — plus a registry_type_* op, and verifies
// the audit trail and monotonic revisions.
func TestEngineVerbsThroughStore(t *testing.T) {
	t.Parallel()
	yamlCat := loadYAML(t, "../testdata/good/minimal.yaml")
	db, store := importStore(t, yamlCat, "verbs")
	ctx := t.Context()

	// Registry op: add the changeset type (minimal.yaml doesn't declare it).
	_, rev, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	res, err := store.Commit(ctx, rev, []graph.Operation{{
		Kind: graph.OpRegistryTypeAdded,
		Node: "changeset",
		After: map[string]any{
			"id": "changeset", "schema": "graph-type/v0", "extends": "core-node",
		},
	}}, graph.CommitOptions{})
	if err != nil || res.Rejected() {
		t.Fatalf("registry_type_added: err=%v res=%+v", err, res)
	}

	// Propose: validate the payload ops with the engine, then commit the
	// changeset node itself — exactly proposeOnce's shape.
	cat, rev, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rawOps := []any{
		map[string]any{
			"kind": "modified", "node": "req-one",
			"changes": []any{map[string]any{
				"path": []any{"status"}, "before": "satisfied", "after": "done",
			}},
		},
	}
	synthetic := &graph.Node{ID: "cs-candidate", TypeID: "changeset", Fields: map[string]any{"operations": rawOps}}
	cs, err := graph.ParseChangeset(synthetic)
	if err != nil {
		t.Fatalf("ParseChangeset: %v", err)
	}
	if reasons := graph.ValidateChangeset(cs, cat); len(reasons) > 0 {
		t.Fatalf("ValidateChangeset: %v", reasons)
	}
	const csID = "cs-pg-test"
	res, err = store.Commit(ctx, rev, []graph.Operation{{
		Kind: graph.OpAdded, Node: csID,
		After: map[string]any{
			"schema": "graph/changeset/v0", "id": csID, "title": "Flip req-one to done",
			"status": graph.ChangesetStatusProposed, "visibility": "internal",
			"operations": rawOps, "created_at": "2026-07-26T00:00:00Z", "authored_by": "tester",
		},
	}}, graph.CommitOptions{})
	if err != nil || res.Rejected() {
		t.Fatalf("propose commit: err=%v res=%+v", err, res)
	}

	// Authorize: the proposed -> authorized lifecycle flip.
	cat, rev, err = store.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cat.Nodes[csID].Status; got != graph.ChangesetStatusProposed {
		t.Fatalf("changeset status after propose = %q, want proposed", got)
	}
	res, err = store.Commit(ctx, rev, []graph.Operation{{
		Kind: graph.OpModified, Node: csID,
		Changes: []graph.FieldChange{
			{Path: []string{"status"}, Before: graph.ChangesetStatusProposed, After: graph.ChangesetStatusAuthorized},
			{Path: []string{"fields", "authorized_at"}, After: "2026-07-26T00:01:00Z"},
			{Path: []string{"fields", "authorized_by"}, After: "steward"},
		},
	}}, graph.CommitOptions{})
	if err != nil || res.Rejected() {
		t.Fatalf("authorize commit: err=%v res=%+v", err, res)
	}

	// Apply: parse the stored changeset (its operations round-tripped
	// through JSONB), re-validate, commit its ops + the notified flip.
	cat, rev, err = store.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	stored, err := graph.ParseChangeset(cat.Nodes[csID])
	if err != nil {
		t.Fatalf("ParseChangeset(stored): %v", err)
	}
	if stored.Status != graph.ChangesetStatusAuthorized {
		t.Fatalf("stored status = %q, want authorized", stored.Status)
	}
	if reasons := graph.ValidateChangeset(stored, cat); len(reasons) > 0 {
		t.Fatalf("ValidateChangeset(stored): %v", reasons)
	}
	applyOps := append(append([]graph.Operation{}, stored.Operations...), graph.Operation{
		Kind: graph.OpModified, Node: csID,
		Changes: []graph.FieldChange{
			{Path: []string{"status"}, Before: graph.ChangesetStatusAuthorized, After: graph.ChangesetStatusNotified},
			{Path: []string{"fields", "applied_at"}, After: "2026-07-26T00:02:00Z"},
		},
	})
	res, err = store.Commit(ctx, rev, applyOps, graph.CommitOptions{})
	if err != nil || res.Rejected() {
		t.Fatalf("apply commit: err=%v res=%+v", err, res)
	}

	cat, rev, err = store.Load(ctx)
	if err != nil {
		t.Fatalf("final Load: %v", err)
	}
	if rev != "5" {
		t.Errorf("final rev = %q, want \"5\" (import 1 + 4 commits)", rev)
	}
	if got := cat.Nodes["req-one"].Status; got != "done" {
		t.Errorf("req-one status = %q, want done", got)
	}
	if got := cat.Nodes[csID].Status; got != graph.ChangesetStatusNotified {
		t.Errorf("changeset status = %q, want notified", got)
	}
	if got, _ := cat.Nodes[csID].Fields["applied_at"].(string); got == "" {
		t.Error("applied_at not stamped")
	}

	// Audit: one append-only row per landed commit, with the changeset and
	// actor attributed on the lifecycle commits.
	rows, err := db.Query(
		`SELECT rev, changeset_id, actor FROM graph.graph_audit WHERE catalog_id = 'verbs' ORDER BY rev`)
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	defer rows.Close()
	type auditRow struct {
		rev                int64
		changesetID, actor string
	}
	var audits []auditRow
	for rows.Next() {
		var a auditRow
		if err := rows.Scan(&a.rev, &a.changesetID, &a.actor); err != nil {
			t.Fatalf("audit scan: %v", err)
		}
		audits = append(audits, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("audit rows: %v", err)
	}
	want := []auditRow{
		{rev: 2, changesetID: "", actor: ""},          // registry type add
		{rev: 3, changesetID: csID, actor: "tester"},  // propose
		{rev: 4, changesetID: csID, actor: "steward"}, // authorize
		{rev: 5, changesetID: csID, actor: ""},        // apply
	}
	if !reflect.DeepEqual(audits, want) {
		t.Errorf("audit rows:\nwant %+v\ngot  %+v", want, audits)
	}
}

// TestConcurrentCommitConflictRetry: a second writer committing against a
// stale base gets a CAS conflict (graph.IsCASConflict), and the standard
// reload-revalidate-retry loop lands it.
func TestConcurrentCommitConflictRetry(t *testing.T) {
	t.Parallel()
	yamlCat := loadYAML(t, writeFixture(t))
	_, store := importStore(t, yamlCat, "conflict")
	ctx := t.Context()

	_, revA, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load A: %v", err)
	}
	_, revB, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load B: %v", err)
	}

	addOp := func(id string) []graph.Operation {
		return []graph.Operation{{Kind: graph.OpAdded, Node: graph.NodeID(id), After: map[string]any{
			"schema": "graph/change/v1", "id": id, "title": "Change " + id,
			"status": "todo", "visibility": "internal",
		}}}
	}
	res, err := store.Commit(ctx, revA, addOp("change-c"), graph.CommitOptions{})
	if err != nil || res.Rejected() {
		t.Fatalf("writer A: err=%v res=%+v", err, res)
	}

	_, err = store.Commit(ctx, revB, addOp("change-d"), graph.CommitOptions{})
	if err == nil {
		t.Fatal("writer B with stale base: expected CAS conflict, got nil error")
	}
	if !graph.IsCASConflict(err) {
		t.Fatalf("writer B: expected IsCASConflict, got: %v", err)
	}

	// Retry loop: reload, re-validate, re-commit — the verbs' exact recipe.
	_, freshRev, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	res, err = store.Commit(ctx, freshRev, addOp("change-d"), graph.CommitOptions{})
	if err != nil || res.Rejected() {
		t.Fatalf("writer B retry: err=%v res=%+v", err, res)
	}

	cat, rev, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("final Load: %v", err)
	}
	if rev != "3" {
		t.Errorf("rev = %q, want \"3\"", rev)
	}
	for _, id := range []graph.NodeID{"change-c", "change-d"} {
		if _, ok := cat.Nodes[id]; !ok {
			t.Errorf("node %q missing after retry", id)
		}
	}
}

// TestLintGateParity_AcyclicCycle: an op that would close a cycle on an
// acyclic (and storage:top_level) edge is rejected with EXACTLY the same
// lint issue set by the pg store and the file store.
func TestLintGateParity_AcyclicCycle(t *testing.T) {
	t.Parallel()
	path := writeFixture(t)
	yamlCat := loadYAML(t, path)
	_, pgStore := importStore(t, yamlCat, "cycle")
	fileStore := graph.NewFileCatalogStore(path)
	ctx := t.Context()

	cycleOps := []graph.Operation{{
		Kind: graph.OpModified, Node: "change-b",
		Changes: []graph.FieldChange{{Path: []string{"fields", "depends_on"}, After: []any{"change-a"}}},
	}}

	_, fileRev, err := fileStore.Load(ctx)
	if err != nil {
		t.Fatalf("file Load: %v", err)
	}
	fileRes, err := fileStore.Commit(ctx, fileRev, cycleOps, graph.CommitOptions{})
	if err != nil {
		t.Fatalf("file Commit: %v", err)
	}
	_, pgRev, err := pgStore.Load(ctx)
	if err != nil {
		t.Fatalf("pg Load: %v", err)
	}
	pgRes, err := pgStore.Commit(ctx, pgRev, cycleOps, graph.CommitOptions{})
	if err != nil {
		t.Fatalf("pg Commit: %v", err)
	}

	if len(fileRes.LintIssues) == 0 || len(fileRes.RejectReasons) != 0 {
		t.Fatalf("file store: want a lint rejection, got %+v", fileRes)
	}
	if !reflect.DeepEqual(fileRes.LintIssues, pgRes.LintIssues) {
		t.Errorf("lint gate diverged:\nfile %v\npg   %v", fileRes.LintIssues, pgRes.LintIssues)
	}
	if len(pgRes.RejectReasons) != 0 {
		t.Errorf("pg store: lint rejection must not carry RejectReasons, got %v", pgRes.RejectReasons)
	}

	// Nothing persisted on either side.
	if _, rev, err := pgStore.Load(ctx); err != nil || rev != pgRev {
		t.Errorf("pg rev moved after rejection: %q -> %q (err %v)", pgRev, rev, err)
	}
	if _, rev, err := fileStore.Load(ctx); err != nil || rev != fileRev {
		t.Errorf("file catalog changed after rejection (err %v)", err)
	}
}

// TestRejectParity_CardinalityOne: giving a cardinality-one edge two targets
// is refused by both stores as an unappliable candidate (RejectReasons, not
// lint), because per-node cardinality is load-time validation in both paths.
func TestRejectParity_CardinalityOne(t *testing.T) {
	t.Parallel()
	path := writeFixture(t)
	yamlCat := loadYAML(t, path)
	_, pgStore := importStore(t, yamlCat, "card")
	fileStore := graph.NewFileCatalogStore(path)
	ctx := t.Context()

	ops := []graph.Operation{{
		Kind: graph.OpModified, Node: "rig-a",
		Changes: []graph.FieldChange{{Path: []string{"edges", "anchor"}, After: []any{"change-a", "change-b"}}},
	}}

	_, fileRev, err := fileStore.Load(ctx)
	if err != nil {
		t.Fatalf("file Load: %v", err)
	}
	fileRes, err := fileStore.Commit(ctx, fileRev, ops, graph.CommitOptions{})
	if err != nil {
		t.Fatalf("file Commit: %v", err)
	}
	_, pgRev, err := pgStore.Load(ctx)
	if err != nil {
		t.Fatalf("pg Load: %v", err)
	}
	pgRes, err := pgStore.Commit(ctx, pgRev, ops, graph.CommitOptions{})
	if err != nil {
		t.Fatalf("pg Commit: %v", err)
	}

	for name, res := range map[string]graph.CommitResult{"file": fileRes, "pg": pgRes} {
		if len(res.RejectReasons) == 0 || len(res.LintIssues) != 0 {
			t.Errorf("%s store: want RejectReasons-only rejection, got %+v", name, res)
			continue
		}
		if !strings.Contains(res.RejectReasons[0], `cardinality "one" but 2 targets`) {
			t.Errorf("%s store reject reason %q does not name the cardinality violation", name, res.RejectReasons[0])
		}
	}
	if _, rev, err := pgStore.Load(ctx); err != nil || rev != pgRev {
		t.Errorf("pg rev moved after rejection: %q -> %q (err %v)", pgRev, rev, err)
	}
}

// TestDryRunPersistsNothing mirrors the file store's dry-run contract: the
// candidate validates and previews, the rows and revision do not move.
func TestDryRunPersistsNothing(t *testing.T) {
	t.Parallel()
	yamlCat := loadYAML(t, writeFixture(t))
	db, store := importStore(t, yamlCat, "dry")
	ctx := t.Context()

	_, rev, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	res, err := store.Commit(ctx, rev, []graph.Operation{{
		Kind: graph.OpAdded, Node: "change-dry",
		After: map[string]any{
			"schema": "graph/change/v1", "id": "change-dry", "title": "Dry",
			"status": "todo", "visibility": "internal",
		},
	}}, graph.CommitOptions{DryRun: true})
	if err != nil || res.Rejected() {
		t.Fatalf("dry-run: err=%v res=%+v", err, res)
	}
	if len(res.ChangedFiles) == 0 {
		t.Error("dry-run should preview the logical units it would touch")
	}

	cat, newRev, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if newRev != rev {
		t.Errorf("dry-run advanced rev %q -> %q", rev, newRev)
	}
	if _, ok := cat.Nodes["change-dry"]; ok {
		t.Error("dry-run persisted the node")
	}
	var audits int
	if err := db.QueryRow(`SELECT count(*) FROM graph.graph_audit WHERE catalog_id = 'dry'`).Scan(&audits); err != nil {
		t.Fatalf("audit count: %v", err)
	}
	if audits != 0 {
		t.Errorf("dry-run wrote %d audit rows, want 0", audits)
	}
}

// TestImportCatalogRefusesDuplicate: importing over an existing catalog id
// is an error, never a silent overwrite.
func TestImportCatalogRefusesDuplicate(t *testing.T) {
	t.Parallel()
	yamlCat := loadYAML(t, writeFixture(t))
	db, _ := importStore(t, yamlCat, "dup")
	if err := ImportCatalog(t.Context(), db, yamlCat, "dup"); err == nil {
		t.Fatal("expected duplicate import to fail")
	}
}
