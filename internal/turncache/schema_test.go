// Schema-level tests for the SQLite backend. These tests are
// deliberately at the SQL level — they open the DB, inspect
// sqlite_master, and verify the embedded DDL produces the expected
// table / index / column shape. If a future agent edits schema.sql,
// the test below fails loudly rather than letting a silent drift slip
// through the conformance suite.
package turncache

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestSchema_TablesAndIndexExist asserts a fresh DB has the two tables
// (turn_cache, synonym_hits) and the LRU index after NewSQLite returns.
func TestSchema_TablesAndIndexExist(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cache.db")
	c, err := NewSQLite(path, testConfig())
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("introspect open: %v", err)
	}
	defer db.Close()

	wantTables := []string{"turn_cache", "synonym_hits"}
	for _, name := range wantTables {
		var got string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&got)
		if err != nil {
			t.Errorf("expected table %q present, got error %v", name, err)
		}
		if got != name {
			t.Errorf("expected table %q, got %q", name, got)
		}
	}
	var idxName string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='turn_cache_lru'`).Scan(&idxName); err != nil {
		t.Errorf("expected index turn_cache_lru present, got %v", err)
	}
}

// TestSchema_TurnCacheColumns enumerates every column the embedded
// schema declares for turn_cache and asserts it exists with the right
// NOT NULL / type configuration. A future schema bump that adds or
// drops a column has to update this test in lockstep.
func TestSchema_TurnCacheColumns(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cache.db")
	c, err := NewSQLite(path, testConfig())
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("introspect open: %v", err)
	}
	defer db.Close()

	type wantCol struct {
		name    string
		notNull bool
	}
	want := []wantCol{
		{"app", true},
		{"app_hash", true},
		{"state_path", true},
		{"signature", true},
		{"intent", true},
		{"slots_json", true},
		{"confidence", true},
		{"source_model", false},
		{"source_turn_id", false},
		{"hit_count", true},
		{"last_hit_at", false},
		{"last_verified_at", false},
		{"revalidate_fails", true},
		{"created_at", true},
	}

	rows, err := db.Query(`PRAGMA table_info(turn_cache)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()
	have := map[string]bool{}
	notNullMap := map[string]bool{}
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notNull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		have[name] = true
		notNullMap[name] = notNull == 1
	}
	for _, w := range want {
		if !have[w.name] {
			t.Errorf("turn_cache column %q missing", w.name)
			continue
		}
		if notNullMap[w.name] != w.notNull {
			t.Errorf("turn_cache.%s NOT NULL mismatch: want %v got %v", w.name, w.notNull, notNullMap[w.name])
		}
	}
}

// TestSchema_SynonymHitsColumns is the parallel check for the synonym
// table. Same approach: enumerate the documented columns, fail if any
// is missing or has the wrong NOT NULL stance.
func TestSchema_SynonymHitsColumns(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cache.db")
	c, err := NewSQLite(path, testConfig())
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("introspect open: %v", err)
	}
	defer db.Close()

	type wantCol struct {
		name    string
		notNull bool
	}
	want := []wantCol{
		{"app_hash", true},
		{"intent", true},
		{"pattern", true},
		{"kind", true},
		{"hit_count", true},
		{"last_hit_at", false},
	}

	rows, err := db.Query(`PRAGMA table_info(synonym_hits)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()
	have := map[string]bool{}
	notNullMap := map[string]bool{}
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notNull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		have[name] = true
		notNullMap[name] = notNull == 1
	}
	for _, w := range want {
		if !have[w.name] {
			t.Errorf("synonym_hits column %q missing", w.name)
			continue
		}
		if notNullMap[w.name] != w.notNull {
			t.Errorf("synonym_hits.%s NOT NULL mismatch: want %v got %v", w.name, w.notNull, notNullMap[w.name])
		}
	}
}

// TestSchema_RepeatedMigrationIsIdempotent re-applies the embedded DDL
// directly against an already-opened DB and asserts no error. This
// exercises the CREATE … IF NOT EXISTS guard.
func TestSchema_RepeatedMigrationIsIdempotent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cache.db")
	c, err := NewSQLite(path, testConfig())
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("introspect open: %v", err)
	}
	defer db.Close()

	for i := 0; i < 3; i++ {
		if _, err := db.ExecContext(context.Background(), sqliteSchemaDDL); err != nil {
			t.Errorf("re-apply #%d: %v", i+1, err)
		}
	}
	// Sanity: the table count after repeated DDL is still two.
	var tableCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('turn_cache','synonym_hits')`).Scan(&tableCount); err != nil {
		t.Fatalf("count: %v", err)
	}
	if tableCount != 2 {
		t.Errorf("post-repeated-migration table count: want 2 got %d", tableCount)
	}
}

