package journal_test

// postgres_test.go ports the essential SQLite round-trip suite (sqlite_test.go)
// to the Postgres dialect: write entries, read them back, and exercise the
// checkpoint+patches resume contract. Each test gets a fresh per-test database
// from pgtest (embedded server, or KITSOKI_PG_DSN passthrough) and skips when
// no server is available in the environment.

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"kitsoki/internal/app"
	"kitsoki/internal/dbruntime/pgtest"
	"kitsoki/internal/journal"
)

// openPGTestDB opens an isolated per-test Postgres database with the journal
// table DDL applied. The DDL mirrors the journal table in
// internal/store/schema_pg.sql (which owns the shape in production; the store
// layer applies it when it opens the database).
func openPGTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := pgtest.Open(t)

	// pgx's extended protocol rejects multi-command strings, so each
	// statement executes on its own.
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS journal (
			session_id   TEXT    NOT NULL,
			turn         BIGINT  NOT NULL,
			seq          INTEGER NOT NULL,
			ts           BIGINT  NOT NULL,
			kind         TEXT    NOT NULL,
			doc          TEXT,
			doc_version  BIGINT,
			body_json    TEXT    NOT NULL,
			PRIMARY KEY (session_id, turn, seq)
		)`,
		`CREATE INDEX IF NOT EXISTS journal_doc_idx ON journal (session_id, doc, doc_version)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("DDL: %v", err)
		}
	}
	return db
}

func makePGWriter(t *testing.T, db *sql.DB) journal.Writer {
	t.Helper()
	w, err := journal.NewPostgresWriter(db)
	if err != nil {
		t.Fatalf("NewPostgresWriter: %v", err)
	}
	return w
}

func makePGReader(t *testing.T, db *sql.DB) journal.Reader {
	t.Helper()
	r, err := journal.NewPostgresReader(db)
	if err != nil {
		t.Fatalf("NewPostgresReader: %v", err)
	}
	return r
}

// TestPostgres_RoundTrip_PatchEntries writes several patch entries and reads
// them back via ReplayFrom, asserting correct ordering and count.
func TestPostgres_RoundTrip_PatchEntries(t *testing.T) {
	db := openPGTestDB(t)
	w := makePGWriter(t, db)
	r := makePGReader(t, db)

	sid := app.SessionID("pg-rt-1")
	// Insert in non-monotonic order.
	appendSQLitePatch(t, w, sid, 3, 0, "world", journal.KindWorldPatch)
	appendSQLitePatch(t, w, sid, 1, 1, "world", journal.KindWorldPatch)
	appendSQLitePatch(t, w, sid, 2, 0, "world", journal.KindWorldPatch)
	appendSQLitePatch(t, w, sid, 1, 0, "world", journal.KindWorldPatch)

	var got []journal.Entry
	seq, errFn := r.ReplayFrom(sid, "world", 1)
	for e := range seq {
		got = append(got, e)
	}
	if err := errFn(); err != nil {
		t.Fatalf("ReplayFrom: %v", err)
	}

	if len(got) != 4 {
		t.Fatalf("ReplayFrom len = %d, want 4", len(got))
	}
	// Should come back sorted by (turn, seq).
	wantOrder := [][2]int64{{1, 0}, {1, 1}, {2, 0}, {3, 0}}
	for i, e := range got {
		if int64(e.Turn) != wantOrder[i][0] || int64(e.Seq) != wantOrder[i][1] {
			t.Errorf("got[%d] = (turn=%d,seq=%d), want (%d,%d)",
				i, e.Turn, e.Seq, wantOrder[i][0], wantOrder[i][1])
		}
	}
}

