package pgverify_test

// pgverify_test.go exercises every check both on the happy path (against a
// real embedded Postgres via internal/dbruntime/pgtest — never a mock
// database) AND adversarially: each check is fed exactly the broken input it
// is supposed to catch, and the test asserts it actually catches it. That
// second half is the point of this package — a verification tool that only
// has happy-path tests has not proven it can tell a false green from a real
// one.

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/artifactjob"
	"kitsoki/internal/chats"
	"kitsoki/internal/dbruntime/pgtest"
	"kitsoki/internal/jobs"
	"kitsoki/internal/pgverify"
	"kitsoki/internal/store"
	"kitsoki/internal/study"
	"kitsoki/internal/webauth"
)

// ─── test scaffolding ───────────────────────────────────────────────────────

// openPG returns a fresh embedded-Postgres *sql.DB (pgtest) with the main
// session-store schema applied.
func openPG(t *testing.T) *sql.DB {
	t.Helper()
	db := pgtest.Open(t)
	_, err := store.OpenPostgres(db)
	require.NoError(t, err)
	return db
}

// openSvc wraps db as a store.Store the way a live kitsoki process would
// (store.OpenPostgres), for use as Options.OpenServiceStore.
func openSvcFactory(db *sql.DB) func() (store.Store, error) {
	return func() (store.Store, error) { return store.OpenPostgres(db) }
}

// createAllSatelliteSchemas applies every satellite schema dbmigrate's
// PreflightShared expects to exist (artifactjob, jobs, chats, study,
// webauth), mirroring cmd/kitsoki/db_migrate.go's setup. Needed because
// dbmigrate.PreflightShared errors on the first missing table across ALL of
// internal/dbmigrate's AllSharedFileSpecs(), not just the ones a given test
// cares about.
func createAllSatelliteSchemas(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := artifactjob.NewPostgresStore(db)
	require.NoError(t, err)
	_, err = jobs.NewJobStore(db, jobs.WithDialect(jobs.DialectPostgres))
	require.NoError(t, err)
	_, err = chats.NewPostgresStore(db)
	require.NoError(t, err)
	_, err = study.NewPostgresStore(db)
	require.NoError(t, err)
	_, err = webauth.NewPostgresStore(db)
	require.NoError(t, err)
}

// simulateServiceConnection reserves one dedicated backend connection on db
// and stamps its application_name to store.ApplicationName, modeling "a live
// kitsoki process is connected" for CheckBackendIdentity without needing a
// second real process. The returned func releases it.
func simulateServiceConnection(t *testing.T, db *sql.DB) func() {
	t.Helper()
	conn, err := db.Conn(context.Background())
	require.NoError(t, err)
	_, err = conn.ExecContext(context.Background(), `SET application_name = `+"'"+store.ApplicationName+"'")
	require.NoError(t, err)
	return func() { _ = conn.Close() }
}

func fakeOpts() pgverify.Options {
	return pgverify.Options{
		Now:   func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
		Sleep: func(time.Duration) {}, // instant in tests unless a case overrides it
	}
}

// ─── 1. Backend identity ────────────────────────────────────────────────────

func TestCheckBackendIdentity_Pass_WhenLiveKitsokiConnectionExists(t *testing.T) {
	db := openPG(t)
	release := simulateServiceConnection(t, db)
	defer release()

	res := pgverify.CheckBackendIdentity(context.Background(), db)
	require.Equal(t, pgverify.StatusPass, res.Status, "detail: %s", res.Detail)
	require.Equal(t, 1, res.Evidence["connection_count"])
}

func TestCheckBackendIdentity_Fail_WhenNoKitsokiConnectionExists(t *testing.T) {
	db := openPG(t)
	// Deliberately no simulated connection — models a host where the
	// process either isn't running, is still SQLite-backed, or is pointed
	// at a different database entirely.
	res := pgverify.CheckBackendIdentity(context.Background(), db)
	require.Equal(t, pgverify.StatusFail, res.Status)
	require.Equal(t, 0, res.Evidence["connection_count"])
}

func TestCheckBackendIdentity_NotEstablished_WhenNoHandle(t *testing.T) {
	res := pgverify.CheckBackendIdentity(context.Background(), nil)
	require.Equal(t, pgverify.StatusNotEstablished, res.Status)
}

