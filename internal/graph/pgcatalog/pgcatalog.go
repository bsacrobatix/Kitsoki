// Package pgcatalog is the Postgres-backed graph.CatalogStore: catalogs live
// as normalized rows in a dedicated "graph" schema (schema-per-subsystem on a
// shared database, like internal/artifactjob's postgres backend) instead of
// YAML files. The engine's domain layer stays storage-agnostic — Load rebuilds
// a *graph.Catalog through the same registry Resolve and per-node validation
// the YAML loader runs (graph.BuildCatalogFromData), and Commit runs the same
// lint-delta gate the file pipeline runs — while durability, atomicity, and
// optimistic concurrency come from a single transaction with a
// SELECT ... FOR UPDATE revision check instead of content-digest CAS.
//
// Edge normalization: ALL edges — including declarations with
// storage: top_level (e.g. change.depends_on, which the YAML contract keeps
// inline in the node mapping rather than under edges:) — live in
// graph.graph_edges. Load rehydrates each edge group according to its
// registry declaration: top_level edges back into Node.Fields, edges-map
// edges into Node.Edges, so the in-memory Catalog is indistinguishable from a
// YAML load. (One fidelity note: a top_level edge authored as a bare scalar
// string rehydrates as a one-element list; graph.Node.EdgeTargets reads both
// shapes identically.)
//
// The YAML path stays byte-for-byte unchanged and remains the default; this
// backend is additive and opt-in.
package pgcatalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"kitsoki/internal/graph"
)

// RefPrefix marks a catalog reference as Postgres-backed: "pg:<catalog-id>".
// It is an explicit, opt-in spelling — never something a filesystem path can
// accidentally look like — so binding surfaces (MCP catalog aliases, host
// catalog_path args) can route on it without any schema change.
const RefPrefix = "pg:"

// IsRef reports whether ref is a Postgres catalog reference.
func IsRef(ref string) bool {
	return strings.HasPrefix(ref, RefPrefix)
}

// CatalogID extracts the catalog id from a "pg:<catalog-id>" reference;
// ok is false when ref is not a pg reference or names no id.
func CatalogID(ref string) (id string, ok bool) {
	if !IsRef(ref) {
		return "", false
	}
	id = strings.TrimPrefix(ref, RefPrefix)
	return id, id != ""
}

// schemaDDL creates the graph schema. Idempotent (IF NOT EXISTS throughout);
// re-running against a migrated database is a no-op.
const schemaDDL = `
CREATE SCHEMA IF NOT EXISTS graph;

CREATE TABLE IF NOT EXISTS graph.graph_catalogs (
  catalog_id       TEXT PRIMARY KEY,
  schema_pin       TEXT NOT NULL DEFAULT '',
  rev              BIGINT NOT NULL DEFAULT 1,
  lint_hints       JSONB,
  write_policy     JSONB,
  feedback_routing JSONB,
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS graph.graph_types (
  catalog_id TEXT NOT NULL REFERENCES graph.graph_catalogs(catalog_id) ON DELETE CASCADE,
  type_id    TEXT NOT NULL,
  decl       JSONB NOT NULL,
  PRIMARY KEY (catalog_id, type_id)
);

CREATE TABLE IF NOT EXISTS graph.graph_nodes (
  catalog_id TEXT NOT NULL REFERENCES graph.graph_catalogs(catalog_id) ON DELETE CASCADE,
  id         TEXT NOT NULL,
  type_id    TEXT NOT NULL,
  schema_pin TEXT NOT NULL,
  title      TEXT NOT NULL,
  status     TEXT NOT NULL,
  visibility TEXT NOT NULL,
  sources    JSONB,
  fields     JSONB,
  PRIMARY KEY (catalog_id, id)
);

CREATE INDEX IF NOT EXISTS graph_nodes_type ON graph.graph_nodes(catalog_id, type_id);

CREATE TABLE IF NOT EXISTS graph.graph_edges (
  catalog_id TEXT NOT NULL REFERENCES graph.graph_catalogs(catalog_id) ON DELETE CASCADE,
  src        TEXT NOT NULL,
  field      TEXT NOT NULL,
  dst        TEXT NOT NULL,
  ord        INT NOT NULL,
  PRIMARY KEY (catalog_id, src, field, ord)
);

CREATE INDEX IF NOT EXISTS graph_edges_dst ON graph.graph_edges(catalog_id, dst);

CREATE TABLE IF NOT EXISTS graph.graph_audit (
  catalog_id   TEXT NOT NULL,
  rev          BIGINT NOT NULL,
  changeset_id TEXT NOT NULL DEFAULT '',
  actor        TEXT NOT NULL DEFAULT '',
  summary      JSONB,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS graph_audit_catalog_rev ON graph.graph_audit(catalog_id, rev);
`