// TestPostgres_CheckpointResume exercises the checkpoint+patches resume
// contract end to end: patches, a checkpoint whose version continues from
// them, patches after it, and LoadDocument/LatestCheckpoint/ReplayFrom
// agreeing on where resume starts.
func TestPostgres_CheckpointResume(t *testing.T) {
	db := openPGTestDB(t)
	w := makePGWriter(t, db)
	r := makePGReader(t, db)

	sid := app.SessionID("pg-cp-1")
	// Three patches (versions 1,2,3), then a checkpoint (version 4),
	// then two more patches (versions 5,6).
	appendSQLitePatch(t, w, sid, 1, 0, "world", journal.KindWorldPatch)
	appendSQLitePatch(t, w, sid, 2, 0, "world", journal.KindWorldPatch)
	appendSQLitePatch(t, w, sid, 3, 0, "world", journal.KindWorldPatch)

	full := json.RawMessage(`{"vars":{"gold":100}}`)
	if err := w.AppendCheckpoint(sid, 4, 0, "world", full); err != nil {
		t.Fatalf("AppendCheckpoint: %v", err)
	}

	appendSQLitePatch(t, w, sid, 5, 0, "world", journal.KindWorldPatch)
	appendSQLitePatch(t, w, sid, 6, 0, "world", journal.KindWorldPatch)

	cp, ok, err := r.LatestCheckpoint(sid, "world")
	if err != nil {
		t.Fatalf("LatestCheckpoint: %v", err)
	}
	if !ok {
		t.Fatal("LatestCheckpoint: not found")
	}
	if cp.DocVersion != 4 {
		t.Errorf("checkpoint DocVersion = %d, want 4", cp.DocVersion)
	}

	// Patches after checkpoint.
	var afterCp []journal.Entry
	seq, errFn := r.ReplayFrom(sid, "world", cp.DocVersion+1)
	for e := range seq {
		afterCp = append(afterCp, e)
	}
	if err := errFn(); err != nil {
		t.Fatalf("ReplayFrom: %v", err)
	}
	if len(afterCp) != 2 {
		t.Errorf("patches after checkpoint = %d, want 2", len(afterCp))
	}

	// LoadDocument returns the checkpoint body and the highest version (6).
	cur, ver, err := r.LoadDocument(sid, "world")
	if err != nil {
		t.Fatalf("LoadDocument: %v", err)
	}
	if string(cur) != string(full) {
		t.Errorf("LoadDocument body = %s, want %s", cur, full)
	}
	if ver != 6 {
		t.Errorf("highest version = %d, want 6", ver)
	}
}

// TestPostgres_LoadDocument_NoCheckpoint ensures LoadDocument returns
// (nil, highest-patch-version, nil) when no checkpoint exists.
func TestPostgres_LoadDocument_NoCheckpoint(t *testing.T) {
	db := openPGTestDB(t)
	w := makePGWriter(t, db)
	r := makePGReader(t, db)

	sid := app.SessionID("pg-no-cp")
	appendSQLitePatch(t, w, sid, 1, 0, "world", journal.KindWorldPatch)

	cur, ver, err := r.LoadDocument(sid, "world")
	if err != nil {
		t.Fatalf("LoadDocument: %v", err)
	}
	if cur != nil {
		t.Errorf("current = %s, want nil", cur)
	}
	if ver != 1 {
		t.Errorf("ver = %d, want 1", ver)
	}
}

// TestPostgres_DocVersion_PerDocIsolation verifies MAX+1 versions are tracked
// independently per (session, doc).
func TestPostgres_DocVersion_PerDocIsolation(t *testing.T) {
	db := openPGTestDB(t)
	w := makePGWriter(t, db)
	r := makePGReader(t, db)

	sid := app.SessionID("pg-dv-iso")
	appendSQLitePatch(t, w, sid, 1, 0, "world", journal.KindWorldPatch)
	appendSQLitePatch(t, w, sid, 1, 1, "world", journal.KindWorldPatch)
	appendSQLitePatch(t, w, sid, 1, 2, "state", journal.KindStateTransition)

	_, worldVer, err := r.LoadDocument(sid, "world")
	if err != nil {
		t.Fatalf("LoadDocument world: %v", err)
	}
	_, stateVer, err := r.LoadDocument(sid, "state")
	if err != nil {
		t.Fatalf("LoadDocument state: %v", err)
	}
	if worldVer != 2 {
		t.Errorf("world version = %d, want 2", worldVer)
	}
	if stateVer != 1 {
		t.Errorf("state version = %d, want 1", stateVer)
	}
}