// ─── 2 & 3. Round trip + direct rows ────────────────────────────────────────

func TestCheckRoundTrip_Pass_RealStore(t *testing.T) {
	db := openPG(t)
	svc, err := store.OpenPostgres(db)
	require.NoError(t, err)
	defer svc.Close()

	roundtrip, directRows, cleanup := pgverify.CheckRoundTrip(context.Background(), svc, db, false)
	defer cleanup()

	require.Equal(t, pgverify.StatusPass, roundtrip.Status, "detail: %s", roundtrip.Detail)
	require.Equal(t, pgverify.StatusPass, directRows.Status, "detail: %s", directRows.Detail)

	// Cleanup actually removes the session (never leaves test data behind).
	cleanup()
	sid := app.SessionID(roundtrip.Evidence["session_id"].(string))
	_, err = svc.GetSession(context.Background(), sid)
	require.ErrorIs(t, err, store.ErrSessionNotFound, "cleanup must delete the session it created")
}

// fakeBrokenStore implements store.Store but LIES about what it persisted:
// AppendEvents/CreateSession report success while never touching Postgres,
// and LoadHistory returns different data than was "written". This proves
// CheckRoundTrip does not just trust a green store.Store return value.
type fakeBrokenStore struct {
	store.Store // embed nil; every method must be overridden below or it panics loudly, which is fine for a test double
	created     app.SessionID
}

func (f *fakeBrokenStore) CreateSession(ctx context.Context, def *app.AppDef) (app.SessionID, error) {
	f.created = "fake-session-never-in-postgres"
	return f.created, nil
}
func (f *fakeBrokenStore) AppendEvents(session app.SessionID, events []store.Event) error {
	return nil // silently drops the write
}
func (f *fakeBrokenStore) LoadHistory(session app.SessionID) (store.History, error) {
	// Returns a DIFFERENT payload than what CheckRoundTrip wrote.
	payload, _ := json.Marshal(map[string]string{"marker": "not-the-real-marker", "nonce": "wrong-nonce"})
	return store.History{{Turn: 1, Seq: 0, Kind: store.TransitionApplied, Payload: payload}}, nil
}
func (f *fakeBrokenStore) DeleteSession(ctx context.Context, session app.SessionID) error { return nil }

func TestCheckRoundTrip_Fail_WhenServiceReturnsWrongData(t *testing.T) {
	db := openPG(t) // real Postgres for the direct-row half; the fake never writes to it
	fake := &fakeBrokenStore{}

	roundtrip, directRows, cleanup := pgverify.CheckRoundTrip(context.Background(), fake, db, false)
	defer cleanup()

	require.Equal(t, pgverify.StatusFail, roundtrip.Status, "must catch a nonce mismatch, not just a store error")
	// The session was never actually written to Postgres, so the direct-row
	// re-derivation cannot find it either — a real, honest negative.
	require.NotEqual(t, pgverify.StatusPass, directRows.Status)
}

func TestCheckRoundTrip_NotEstablished_WhenNoServiceConfigured(t *testing.T) {
	roundtrip, directRows, cleanup := pgverify.CheckRoundTrip(context.Background(), nil, nil, false)
	defer cleanup()
	require.Equal(t, pgverify.StatusNotEstablished, roundtrip.Status)
	require.Equal(t, pgverify.StatusNotEstablished, directRows.Status)
}

// ─── 4. SQLite inert ─────────────────────────────────────────────────────

func TestCheckSQLiteInert_Pass_WhenUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.db")
	require.NoError(t, os.WriteFile(path, []byte("stable content"), 0o600))

	opts := fakeOpts()
	res := pgverify.CheckSQLiteInert(path, pgverify.StatSQLiteFiles(path), opts)
	require.Equal(t, pgverify.StatusPass, res.Status, "detail: %s", res.Detail)
}

func TestCheckSQLiteInert_Pass_WhenAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.db")

	opts := fakeOpts()
	res := pgverify.CheckSQLiteInert(path, pgverify.StatSQLiteFiles(path), opts)
	require.Equal(t, pgverify.StatusPass, res.Status, "detail: %s", res.Detail)
}