// Store is a graph.CatalogStore over one catalog's rows in Postgres. It
// holds no state beyond the handle and the catalog id — each Load is a
// fresh full read, matching the per-call reload contract the file store and
// every MCP/host read already have.
type Store struct {
	db        *sql.DB
	catalogID string
}

var _ graph.CatalogStore = (*Store)(nil)

// Open runs the idempotent schema migration and returns the store for
// catalogID. It does not require the catalog to exist yet — ImportCatalog
// creates it; a Load before that reports a clear not-found error.
func Open(db *sql.DB, catalogID string) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("pgcatalog.Open: nil db")
	}
	if catalogID == "" {
		return nil, fmt.Errorf("pgcatalog.Open: empty catalog id")
	}
	if _, err := db.Exec(schemaDDL); err != nil {
		return nil, fmt.Errorf("pgcatalog.Open: schema migration: %w", err)
	}
	return &Store{db: db, catalogID: catalogID}, nil
}

// Ref names the stored catalog for diagnostics and routing.
func (s *Store) Ref() string { return "pg:" + s.catalogID }

// Load reads the whole catalog and rebuilds a *graph.Catalog through the
// same validation the YAML loader runs. RootPath is the synthetic Ref();
// NodeFile and ContentDigest are empty (nothing on disk backs this catalog).
// The revision token is graph_catalogs.rev.
func (s *Store) Load(ctx context.Context) (*graph.Catalog, graph.CatalogRev, error) {
	rev, data, err := loadData(ctx, s.db, s.catalogID)
	if err != nil {
		return nil, "", err
	}
	cat, err := graph.BuildCatalogFromData(s.Ref(), data)
	if err != nil {
		return nil, "", fmt.Errorf("pgcatalog: load %s: %w", s.catalogID, err)
	}
	return cat, revToken(rev), nil
}

// Audit is the provenance recorded with a committed revision in
// graph.graph_audit. Commit derives a best-effort Audit from the operations
// themselves; CommitWithAudit lets a caller that knows the acting changeset/
// actor say so explicitly.
type Audit struct {
	ChangesetID string
	Actor       string
}

// Commit applies ops against the catalog as of base in a single transaction:
// SELECT ... FOR UPDATE pins the revision row, a base mismatch returns an
// error satisfying graph.IsCASConflict (the caller's cue to reload and
// retry), the operations are applied in memory with the file pipeline's
// exact semantics (graph.ApplyOperationsToData), the candidate is re-built
// and re-validated (graph.BuildCatalogFromData) and gated on the same
// lint-delta policy (graph.DeltaErrorIssues) the file path enforces, and
// only then are the rows rewritten, the revision bumped, and an audit row
// appended. Rejections come back inside the CommitResult with a nil error;
// nothing is persisted for a rejection or with opts.DryRun set.
func (s *Store) Commit(ctx context.Context, base graph.CatalogRev, ops []graph.Operation, opts graph.CommitOptions) (graph.CommitResult, error) {
	return s.commit(ctx, base, ops, opts, nil)
}

// CommitWithAudit is Commit with explicit audit provenance instead of the
// best-effort derivation from the operations.
func (s *Store) CommitWithAudit(ctx context.Context, base graph.CatalogRev, ops []graph.Operation, opts graph.CommitOptions, audit Audit) (graph.CommitResult, error) {
	return s.commit(ctx, base, ops, opts, &audit)
}

