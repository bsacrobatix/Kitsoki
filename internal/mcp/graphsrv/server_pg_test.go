package graphsrv_test

// server_pg_test.go: a pg-bound catalog alias exercised end to end through
// the real MCP server — bind "pg:<catalog-id>", then open / find / get /
// propose / authorize / apply / history against it. Runs against
// internal/dbruntime/pgtest's embedded (or KITSOKI_PG_DSN external) server
// and skips cleanly when none is available. Every file-path test in this
// package is untouched: the pg ref is an explicit opt-in spelling.

import (
	"context"
	"testing"

	"kitsoki/internal/dbruntime/pgtest"
	objectgraph "kitsoki/internal/graph"
	"kitsoki/internal/graph/pgcatalog"
	"kitsoki/internal/host"
	"kitsoki/internal/mcp/graphsrv"
)

// openPGFixture imports the shared graph fixture into a fresh pgtest database
// under catalogID and points host's pg catalog routing at that database for
// the duration of the test.
func openPGFixture(t *testing.T, catalogID string) {
	t.Helper()
	db := pgtest.Open(t)
	cat, err := objectgraph.LoadCatalog(fixturePath)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	if err := pgcatalog.ImportCatalog(context.Background(), db, cat, catalogID); err != nil {
		t.Fatalf("ImportCatalog: %v", err)
	}
	host.SetGraphPGDB(db)
	t.Cleanup(func() { host.SetGraphPGDB(nil) })
}

func TestGraphServer_PGCatalog_ReadWriteHistoryRoundTrip(t *testing.T) {
	openPGFixture(t, "pg-fixture")
	cs, done := connectGraphServer(t, graphsrv.Config{
		CatalogFlags: []string{"pgcat=pg:pg-fixture"},
		Mode:         graphsrv.ModeSteward,
		Actor:        "pg-tester",
	})
	defer done()

	// graph.open: head.rev is the store revision token, no git involved.
	m, isErr := callTool(t, cs, "graph.open", map[string]any{})
	if isErr {
		t.Fatalf("graph.open: %+v", m)
	}
	if got, _ := m["catalog"].(string); got != "pgcat" {
		t.Fatalf("catalog = %v, want pgcat", m["catalog"])
	}
	head, _ := m["head"].(map[string]any)
	if rev, _ := head["rev"].(string); rev != "1" {
		t.Fatalf("head.rev = %v, want \"1\" (import revision)", head["rev"])
	}
	if dirty, _ := head["dirty"].(bool); dirty {
		t.Fatalf("head.dirty = true, want false for a pg catalog")
	}

	// graph.find: same tool schema, same rows, against the pg rows.
	m, isErr = callTool(t, cs, "graph.find", map[string]any{"type": "requirement"})
	if isErr {
		t.Fatalf("graph.find: %+v", m)
	}
	if total, _ := m["total"].(float64); total < 2 {
		t.Fatalf("graph.find total = %v, want >= 2 requirements", m["total"])
	}

	// graph.get: full envelope incl. edges resolved from graph_edges rows.
	m, isErr = callTool(t, cs, "graph.get", map[string]any{"ids": []string{"req-alpha"}})
	if isErr {
		t.Fatalf("graph.get: %+v", m)
	}
	nodes, _ := m["nodes"].([]any)
	if len(nodes) != 1 {
		t.Fatalf("graph.get nodes = %+v, want exactly req-alpha", m["nodes"])
	}

	// graph.propose → authorize → apply, all committed through the pg store.
	m, isErr = callTool(t, cs, "graph.propose",
		proposeArgs("flip req-beta", []map[string]any{flipStatusOp("req-beta", "active", "done")}))
	if isErr {
		t.Fatalf("graph.propose: %+v", m)
	}
	changesetID, _ := m["changeset_id"].(string)
	if changesetID == "" {
		t.Fatalf("expected a changeset_id, got %+v", m)
	}
	if status, _ := m["status"].(string); status != "proposed" {
		t.Fatalf("status = %v, want proposed", m["status"])
	}

	m, isErr = callTool(t, cs, "graph.authorize", map[string]any{"id": changesetID})
	if isErr {
		t.Fatalf("graph.authorize: %+v", m)
	}
	m, isErr = callTool(t, cs, "graph.apply", map[string]any{"id": changesetID})
	if isErr {
		t.Fatalf("graph.apply: %+v", m)
	}
	if applied, _ := m["applied"].(bool); !applied {
		t.Fatalf("graph.apply applied = %v, want true: %+v", m["applied"], m)
	}

	// The write landed: req-beta reads back done, and head.rev advanced by
	// exactly the three committed revisions (propose, authorize, apply).
	m, isErr = callTool(t, cs, "graph.get", map[string]any{"ids": []string{"req-beta"}})
	if isErr {
		t.Fatalf("graph.get req-beta: %+v", m)
	}
	nodes, _ = m["nodes"].([]any)
	if len(nodes) != 1 {
		t.Fatalf("graph.get req-beta nodes = %+v", m["nodes"])
	}
	node, _ := nodes[0].(map[string]any)
	if status, _ := node["status"].(string); status != "done" {
		t.Fatalf("req-beta status = %v, want done", node["status"])
	}
	m, isErr = callTool(t, cs, "graph.open", map[string]any{})
	if isErr {
		t.Fatalf("graph.open (after writes): %+v", m)
	}
	head, _ = m["head"].(map[string]any)
	if rev, _ := head["rev"].(string); rev != "4" {
		t.Fatalf("head.rev = %v, want \"4\" after propose+authorize+apply", head["rev"])
	}

	// graph.history: changeset-era entries plus source:"audit" entries from
	// graph_audit — never a git walk for a pg catalog.
	m, isErr = callTool(t, cs, "graph.history", map[string]any{"id": "req-beta"})
	if isErr {
		t.Fatalf("graph.history: %+v", m)
	}
	entries, _ := m["entries"].([]any)
	if len(entries) == 0 {
		t.Fatalf("graph.history returned no entries: %+v", m)
	}
	sources := map[string]int{}
	for _, e := range entries {
		em, _ := e.(map[string]any)
		src, _ := em["source"].(string)
		sources[src]++
		if src == "git" {
			t.Fatalf("pg catalog history produced a git-era entry: %+v", em)
		}
	}
	if sources["changeset"] == 0 {
		t.Fatalf("expected changeset-era entries, got sources %v", sources)
	}
	if sources["audit"] == 0 {
		t.Fatalf("expected audit-era entries from graph_audit, got sources %v", sources)
	}
}