func TestCheckSQLiteInert_Fail_WhenActivelyWrittenDuringWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.db")
	require.NoError(t, os.WriteFile(path, []byte("v1"), 0o600))

	opts := fakeOpts()
	// The "sleep" IS the write in this test: deterministic, no real
	// wall-clock wait, but exercises the exact same before/after compare.
	opts.Sleep = func(time.Duration) {
		require.NoError(t, os.WriteFile(path, []byte("v2-still-being-written"), 0o600))
	}
	res := pgverify.CheckSQLiteInert(path, pgverify.StatSQLiteFiles(path), opts)
	require.Equal(t, pgverify.StatusFail, res.Status)
}

func TestCheckSQLiteInert_Fail_WhenWALGrows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.db")
	require.NoError(t, os.WriteFile(path, []byte("stable"), 0o600))

	opts := fakeOpts()
	opts.Sleep = func(time.Duration) {
		// Main file untouched, but the WAL sidecar grows — still a real
		// active-write signal (SQLite writes there before checkpointing).
		require.NoError(t, os.WriteFile(path+"-wal", []byte("wal frame"), 0o600))
	}
	res := pgverify.CheckSQLiteInert(path, pgverify.StatSQLiteFiles(path), opts)
	require.Equal(t, pgverify.StatusFail, res.Status)
}

func TestCheckSQLiteInert_NotEstablished_WhenNoPathGiven(t *testing.T) {
	res := pgverify.CheckSQLiteInert("", pgverify.StatSQLiteFiles(""), fakeOpts())
	require.Equal(t, pgverify.StatusNotEstablished, res.Status)
}

// ─── 5. Migrated data intact ───────────────────────────────────────────────

func seedRawSession(t *testing.T, db *sql.DB, appID string, eventCount int) string {
	t.Helper()
	sid := "migrated-" + appID
	_, err := db.Exec(`INSERT INTO sessions (id, app_id, app_version, started_at, last_turn, status) VALUES ($1,$2,'1.0',0,0,'active')`, sid, appID)
	require.NoError(t, err)
	for i := 0; i < eventCount; i++ {
		_, err := db.Exec(`INSERT INTO events (session_id, turn, seq, ts, kind, payload_json) VALUES ($1,1,$2,0,'transition.applied','{}')`, sid, i)
		require.NoError(t, err)
	}
	return sid
}

func TestCheckMigratedDataIntact_Pass_CountsAndSample(t *testing.T) {
	db := openPG(t)
	createAllSatelliteSchemas(t, db)
	svc, err := store.OpenPostgres(db)
	require.NoError(t, err)
	defer svc.Close()

	sid := seedRawSession(t, db, "real-preexisting-app", 5)

	opts := fakeOpts()
	opts.ExpectedCounts = map[string]int64{"store.sessions": 1, "store.events": 5}
	opts.SampleSessionIDs = []string{sid}

	res := pgverify.CheckMigratedDataIntact(context.Background(), svc, db, opts)
	require.Equal(t, pgverify.StatusPass, res.Status, "detail: %s", res.Detail)
}

func TestCheckMigratedDataIntact_Fail_WhenCountBelowExpected(t *testing.T) {
	db := openPG(t)
	createAllSatelliteSchemas(t, db)
	svc, err := store.OpenPostgres(db)
	require.NoError(t, err)
	defer svc.Close()

	seedRawSession(t, db, "real-preexisting-app-2", 2)

	opts := fakeOpts()
	// Deliberately way above what was actually seeded — models a
	// migration that silently dropped rows.
	opts.ExpectedCounts = map[string]int64{"store.events": 999999}

	res := pgverify.CheckMigratedDataIntact(context.Background(), svc, db, opts)
	require.Equal(t, pgverify.StatusFail, res.Status)
}

func TestCheckMigratedDataIntact_Fail_WhenExpectedLabelUnknown(t *testing.T) {
	db := openPG(t)
	createAllSatelliteSchemas(t, db)
	svc, err := store.OpenPostgres(db)
	require.NoError(t, err)
	defer svc.Close()

	opts := fakeOpts()
	opts.ExpectedCounts = map[string]int64{"nonexistent.table": 1}

	res := pgverify.CheckMigratedDataIntact(context.Background(), svc, db, opts)
	require.Equal(t, pgverify.StatusFail, res.Status)
}