// TestPostgres_ReplayTyped_FiltersCorrectly checks that ReplayTyped returns
// only non-patch, non-checkpoint entries in (turn, seq) order.
func TestPostgres_ReplayTyped_FiltersCorrectly(t *testing.T) {
	db := openPGTestDB(t)
	w := makePGWriter(t, db)
	r := makePGReader(t, db)

	sid := app.SessionID("pg-typed")
	appendSQLitePatch(t, w, sid, 1, 0, "world", journal.KindWorldPatch)
	appendSQLiteTyped(t, w, sid, 1, 1, journal.KindHostInvoked)
	appendSQLiteTyped(t, w, sid, 2, 0, journal.KindClarifyRequested)
	appendSQLitePatch(t, w, sid, 2, 1, "world", journal.KindWorldPatch)
	if err := w.AppendCheckpoint(sid, 3, 0, "world", json.RawMessage(`{"vars":{}}`)); err != nil {
		t.Fatalf("AppendCheckpoint: %v", err)
	}
	appendSQLiteTyped(t, w, sid, 3, 1, journal.KindClarifyAnswered)

	var typed []journal.Entry
	seq, errFn := r.ReplayTyped(sid)
	for e := range seq {
		typed = append(typed, e)
	}
	if err := errFn(); err != nil {
		t.Fatalf("ReplayTyped: %v", err)
	}
	if len(typed) != 3 {
		t.Fatalf("ReplayTyped len = %d, want 3", len(typed))
	}
	wantKinds := []string{
		journal.KindHostInvoked,
		journal.KindClarifyRequested,
		journal.KindClarifyAnswered,
	}
	for i, e := range typed {
		if e.Kind != wantKinds[i] {
			t.Errorf("typed[%d].Kind = %q, want %q", i, e.Kind, wantKinds[i])
		}
	}
}

// TestPostgres_ListLiveDocs returns the distinct docs for a session.
func TestPostgres_ListLiveDocs(t *testing.T) {
	db := openPGTestDB(t)
	w := makePGWriter(t, db)
	r := makePGReader(t, db)

	sid := app.SessionID("pg-docs")
	appendSQLitePatch(t, w, sid, 1, 0, "world", journal.KindWorldPatch)
	appendSQLitePatch(t, w, sid, 1, 1, "state", journal.KindStateTransition)
	appendSQLitePatch(t, w, sid, 1, 2, "chats/c1", journal.KindChatsAppend)
	appendSQLiteTyped(t, w, sid, 1, 3, journal.KindHostInvoked) // no doc

	docs := r.ListLiveDocs(sid)
	want := map[string]struct{}{"world": {}, "state": {}, "chats/c1": {}}
	if len(docs) != len(want) {
		t.Fatalf("ListLiveDocs len = %d, want %d", len(docs), len(want))
	}
	for _, d := range docs {
		if _, ok := want[string(d)]; !ok {
			t.Errorf("unexpected doc %q", d)
		}
	}
}

// TestPostgres_MultiSession verifies session isolation.
func TestPostgres_MultiSession(t *testing.T) {
	db := openPGTestDB(t)
	w := makePGWriter(t, db)
	r := makePGReader(t, db)

	s1 := app.SessionID("pg-sess-A")
	s2 := app.SessionID("pg-sess-B")

	appendSQLitePatch(t, w, s1, 1, 0, "world", journal.KindWorldPatch)
	appendSQLitePatch(t, w, s2, 1, 0, "world", journal.KindWorldPatch)
	appendSQLitePatch(t, w, s2, 2, 0, "world", journal.KindWorldPatch)

	var s1E []journal.Entry
	s1Seq, s1Err := r.ReplayFrom(s1, "world", 1)
	for e := range s1Seq {
		s1E = append(s1E, e)
	}
	if err := s1Err(); err != nil {
		t.Fatalf("ReplayFrom(s1): %v", err)
	}
	if len(s1E) != 1 {
		t.Errorf("session A entries = %d, want 1", len(s1E))
	}

	var s2E []journal.Entry
	s2Seq, s2Err := r.ReplayFrom(s2, "world", 1)
	for e := range s2Seq {
		s2E = append(s2E, e)
	}
	if err := s2Err(); err != nil {
		t.Fatalf("ReplayFrom(s2): %v", err)
	}
	if len(s2E) != 2 {
		t.Errorf("session B entries = %d, want 2", len(s2E))
	}
}