// TestGraphServer_PGCatalog_RawPathAndUnknownAliasStillRejected pins the
// binding contract around pg refs: a raw filesystem path is still rejected as
// VALIDATION and a raw "pg:..." ref passed as the catalog ARG (rather than
// bound at startup) is still UNKNOWN_CATALOG — pg support widens what an
// operator can bind, never what a tool call can smuggle in.
func TestGraphServer_PGCatalog_RawPathAndUnknownAliasStillRejected(t *testing.T) {
	openPGFixture(t, "pg-fixture-reject")
	cs, done := connectGraphServer(t, graphsrv.Config{
		CatalogFlags: []string{"pgcat=pg:pg-fixture-reject"},
	})
	defer done()

	m, isErr := callTool(t, cs, "graph.lint", map[string]any{"catalog": fixturePath})
	if !isErr {
		t.Fatalf("expected an error for a raw path arg, got %+v", m)
	}
	if code, _ := m["code"].(string); code != graphsrv.CodeValidation {
		t.Fatalf("code = %v, want %s", m["code"], graphsrv.CodeValidation)
	}

	m, isErr = callTool(t, cs, "graph.lint", map[string]any{"catalog": "pg:pg-fixture-reject"})
	if !isErr {
		t.Fatalf("expected an error for a raw pg ref arg, got %+v", m)
	}
	if code, _ := m["code"].(string); code != graphsrv.CodeUnknownCatalog {
		t.Fatalf("code = %v, want %s", m["code"], graphsrv.CodeUnknownCatalog)
	}
}