func TestCheckMigratedDataIntact_NotEstablished_WhenNothingToCompare(t *testing.T) {
	db := openPG(t)
	svc, err := store.OpenPostgres(db)
	require.NoError(t, err)
	defer svc.Close()

	res := pgverify.CheckMigratedDataIntact(context.Background(), svc, db, fakeOpts())
	require.Equal(t, pgverify.StatusNotEstablished, res.Status)
}

// ─── 6. stream_pos monotonicity under concurrency ──────────────────────────

func TestCheckStreamMonotonicity_Pass_RealConcurrentAppends(t *testing.T) {
	db := openPG(t)
	svc, err := store.OpenPostgres(db)
	require.NoError(t, err)
	defer svc.Close()

	opts := fakeOpts()
	opts.ConcurrencyWriters = 4
	opts.ConcurrencyAppendsPerWriter = 8
	opts.ConcurrencyEventsPerAppend = 3
	opts.AssumeExclusiveWindow = true // nothing else is writing to this fresh test database

	res := pgverify.CheckStreamMonotonicity(context.Background(), svc, db, opts)
	require.Equal(t, pgverify.StatusPass, res.Status, "detail: %s", res.Detail)
	require.Equal(t, 4*8*3, res.Evidence["total_events"])
}

// gapInjectingStream wraps a real store.Store + EventStream but drops one
// entry from the FIRST page ReadSessionStream ever returns, simulating a
// stream reader that silently misses a row (the exact failure mode the
// advisory-lock ordering in postgres_stream.go exists to prevent). Proves
// CheckStreamMonotonicity notices a gap instead of only checking totals in a
// way that could paper over one dropped + one duplicated row.
type gapInjectingStream struct {
	store.Store
	inner   store.EventStream
	dropped bool
}

func (g *gapInjectingStream) ReadStream(ctx context.Context, after store.StreamCursor, limit int) ([]store.StreamEntry, error) {
	return g.inner.ReadStream(ctx, after, limit)
}
func (g *gapInjectingStream) ReadSessionStream(ctx context.Context, session app.SessionID, after store.StreamCursor, limit int) ([]store.StreamEntry, error) {
	page, err := g.inner.ReadSessionStream(ctx, session, after, limit)
	if err != nil || len(page) == 0 || g.dropped {
		return page, err
	}
	g.dropped = true
	return page[1:], nil // silently drop the first entry once
}
func (g *gapInjectingStream) WaitForEvents(ctx context.Context, after store.StreamCursor) error {
	return g.inner.WaitForEvents(ctx, after)
}

func TestCheckStreamMonotonicity_Fail_WhenAReaderMissesARow(t *testing.T) {
	db := openPG(t)
	svc, err := store.OpenPostgres(db)
	require.NoError(t, err)
	defer svc.Close()

	stream, ok := store.AsEventStream(svc)
	require.True(t, ok)
	broken := &gapInjectingStream{Store: svc, inner: stream}

	opts := fakeOpts()
	opts.ConcurrencyWriters = 2
	opts.ConcurrencyAppendsPerWriter = 4
	opts.ConcurrencyEventsPerAppend = 2

	res := pgverify.CheckStreamMonotonicity(context.Background(), broken, db, opts)
	require.Equal(t, pgverify.StatusFail, res.Status, "must detect a silently dropped row, not just check totals")
}

func TestCheckStreamMonotonicity_NotEstablished_WhenStoreIsNotAStream(t *testing.T) {
	mem, err := store.OpenMemory()
	require.NoError(t, err)
	defer mem.Close()

	res := pgverify.CheckStreamMonotonicity(context.Background(), mem, nil, fakeOpts())
	require.Equal(t, pgverify.StatusNotEstablished, res.Status)
}

// ─── 7. Type fidelity ───────────────────────────────────────────────────────

func TestCheckTypeFidelity_Pass(t *testing.T) {
	db := openPG(t)
	svc, err := store.OpenPostgres(db)
	require.NoError(t, err)
	defer svc.Close()

	res := pgverify.CheckTypeFidelity(context.Background(), svc, db, false)
	require.Equal(t, pgverify.StatusPass, res.Status, "detail: %s", res.Detail)
}