// TestPostgres_Flush_NoOp verifies Flush succeeds (it is a no-op on this
// dialect — there is no WAL-checkpoint pragma to run).
func TestPostgres_Flush_NoOp(t *testing.T) {
	db := openPGTestDB(t)
	w := makePGWriter(t, db)
	if err := w.Flush(); err != nil {
		t.Errorf("Flush: %v", err)
	}
}

// TestPostgres_AppendJournalPgTx_CommitAndRollback verifies the store-layer
// entry point: rows written inside a caller-supplied transaction are visible
// after commit and gone after rollback.
func TestPostgres_AppendJournalPgTx_CommitAndRollback(t *testing.T) {
	db := openPGTestDB(t)
	ctx := context.Background()

	sid := app.SessionID("pg-tx")
	entry := journal.Entry{
		Ts:      time.Now(),
		Session: sid,
		Turn:    1,
		Seq:     0,
		Kind:    journal.KindWorldPatch,
		Doc:     "world",
		Body:    json.RawMessage(`{"ops":[]}`),
	}

	// Rollback first: no rows survive.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := journal.AppendJournalPgTx(ctx, tx, sid, []journal.Entry{entry}); err != nil {
		_ = tx.Rollback()
		t.Fatalf("AppendJournalPgTx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	r := makePGReader(t, db)
	seq, errFn := r.ReplayFrom(sid, "world", 1)
	n := 0
	for range seq {
		n++
	}
	if err := errFn(); err != nil {
		t.Fatalf("ReplayFrom after rollback: %v", err)
	}
	if n != 0 {
		t.Fatalf("after rollback: entries = %d, want 0", n)
	}

	// Commit: the row lands with doc_version assigned MAX+1 (=1).
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := journal.AppendJournalPgTx(ctx, tx, sid, []journal.Entry{entry}); err != nil {
		_ = tx.Rollback()
		t.Fatalf("AppendJournalPgTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	var got []journal.Entry
	seq, errFn = r.ReplayFrom(sid, "world", 1)
	for e := range seq {
		got = append(got, e)
	}
	if err := errFn(); err != nil {
		t.Fatalf("ReplayFrom after commit: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("after commit: entries = %d, want 1", len(got))
	}
	if got[0].DocVersion != 1 {
		t.Errorf("DocVersion = %d, want 1", got[0].DocVersion)
	}
}

// TestPostgres_OutOfTurnSeqAutoAssign verifies that out-of-turn entries
// (Turn=0, Seq=0) get Seq auto-assigned from MAX+1 so multiple post-commit
// writes coexist on the (session_id, turn, seq) PK — the same convention the
// SQLite path applies.
func TestPostgres_OutOfTurnSeqAutoAssign(t *testing.T) {
	db := openPGTestDB(t)
	w := makePGWriter(t, db)
	r := makePGReader(t, db)

	sid := app.SessionID("pg-oot")
	for range 3 {
		e := journal.Entry{
			Ts:      time.Now(),
			Session: sid,
			Kind:    journal.KindHostInvoked,
			Body:    json.RawMessage(`{}`),
		}
		if err := w.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	var typed []journal.Entry
	seq, errFn := r.ReplayTyped(sid)
	for e := range seq {
		typed = append(typed, e)
	}
	if err := errFn(); err != nil {
		t.Fatalf("ReplayTyped: %v", err)
	}
	if len(typed) != 3 {
		t.Fatalf("out-of-turn entries = %d, want 3 (seq collisions?)", len(typed))
	}
	for i, e := range typed {
		if e.Seq != i {
			t.Errorf("typed[%d].Seq = %d, want %d", i, e.Seq, i)
		}
	}
}

// TestPostgres_NilConstructors_Errors verifies that nil db returns an error.
func TestPostgres_NilConstructors_Errors(t *testing.T) {
	t.Parallel()

	if _, err := journal.NewPostgresWriter(nil); err == nil {
		t.Error("NewPostgresWriter(nil) expected error, got nil")
	}
	if _, err := journal.NewPostgresReader(nil); err == nil {
		t.Error("NewPostgresReader(nil) expected error, got nil")
	}
}