func (s *Store) commit(ctx context.Context, base graph.CatalogRev, ops []graph.Operation, opts graph.CommitOptions, audit *Audit) (graph.CommitResult, error) {
	baseRev, err := strconv.ParseInt(string(base), 10, 64)
	if err != nil {
		return graph.CommitResult{}, fmt.Errorf("pgcatalog: commit %s: base revision %q is not a pg revision token: %w", s.catalogID, base, err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return graph.CommitResult{}, fmt.Errorf("pgcatalog: commit %s: begin: %w", s.catalogID, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	var rev int64
	err = tx.QueryRowContext(ctx,
		`SELECT rev FROM graph.graph_catalogs WHERE catalog_id = $1 FOR UPDATE`, s.catalogID).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return graph.CommitResult{}, fmt.Errorf("pgcatalog: commit %s: catalog not found (ImportCatalog first)", s.catalogID)
	}
	if err != nil {
		return graph.CommitResult{}, fmt.Errorf("pgcatalog: commit %s: lock revision: %w", s.catalogID, err)
	}
	if rev != baseRev {
		return graph.CommitResult{}, fmt.Errorf("pgcatalog: commit %s: base rev %d is stale (current %d): %w",
			s.catalogID, baseRev, rev, graph.ErrCASConflict)
	}

	_, data, err := loadData(ctx, tx, s.catalogID)
	if err != nil {
		return graph.CommitResult{}, err
	}
	baselineCat, err := graph.BuildCatalogFromData(s.Ref(), data)
	if err != nil {
		return graph.CommitResult{}, fmt.Errorf("pgcatalog: commit %s: stored catalog invalid: %w", s.catalogID, err)
	}
	baseline := graph.ErrorIssues(graph.Lint(baselineCat))

	candidateData, rejects := graph.ApplyOperationsToData(data, ops)
	if len(rejects) > 0 {
		return graph.CommitResult{RejectReasons: rejects}, nil
	}
	candidateCat, err := graph.BuildCatalogFromData(s.Ref(), candidateData)
	if err != nil {
		// Parity with the file pipeline: an unappliable candidate (duplicate
		// id, cardinality violation, unknown type, ...) is a rejection, not
		// a Go error — the same failure LoadCatalog on a scratch tree reports.
		return graph.CommitResult{RejectReasons: []string{fmt.Sprintf("candidate catalog failed to load: %v", err)}}, nil
	}
	if issues := graph.DeltaErrorIssues(baseline, graph.ErrorIssues(graph.Lint(candidateCat))); len(issues) > 0 {
		return graph.CommitResult{LintIssues: issues}, nil
	}

	changed := changedUnits(ops)
	if opts.DryRun {
		return graph.CommitResult{ChangedFiles: changed}, nil
	}

	newRev := rev + 1
	if err := replaceCatalogRows(ctx, tx, s.catalogID, candidateCat, candidateData); err != nil {
		return graph.CommitResult{}, fmt.Errorf("pgcatalog: commit %s: %w", s.catalogID, err)
	}
	if err := updateCatalogRow(ctx, tx, s.catalogID, newRev, candidateData); err != nil {
		return graph.CommitResult{}, fmt.Errorf("pgcatalog: commit %s: %w", s.catalogID, err)
	}
	if audit == nil {
		derived := deriveAudit(baselineCat, ops)
		audit = &derived
	}
	if err := insertAuditRow(ctx, tx, s.catalogID, newRev, *audit, ops, changed); err != nil {
		return graph.CommitResult{}, fmt.Errorf("pgcatalog: commit %s: %w", s.catalogID, err)
	}
	if err := tx.Commit(); err != nil {
		return graph.CommitResult{}, fmt.Errorf("pgcatalog: commit %s: %w", s.catalogID, err)
	}
	return graph.CommitResult{ChangedFiles: changed}, nil
}

// ImportCatalog persists cat as a brand-new catalog under catalogID at
// revision 1 — the YAML→Postgres migration entry point (CLI wiring lives
// elsewhere; the export direction is Store.Load). The catalog must not
// already exist. cat is any loaded *graph.Catalog — typically
// graph.LoadCatalog of a YAML catalog.
func ImportCatalog(ctx context.Context, db *sql.DB, cat *graph.Catalog, catalogID string) error {
	if db == nil {
		return fmt.Errorf("pgcatalog.ImportCatalog: nil db")
	}
	if catalogID == "" {
		return fmt.Errorf("pgcatalog.ImportCatalog: empty catalog id")
	}
	if _, err := db.ExecContext(ctx, schemaDDL); err != nil {
		return fmt.Errorf("pgcatalog.ImportCatalog: schema migration: %w", err)
	}
	data, err := cat.ExportData()
	if err != nil {
		return fmt.Errorf("pgcatalog.ImportCatalog %s: %w", catalogID, err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("pgcatalog.ImportCatalog %s: begin: %w", catalogID, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	var exists bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM graph.graph_catalogs WHERE catalog_id = $1)`, catalogID).Scan(&exists); err != nil {
		return fmt.Errorf("pgcatalog.ImportCatalog %s: %w", catalogID, err)
	}
	if exists {
		return fmt.Errorf("pgcatalog.ImportCatalog %s: catalog already exists", catalogID)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO graph.graph_catalogs (catalog_id, schema_pin, rev) VALUES ($1, $2, 1)`,
		catalogID, string(cat.Schema)); err != nil {
		return fmt.Errorf("pgcatalog.ImportCatalog %s: %w", catalogID, err)
	}
	if err := updateCatalogRow(ctx, tx, catalogID, 1, data); err != nil {
		return fmt.Errorf("pgcatalog.ImportCatalog %s: %w", catalogID, err)
	}
	if err := replaceCatalogRows(ctx, tx, catalogID, cat, data); err != nil {
		return fmt.Errorf("pgcatalog.ImportCatalog %s: %w", catalogID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("pgcatalog.ImportCatalog %s: %w", catalogID, err)
	}
	return nil
}

// --- row IO ---------------------------------------------------------------

// querier is the shared face of *sql.DB and *sql.Tx the row IO runs against,
// so Load (plain reads) and Commit (in-transaction reads) use one code path.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// loadData reads every row for catalogID and reassembles the storage-neutral
// wire form: type decls verbatim, node envelopes with fields inlined, and
// every graph_edges group rehydrated per its registry declaration — a
// storage:top_level edge back inline (Fields), everything else under
// "edges". The result is what graph.BuildCatalogFromData validates.
func loadData(ctx context.Context, q querier, catalogID string) (int64, graph.CatalogData, error) {
	var (
		rev                 int64
		schemaPin           string
		hintsRaw, policyRaw []byte
		routingRaw          []byte
		data                graph.CatalogData
	)
	err := q.QueryRowContext(ctx,
		`SELECT rev, schema_pin, lint_hints, write_policy, feedback_routing
		   FROM graph.graph_catalogs WHERE catalog_id = $1`, catalogID).
		Scan(&rev, &schemaPin, &hintsRaw, &policyRaw, &routingRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, data, fmt.Errorf("pgcatalog: catalog %q not found (ImportCatalog first)", catalogID)
	}
	if err != nil {
		return 0, data, fmt.Errorf("pgcatalog: load %s: %w", catalogID, err)
	}
	data.Schema = graph.SchemaPin(schemaPin)
	if len(hintsRaw) > 0 {
		data.LintHints = &graph.LintHints{}
		if err := json.Unmarshal(hintsRaw, data.LintHints); err != nil {
			return 0, data, fmt.Errorf("pgcatalog: load %s: lint_hints: %w", catalogID, err)
		}
	}
	if len(policyRaw) > 0 {
		data.WritePolicy = &graph.WritePolicy{}
		if err := json.Unmarshal(policyRaw, data.WritePolicy); err != nil {
			return 0, data, fmt.Errorf("pgcatalog: load %s: write_policy: %w", catalogID, err)
		}
	}
	if len(routingRaw) > 0 {
		data.FeedbackRouting = &graph.FeedbackRouting{}
		if err := json.Unmarshal(routingRaw, data.FeedbackRouting); err != nil {
			return 0, data, fmt.Errorf("pgcatalog: load %s: feedback_routing: %w", catalogID, err)
		}
	}

	rows, err := q.QueryContext(ctx,
		`SELECT decl FROM graph.graph_types WHERE catalog_id = $1 ORDER BY type_id`, catalogID)
	if err != nil {
		return 0, data, fmt.Errorf("pgcatalog: load %s: types: %w", catalogID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var declRaw []byte
		if err := rows.Scan(&declRaw); err != nil {
			return 0, data, fmt.Errorf("pgcatalog: load %s: types: %w", catalogID, err)
		}
		var decl map[string]any
		if err := json.Unmarshal(declRaw, &decl); err != nil {
			return 0, data, fmt.Errorf("pgcatalog: load %s: type decl: %w", catalogID, err)
		}
		data.Types = append(data.Types, decl)
	}
	if err := rows.Err(); err != nil {
		return 0, data, fmt.Errorf("pgcatalog: load %s: types: %w", catalogID, err)
	}
	rows.Close()

	// The registry must resolve BEFORE nodes assemble: each edge group's
	// placement (inline top_level field vs "edges" submap) depends on the
	// src node's effective type declaration.
	reg, _, err := graph.ResolveWireTypes(data.Types)
	if err != nil {
		return 0, data, fmt.Errorf("pgcatalog: load %s: %w", catalogID, err)
	}

	type edgeGroup struct {
		field   string
		targets []any
	}
	edgesBySrc := map[string][]*edgeGroup{}
	rows, err = q.QueryContext(ctx,
		`SELECT src, field, dst FROM graph.graph_edges WHERE catalog_id = $1 ORDER BY src, field, ord`, catalogID)
	if err != nil {
		return 0, data, fmt.Errorf("pgcatalog: load %s: edges: %w", catalogID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var src, field, dst string
		if err := rows.Scan(&src, &field, &dst); err != nil {
			return 0, data, fmt.Errorf("pgcatalog: load %s: edges: %w", catalogID, err)
		}
		groups := edgesBySrc[src]
		if n := len(groups); n > 0 && groups[n-1].field == field {
			groups[n-1].targets = append(groups[n-1].targets, dst)
		} else {
			edgesBySrc[src] = append(groups, &edgeGroup{field: field, targets: []any{dst}})
		}
	}
	if err := rows.Err(); err != nil {
		return 0, data, fmt.Errorf("pgcatalog: load %s: edges: %w", catalogID, err)
	}
	rows.Close()

	rows, err = q.QueryContext(ctx,
		`SELECT id, type_id, schema_pin, title, status, visibility, sources, fields
		   FROM graph.graph_nodes WHERE catalog_id = $1 ORDER BY id`, catalogID)
	if err != nil {
		return 0, data, fmt.Errorf("pgcatalog: load %s: nodes: %w", catalogID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id, typeID, nodeSchema, title, status, visibility string
			sourcesRaw, fieldsRaw                             []byte
		)
		if err := rows.Scan(&id, &typeID, &nodeSchema, &title, &status, &visibility, &sourcesRaw, &fieldsRaw); err != nil {
			return 0, data, fmt.Errorf("pgcatalog: load %s: nodes: %w", catalogID, err)
		}
		nm := map[string]any{
			"schema":     nodeSchema,
			"id":         id,
			"title":      title,
			"status":     status,
			"visibility": visibility,
		}
		if len(sourcesRaw) > 0 {
			var sources []any
			if err := json.Unmarshal(sourcesRaw, &sources); err != nil {
				return 0, data, fmt.Errorf("pgcatalog: load %s: node %q sources: %w", catalogID, id, err)
			}
			if len(sources) > 0 {
				nm["sources"] = sources
			}
		}
		var edges map[string]any
		if len(fieldsRaw) > 0 {
			var fields map[string]any
			if err := json.Unmarshal(fieldsRaw, &fields); err != nil {
				return 0, data, fmt.Errorf("pgcatalog: load %s: node %q fields: %w", catalogID, id, err)
			}
			// The reserved "edges" key carries authored-but-empty edge
			// fields (see replaceCatalogRows); it seeds the edges map
			// instead of landing inline.
			if seed, ok := fields["edges"].(map[string]any); ok {
				edges = seed
				delete(fields, "edges")
			}
			for k, v := range fields {
				nm[k] = v
			}
		}
		for _, group := range edgesBySrc[id] {
			if declStorage(reg, typeID, group.field) == graph.StorageTopLevel {
				nm[group.field] = group.targets
				continue
			}
			if edges == nil {
				edges = map[string]any{}
			}
			edges[group.field] = group.targets
		}
		if edges != nil {
			nm["edges"] = edges
		}
		data.Nodes = append(data.Nodes, nm)
	}
	if err := rows.Err(); err != nil {
		return 0, data, fmt.Errorf("pgcatalog: load %s: nodes: %w", catalogID, err)
	}
	return rev, data, nil
}

// declStorage looks up the declared storage of typeID's edge field, or the
// default (edges-map) when the type or field is unknown.
func declStorage(reg *graph.Registry, typeID, field string) graph.EdgeStorage {
	eff, ok := reg.Effective(typeID)
	if !ok {
		return graph.StorageEdges
	}
	for _, decl := range eff.EdgeFields {
		if string(decl.ID) == field {
			return decl.Storage
		}
	}
	return graph.StorageEdges
}

// replaceCatalogRows rewrites catalogID's type/node/edge rows from the
// validated catalog. Every edge — edges-map or top_level — is normalized
// into graph_edges; a top_level edge's key is removed from the node's
// fields JSONB when (and only when) its targets became rows, so zero-target
// raw values (e.g. an authored empty list) survive in fields verbatim.
func replaceCatalogRows(ctx context.Context, q querier, catalogID string, cat *graph.Catalog, data graph.CatalogData) error {
	for _, table := range []string{"graph_edges", "graph_nodes", "graph_types"} {
		if _, err := q.ExecContext(ctx, `DELETE FROM graph.`+table+` WHERE catalog_id = $1`, catalogID); err != nil {
			return fmt.Errorf("clear %s: %w", table, err)
		}
	}

	for _, tw := range data.Types {
		typeID, _ := tw["id"].(string)
		declRaw, err := json.Marshal(tw)
		if err != nil {
			return fmt.Errorf("type %q decl: %w", typeID, err)
		}
		if _, err := q.ExecContext(ctx,
			`INSERT INTO graph.graph_types (catalog_id, type_id, decl) VALUES ($1, $2, $3)`,
			catalogID, typeID, declRaw); err != nil {
			return fmt.Errorf("insert type %q: %w", typeID, err)
		}
	}

	for _, id := range cat.SortedNodeIDs() {
		node := cat.Nodes[id]
		fields := make(map[string]any, len(node.Fields))
		for k, v := range node.Fields {
			fields[k] = v
		}

		type edgeRow struct {
			field   string
			targets []graph.NodeID
		}
		var edgeRows []edgeRow
		if eff, ok := cat.Registry.Effective(node.TypeID); ok {
			for _, decl := range eff.EdgeFields {
				if decl.Storage != graph.StorageTopLevel {
					continue
				}
				targets := node.EdgeTargets(decl)
				if len(targets) == 0 {
					continue
				}
				delete(fields, string(decl.ID))
				edgeRows = append(edgeRows, edgeRow{field: string(decl.ID), targets: targets})
			}
		}
		edgeFields := make([]string, 0, len(node.Edges))
		for field := range node.Edges {
			edgeFields = append(edgeFields, string(field))
		}
		sort.Strings(edgeFields)
		// A present-but-empty edges-map entry (an authored `field: []`) has
		// no rows to carry it, but a YAML load keeps the key — so it rides
		// in fields JSONB under the reserved "edges" key, which a real node
		// field can never occupy (the loader's envelope decoding consumes
		// `edges:` before inline fields are collected). Load merges it back.
		emptyEdges := map[string]any{}
		for _, field := range edgeFields {
			targets := node.Edges[graph.EdgeField(field)]
			if len(targets) == 0 {
				emptyEdges[field] = []any{}
				continue
			}
			edgeRows = append(edgeRows, edgeRow{field: field, targets: targets})
		}
		if len(emptyEdges) > 0 {
			fields["edges"] = emptyEdges
		}

		var sourcesRaw any
		if len(node.Sources) > 0 {
			raw, err := json.Marshal(node.Sources)
			if err != nil {
				return fmt.Errorf("node %q sources: %w", id, err)
			}
			sourcesRaw = raw
		}
		var fieldsRaw any
		if len(fields) > 0 {
			raw, err := json.Marshal(fields)
			if err != nil {
				return fmt.Errorf("node %q fields: %w", id, err)
			}
			fieldsRaw = raw
		}
		if _, err := q.ExecContext(ctx,
			`INSERT INTO graph.graph_nodes (catalog_id, id, type_id, schema_pin, title, status, visibility, sources, fields)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			catalogID, string(id), node.TypeID, string(node.Schema), node.Title, node.Status, string(node.Visibility),
			sourcesRaw, fieldsRaw); err != nil {
			return fmt.Errorf("insert node %q: %w", id, err)
		}
		for _, er := range edgeRows {
			for ord, dst := range er.targets {
				if _, err := q.ExecContext(ctx,
					`INSERT INTO graph.graph_edges (catalog_id, src, field, dst, ord) VALUES ($1, $2, $3, $4, $5)`,
					catalogID, string(id), er.field, string(dst), ord); err != nil {
					return fmt.Errorf("insert edge %s.%s[%d]: %w", id, er.field, ord, err)
				}
			}
		}
	}
	return nil
}

// updateCatalogRow refreshes the catalog header row: revision, schema pin,
// and the three optional catalog-level JSONB blocks.
func updateCatalogRow(ctx context.Context, q querier, catalogID string, rev int64, data graph.CatalogData) error {
	hintsRaw, err := marshalOrNil(data.LintHints)
	if err != nil {
		return fmt.Errorf("lint_hints: %w", err)
	}
	policyRaw, err := marshalOrNil(data.WritePolicy)
	if err != nil {
		return fmt.Errorf("write_policy: %w", err)
	}
	routingRaw, err := marshalOrNil(data.FeedbackRouting)
	if err != nil {
		return fmt.Errorf("feedback_routing: %w", err)
	}
	_, err = q.ExecContext(ctx,
		`UPDATE graph.graph_catalogs
		    SET rev = $2, schema_pin = $3, lint_hints = $4, write_policy = $5, feedback_routing = $6, updated_at = now()
		  WHERE catalog_id = $1`,
		catalogID, rev, string(data.Schema), hintsRaw, policyRaw, routingRaw)
	if err != nil {
		return fmt.Errorf("update catalog row: %w", err)
	}
	return nil
}

// marshalOrNil renders v as JSON, or SQL NULL for a nil pointer — a typed
// nil (e.g. (*graph.LintHints)(nil)) inside an any must become a real NULL,
// not the JSON literal "null".
func marshalOrNil(v any) (any, error) {
	switch val := v.(type) {
	case *graph.LintHints:
		if val == nil {
			return nil, nil
		}
	case *graph.WritePolicy:
		if val == nil {
			return nil, nil
		}
	case *graph.FeedbackRouting:
		if val == nil {
			return nil, nil
		}
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// insertAuditRow appends the append-only provenance record for a committed
// revision: who (actor), through which changeset, and a summary of the
// operation kinds and the logical units they touched.
func insertAuditRow(ctx context.Context, q querier, catalogID string, rev int64, audit Audit, ops []graph.Operation, changed []string) error {
	type opSummary struct {
		Kind string `json:"kind"`
		Node string `json:"node,omitempty"`
		From string `json:"from,omitempty"`
		To   string `json:"to,omitempty"`
	}
	summary := struct {
		Ops     []opSummary `json:"ops"`
		Changed []string    `json:"changed"`
	}{Changed: changed}
	for _, op := range ops {
		summary.Ops = append(summary.Ops, opSummary{
			Kind: string(op.Kind),
			Node: string(op.Node),
			From: string(op.From),
			To:   string(op.To),
		})
	}
	summaryRaw, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("audit summary: %w", err)
	}
	if _, err := q.ExecContext(ctx,
		`INSERT INTO graph.graph_audit (catalog_id, rev, changeset_id, actor, summary) VALUES ($1, $2, $3, $4, $5)`,
		catalogID, rev, audit.ChangesetID, audit.Actor, summaryRaw); err != nil {
		return fmt.Errorf("insert audit row: %w", err)
	}
	return nil
}

// deriveAudit extracts best-effort provenance from the operations when the
// caller didn't supply an explicit Audit: the first changeset node an op
// adds or modifies (by baseline type) names the changeset, and an
// authored_by / authorized_by value in the payload names the actor.
func deriveAudit(baseline *graph.Catalog, ops []graph.Operation) Audit {
	var audit Audit
	for _, op := range ops {
		switch op.Kind {
		case graph.OpAdded:
			pin, _ := op.After["schema"].(string)
			if _, typeID, _, err := graph.ParseSchemaPin(graph.SchemaPin(pin)); err == nil && typeID == "changeset" {
				if audit.ChangesetID == "" {
					audit.ChangesetID = string(op.Node)
				}
				if actor, _ := op.After["authored_by"].(string); actor != "" && audit.Actor == "" {
					audit.Actor = actor
				}
			}
		case graph.OpModified:
			if node, ok := baseline.Nodes[op.Node]; ok && node.TypeID == "changeset" {
				if audit.ChangesetID == "" {
					audit.ChangesetID = string(op.Node)
				}
				for _, ch := range op.Changes {
					if len(ch.Path) == 2 && ch.Path[0] == "fields" &&
						(ch.Path[1] == "authorized_by" || ch.Path[1] == "authored_by") {
						if actor, _ := ch.After.(string); actor != "" && audit.Actor == "" {
							audit.Actor = actor
						}
					}
				}
			}
		}
	}
	return audit
}

// changedUnits names the logical units a batch of operations touches —
// the non-file store's CommitResult.ChangedFiles contract ("a non-file
// store reports the logical units it touched").
func changedUnits(ops []graph.Operation) []string {
	seen := map[string]bool{}
	var out []string
	add := func(unit string) {
		if unit == "" || seen[unit] {
			return
		}
		seen[unit] = true
		out = append(out, unit)
	}
	for _, op := range ops {
		switch op.Kind {
		case graph.OpRenamed:
			add("node/" + string(op.From))
			add("node/" + string(op.To))
		case graph.OpRegistryTypeAdded, graph.OpRegistryTypeModified:
			add("type/" + string(op.Node))
		default:
			add("node/" + string(op.Node))
		}
	}
	sort.Strings(out)
	return out
}

func revToken(rev int64) graph.CatalogRev {
	return graph.CatalogRev(strconv.FormatInt(rev, 10))
}

// AuditOp is one operation summarized in a graph_audit row — the persisted
// mirror of insertAuditRow's opSummary.
type AuditOp struct {
	Kind string `json:"kind"`
	Node string `json:"node,omitempty"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// AuditEntry is one committed revision's append-only provenance record: who
// (Actor), through which changeset, which operations, and when. It is the
// pg catalog's replacement for the git-era history walk — graph.history
// renders these as source:"audit" timeline entries.
type AuditEntry struct {
	Rev         int64
	ChangesetID string
	Actor       string
	Ops         []AuditOp
	Changed     []string
	CreatedAt   time.Time
}

// AuditEntries returns every audit row for the catalog, newest revision
// first. Rows whose summary fails to decode are skipped best-effort, matching
// the history walk's treatment of unparsable historical revisions.
func (s *Store) AuditEntries(ctx context.Context) ([]AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT rev, changeset_id, actor, summary, created_at
		   FROM graph.graph_audit WHERE catalog_id = $1 ORDER BY rev DESC`, s.catalogID)
	if err != nil {
		return nil, fmt.Errorf("pgcatalog: audit %s: %w", s.catalogID, err)
	}
	defer rows.Close()
	var entries []AuditEntry
	for rows.Next() {
		var (
			e          AuditEntry
			summaryRaw []byte
		)
		if err := rows.Scan(&e.Rev, &e.ChangesetID, &e.Actor, &summaryRaw, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("pgcatalog: audit %s: %w", s.catalogID, err)
		}
		if len(summaryRaw) > 0 {
			var summary struct {
				Ops     []AuditOp `json:"ops"`
				Changed []string  `json:"changed"`
			}
			if err := json.Unmarshal(summaryRaw, &summary); err == nil {
				e.Ops = summary.Ops
				e.Changed = summary.Changed
			}
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgcatalog: audit %s: %w", s.catalogID, err)
	}
	return entries, nil
}