func TestCheckTypeFidelity_Fail_WhenBlobTruncated(t *testing.T) {
	db := openPG(t)
	_, err := db.Exec(`CREATE SCHEMA IF NOT EXISTS study`)
	require.NoError(t, err)
	// A BYTEA column too narrow to hold the probe payload models a
	// truncation bug: the check must catch a short read-back, not just "no
	// error".
	_, err = db.Exec(`DROP TABLE IF EXISTS study.governed_studies`)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE study.governed_studies (id TEXT PRIMARY KEY, idempotency_key TEXT NOT NULL UNIQUE, payload BYTEA NOT NULL)`)
	require.NoError(t, err)

	// Simulate truncation via a BEFORE INSERT trigger that chops the payload
	// to 4 bytes — deterministic, no dependency on driver-level mangling.
	_, err = db.Exec(`CREATE OR REPLACE FUNCTION pgverify_test_truncate() RETURNS trigger AS $$
	BEGIN
	  NEW.payload := substring(NEW.payload for 4);
	  RETURN NEW;
	END; $$ LANGUAGE plpgsql`)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TRIGGER pgverify_truncate BEFORE INSERT ON study.governed_studies FOR EACH ROW EXECUTE FUNCTION pgverify_test_truncate()`)
	require.NoError(t, err)

	res := pgverify.CheckTypeFidelity(context.Background(), nil, db, false)
	require.Equal(t, pgverify.StatusFail, res.Status)
}

func TestCheckTypeFidelity_NotEstablished_WhenNoHandle(t *testing.T) {
	res := pgverify.CheckTypeFidelity(context.Background(), nil, nil, false)
	require.Equal(t, pgverify.StatusNotEstablished, res.Status)
}

// ─── Run() orchestration ────────────────────────────────────────────────────

func TestRun_HappyPath_EveryCheckPasses(t *testing.T) {
	db := openPG(t)
	createAllSatelliteSchemas(t, db)
	release := simulateServiceConnection(t, db)
	defer release()

	sid := seedRawSession(t, db, "real-preexisting-app", 3)

	opts := fakeOpts()
	opts.PGDB = db
	opts.OpenServiceStore = openSvcFactory(db)
	opts.SQLitePath = filepath.Join(t.TempDir(), "sessions.db") // absent -> retired, StatusPass
	opts.ExpectedCounts = map[string]int64{"store.sessions": 1, "store.events": 3}
	opts.SampleSessionIDs = []string{sid}
	opts.ConcurrencyWriters = 3
	opts.ConcurrencyAppendsPerWriter = 4
	opts.ConcurrencyEventsPerAppend = 2

	report, err := pgverify.Run(context.Background(), opts)
	require.NoError(t, err)

	for _, c := range report.Checks {
		require.Equalf(t, pgverify.StatusPass, c.Status, "check %s (%s): %s", c.ID, c.Name, c.Detail)
	}
	require.True(t, report.OK())
	require.Equal(t, 0, report.ExitCode())
	require.Len(t, report.Checks, 7)
}

func TestRun_FailClosed_WhenNothingConfigured(t *testing.T) {
	report, err := pgverify.Run(context.Background(), fakeOpts())
	require.NoError(t, err)
	require.False(t, report.OK(), "an unconfigured run must never report green")
	require.Equal(t, 1, report.ExitCode())
	for _, c := range report.Checks {
		require.NotEqual(t, pgverify.StatusPass, c.Status, "check %s must not pass with nothing configured", c.ID)
	}
}

func TestRun_JSONRoundTrip(t *testing.T) {
	db := openPG(t)
	release := simulateServiceConnection(t, db)
	defer release()

	opts := fakeOpts()
	opts.PGDB = db
	opts.OpenServiceStore = openSvcFactory(db)

	report, err := pgverify.Run(context.Background(), opts)
	require.NoError(t, err)

	b, err := json.Marshal(report)
	require.NoError(t, err)
	var decoded pgverify.Report
	require.NoError(t, json.Unmarshal(b, &decoded))
	require.Len(t, decoded.Checks, len(report.Checks))
}