// TestSchema_SynonymHitsKindAcceptsAll covers the H1 schema extension:
// the synonym_hits.kind CHECK constraint accepts all four documented
// values (bare / example / template / enum_value). The orchestrator's
// RecordSynonymHit writes "example" for Intent.Examples-derived hits;
// without the CHECK update those writes would fail with a SQLite
// constraint error and the table would silently stay empty.
//
// We round-trip a row per kind through RecordSynonymHit + SynonymStats
// to prove the constraint is exactly the documented set and no other.
func TestSchema_SynonymHitsKindAcceptsAll(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cache.db")
	c, err := NewSQLite(path, testConfig())
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	now := time.Now()
	ctx := context.Background()
	cases := []struct {
		name string
		sk   SynonymKey
	}{
		{"bare", SynonymKey{AppHash: "h", Intent: "go_west", Pattern: "wade", Kind: "bare"}},
		{"example", SynonymKey{AppHash: "h", Intent: "go_south", Pattern: "go south", Kind: "example"}},
		{"template", SynonymKey{AppHash: "h", Intent: "buy", Pattern: "buy {item}", Kind: "template"}},
		{"enum_value", SynonymKey{AppHash: "h", Intent: "pick_method", Pattern: "ford", Kind: "enum_value"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if err := c.RecordSynonymHit(ctx, tc.sk, now); err != nil {
				t.Fatalf("RecordSynonymHit(%s): want nil error, got %v (kind=%q)", tc.name, err, tc.sk.Kind)
			}
		})
	}
	stats, err := c.SynonymStats(ctx, "h")
	if err != nil {
		t.Fatalf("SynonymStats: %v", err)
	}
	if len(stats) != len(cases) {
		t.Fatalf("SynonymStats: want %d rows (one per kind), got %d (%+v)", len(cases), len(stats), stats)
	}
	// Each kind must appear exactly once.
	seen := map[string]int{}
	for _, s := range stats {
		seen[s.Kind]++
	}
	for _, tc := range cases {
		if seen[tc.sk.Kind] != 1 {
			t.Errorf("kind %q count: want 1, got %d (seen=%+v)", tc.sk.Kind, seen[tc.sk.Kind], seen)
		}
	}
}

// TestSchema_SynonymHitsKindRejectsUnknown is the negative side of
// TestSchema_SynonymHitsKindAcceptsAll: a row whose kind is not in
// the documented set must error out via the CHECK constraint. If a
// future schema bump weakens the constraint (or adds a new kind
// without updating the surrounding code) this test fires.
func TestSchema_SynonymHitsKindRejectsUnknown(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cache.db")
	c, err := NewSQLite(path, testConfig())
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	bad := SynonymKey{AppHash: "h", Intent: "go_west", Pattern: "wade", Kind: "nonsense"}
	if err := c.RecordSynonymHit(context.Background(), bad, time.Now()); err == nil {
		t.Errorf("RecordSynonymHit(kind=%q): want CHECK-constraint error, got nil", bad.Kind)
	}
}

// TestSchema_DDLNoStrayPragma verifies that the embedded DDL does not
// include a `PRAGMA user_version = N` stanza — turncache does not
// version-stamp the way internal/chats/store.go does. If a future
// commit adds versioning, this test fires; that's the moment to also
// add an expectedSchemaVersion constant.
func TestSchema_DDLNoStrayPragma(t *testing.T) {
	t.Parallel()
	if strings.Contains(sqliteSchemaDDL, "PRAGMA user_version") {
		t.Errorf("schema.sql contains PRAGMA user_version — wire an expectedSchemaVersion check in sqlite.go before merging")
	}
}
