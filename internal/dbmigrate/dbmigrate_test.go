package dbmigrate_test

// Round-trip and resumability coverage for internal/dbmigrate, built the same
// way the phase 1-3 tests exercise the Postgres backend: a real embedded
// Postgres database per test via internal/dbruntime/pgtest (no network
// service, no external process, skips cleanly when no server can start in
// this environment). The SQLite source is a real on-disk file built through
// each subsystem's own public write API — the same construction path
// db_backend.go uses in production — so the seeded data is exactly what a
// live deployment would have written, not a hand-rolled fixture.

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/artifactjob"
	"kitsoki/internal/chats"
	"kitsoki/internal/dbmigrate"
	"kitsoki/internal/dbruntime/pgtest"
	"kitsoki/internal/jobs"
	"kitsoki/internal/journal"
	"kitsoki/internal/store"
	"kitsoki/internal/study"
	"kitsoki/internal/webauth"

	_ "modernc.org/sqlite"
)

// seeded pins every id/value this test package writes into the source, so
// assertions after migration can compare against known values rather than
// re-deriving them from the write calls.
type seeded struct {
	sessionA, sessionB app.SessionID
	jobID              artifactjob.JobID
	schedJobID         jobs.JobID
	notificationID     string
	chatID             string
	studyID            string
	githubUserID       int64
	sessionToken       string
}

// buildSource creates a fresh SQLite file at dir/sessions.db and populates it
// through the main store plus all five shared-file satellites, exactly the
// way a live `kitsoki web`/session-serving process would (db_backend.go's
// dispatch: every satellite shares the main store's *sql.DB).
func buildSource(t *testing.T, dir string) (path string, data seeded) {
	t.Helper()
	path = filepath.Join(dir, "sessions.db")

	st, err := store.Open(path)
	require.NoError(t, err)
	db := st.DB()

	def := &app.AppDef{App: app.AppMeta{ID: "dbmigrate-test", Version: "1.0.0"}}
	sidA, err := st.CreateSession(context.Background(), def)
	require.NoError(t, err)
	sidB, err := st.CreateSession(context.Background(), def)
	require.NoError(t, err)
	data.sessionA, data.sessionB = sidA, sidB

	// Write three events (two sessions) then force their ts columns to a
	// chosen out-of-order-of-insertion sequence: AppendEvents always stamps
	// ts as time.Now() (the Event.Ts a caller supplies is not persisted), so
	// controlling read order deterministically means rewriting ts directly,
	// the same way a real deployment's events accumulate ts values no
	// caller controls. This is what exercises the migrated events table's
	// global (ts, session_id, turn, seq) ordering — real reordering, not
	// accidentally identical to insertion order.
	mkEvent := func(turn app.TurnNumber) store.Event {
		return store.Event{Turn: turn, Kind: store.TransitionApplied, Payload: []byte(`{"from":"a","to":"b"}`)}
	}
	require.NoError(t, st.AppendEvents(sidB, []store.Event{mkEvent(0)}))
	require.NoError(t, st.AppendEvents(sidA, []store.Event{mkEvent(0)}))
	require.NoError(t, st.AppendEventsAndJournal(sidA,
		[]store.Event{mkEvent(1)},
		[]journal.Entry{{Session: sidA, Turn: 1, Seq: 0, Kind: "test.patch", Doc: "world", DocVersion: 1, Body: []byte(`{"op":"add"}`)}},
	))
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	setTs := func(session app.SessionID, turn app.TurnNumber, ts time.Time) {
		_, err := db.Exec(`UPDATE events SET ts = ? WHERE session_id = ? AND turn = ?`, ts.UnixMicro(), string(session), int64(turn))
		require.NoError(t, err)
	}
	setTs(sidB, 0, base.Add(3*time.Second)) // read order: 3rd
	setTs(sidA, 0, base.Add(1*time.Second)) // read order: 1st
	setTs(sidA, 1, base.Add(2*time.Second)) // read order: 2nd

	require.NoError(t, st.Snapshot(sidA, 1, store.Snapshot{StatePath: "@start", WorldJSON: []byte(`{}`), RNGSeed: 42}))
	require.NoError(t, st.BindExternalKey(context.Background(), sidA, "jira", "PROJ-1"))

	ajStore, err := artifactjob.NewSQLiteStore(db)
	require.NoError(t, err)
	job, err := ajStore.Register(context.Background(), artifactjob.RegisterRequest{
		ID:        "job-1",
		SessionID: sidA,
		AppID:     "dbmigrate-test",
		Story:     "stories/dbmigrate-test",
		Origin:    artifactjob.Origin{Kind: "dev-story", Ref: "design:dbmigrate-test"},
		Summary:   "seed job",
		Phase:     "phase_1",
	})
	require.NoError(t, err)
	data.jobID = job.ID
	require.NoError(t, ajStore.UpsertRun(context.Background(), artifactjob.Run{
		JobID: job.ID, SessionID: sidA, Story: "stories/dbmigrate-test",
		Status: artifactjob.StatusRunning, StartedAt: base, TracePath: "trace.jsonl",
	}))
	require.NoError(t, ajStore.UpsertArtifact(context.Background(), artifactjob.Artifact{
		Handle: "artifact-1", JobID: job.ID, Kind: "log", MIME: "text/plain",
		Path: "artifact.log", CreatedAt: base,
	}))

	jobStore, err := jobs.NewJobStore(db)
	require.NoError(t, err)
	schedJobID := jobs.JobID("sched-job-1")
	require.NoError(t, jobStore.UpsertJob(context.Background(), &jobs.Job{
		ID: schedJobID, SessionID: sidA, Kind: "host.run_tests", Status: jobs.JobStatus("running"),
		OriginState: "@start", Payload: map[string]any{"cmd": "go test"}, CreatedAt: base, UpdatedAt: base,
	}))
	data.schedJobID = schedJobID
	notif := &jobs.Notification{
		ID: "notif-1", SessionID: sidA, CreatedAt: base, Severity: jobs.SeverityInfo,
		Title: "job done", OriginKind: "job", OriginRef: "job:" + string(schedJobID),
	}
	require.NoError(t, jobStore.InsertNotification(context.Background(), notif))
	data.notificationID = notif.ID

	chatStore, err := chats.NewStore(db)
	require.NoError(t, err)
	chat, err := chatStore.Create(context.Background(), "dbmigrate-test", "agent", "", "seed chat")
	require.NoError(t, err)
	data.chatID = chat.ID
	_, err = chatStore.AppendMessage(context.Background(), chat.ID, "user", "hello", nil)
	require.NoError(t, err)
	_, err = chatStore.Enqueue(context.Background(), chats.EnqueueOptions{
		ChatID: chat.ID, Transport: chats.DriveTransport("cli"), Payload: "do the thing",
	})
	require.NoError(t, err)

	studyStore, err := study.NewSQLiteStore(db)
	require.NoError(t, err)
	stdy, _, err := studyStore.Submit(context.Background(), study.SubmitRequest{
		IdempotencyKey: "idem-1",
		Plan: study.Plan{
			Revision: "r1", Digest: "d1",
			Waves:  []study.Wave{{ID: "wave-1", Order: 0, Cells: []study.CellPlan{{ID: "cell-1"}}}},
			Budget: study.Budget{Limit: 10, Currency: "USD"},
		},
	})
	require.NoError(t, err)
	data.studyID = stdy.ID

	authStore, err := webauth.NewStore(db)
	require.NoError(t, err)
	_, plainCode, err := authStore.CreateInvite(context.Background(), "alice", webauth.RoleUser)
	require.NoError(t, err)
	user, err := authStore.RedeemInvite(context.Background(), plainCode, webauth.GitHubUser{ID: 4242, Login: "alice"})
	require.NoError(t, err)
	data.githubUserID = user.GitHubID
	token, err := authStore.CreateSession(context.Background(), user.ID, time.Hour)
	require.NoError(t, err)
	data.sessionToken = token

	require.NoError(t, st.Close())
	return path, data
}

// openSource opens path read-only-ish, matching cmd/kitsoki's
// openReadOnlySQLite: a fresh *sql.DB, never the one buildSource wrote
// through.
func openSource(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// prepareDest creates every destination schema Migrate expects to already
// exist, exactly as cmd/kitsoki/db_migrate.go's runDBMigrate does before
// calling dbmigrate.Migrate.
func prepareDest(t *testing.T) *sql.DB {
	t.Helper()
	db := pgtest.Open(t)
	_, err := store.OpenPostgres(db)
	require.NoError(t, err)
	_, err = artifactjob.NewPostgresStore(db)
	require.NoError(t, err)
	_, err = jobs.NewJobStore(db, jobs.WithDialect(jobs.DialectPostgres))
	require.NoError(t, err)
	_, err = chats.NewPostgresStore(db)
	require.NoError(t, err)
	_, err = study.NewPostgresStore(db)
	require.NoError(t, err)
	_, err = webauth.NewPostgresStore(db)
	require.NoError(t, err)
	return db
}

func TestMigrate_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path, data := buildSource(t, dir)
	destDB := prepareDest(t)
	srcDB := openSource(t, path)

	report := dbmigrate.Migrate(context.Background(), srcDB, destDB, dbmigrate.Options{})
	require.True(t, report.OK(), "report: %+v, err: %v", report.Tables, report.Err())

	// ── main store, through store.Store's public API ──────────────────────
	destStore, err := store.OpenPostgres(destDB)
	require.NoError(t, err)

	hist, err := destStore.LoadHistory(data.sessionA)
	require.NoError(t, err)
	require.Empty(t, hist, "LoadHistory returns events after the latest snapshot (turn 1); the only sessionA event is AT turn 1")

	snap, ok, err := destStore.LatestSnapshot(data.sessionA)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int64(42), snap.RNGSeed)

	sess, err := destStore.LookupByKey(context.Background(), "jira", "PROJ-1")
	require.NoError(t, err)
	require.Equal(t, data.sessionA, sess)

	summary, err := destStore.GetSession(context.Background(), data.sessionA)
	require.NoError(t, err)
	require.Equal(t, "dbmigrate-test", summary.AppID)

	// ── events global stream ordering: stream_pos must ascend in (ts,
	// session, turn, seq) order, matching how Migrate re-orders SQLite's
	// unordered rows, NOT SQLite insertion order (session B, then A, then
	// A again) ─────────────────────────────────────────────────────────
	es, ok := store.AsEventStream(destStore)
	require.True(t, ok, "Postgres store must implement EventStream")
	entries, err := es.ReadStream(context.Background(), 0, 0)
	require.NoError(t, err)
	require.Len(t, entries, 3)
	require.Equal(t, data.sessionA, entries[0].Session) // ts +1s
	require.Equal(t, data.sessionA, entries[1].Session) // ts +2s
	require.Equal(t, data.sessionB, entries[2].Session) // ts +3s
	for i := 1; i < len(entries); i++ {
		require.Greater(t, entries[i].Pos, entries[i-1].Pos, "stream_pos must be strictly ascending in read order")
	}
	// Fidelity columns SQLite dropped land at their Postgres defaults.
	require.Equal(t, app.StatePath(""), entries[0].Event.StatePath)
	require.Equal(t, 0, entries[0].Event.MatchIdx)

	// ── artifactjob ─────────────────────────────────────────────────────
	ajStore, err := artifactjob.NewPostgresStore(destDB)
	require.NoError(t, err)
	job, err := ajStore.Get(context.Background(), data.jobID)
	require.NoError(t, err)
	require.Equal(t, "seed job", job.Summary)
	run, err := ajStore.GetRun(context.Background(), data.jobID)
	require.NoError(t, err)
	require.Equal(t, "trace.jsonl", run.TracePath)
	artifacts, err := ajStore.Artifacts(context.Background(), data.jobID)
	require.NoError(t, err)
	require.Len(t, artifacts, 1)
	require.Equal(t, "artifact-1", artifacts[0].Handle)

	// ── jobs ────────────────────────────────────────────────────────────
	jobStore, err := jobs.NewJobStore(destDB, jobs.WithDialect(jobs.DialectPostgres))
	require.NoError(t, err)
	schedJob, err := jobStore.GetJob(context.Background(), data.schedJobID)
	require.NoError(t, err)
	require.Equal(t, "host.run_tests", schedJob.Kind)
	notif, err := jobStore.GetNotification(context.Background(), data.notificationID)
	require.NoError(t, err)
	require.Equal(t, "job done", notif.Title)

	// ── chats ───────────────────────────────────────────────────────────
	chatStore, err := chats.NewPostgresStore(destDB)
	require.NoError(t, err)
	chat, err := chatStore.Get(context.Background(), data.chatID)
	require.NoError(t, err)
	require.Equal(t, "agent", chat.Room)
	transcript, err := chatStore.Transcript(context.Background(), data.chatID, 0)
	require.NoError(t, err)
	require.Len(t, transcript, 1)
	require.Equal(t, "hello", transcript[0].Content)
	drives, err := chatStore.ListDrives(context.Background(), data.chatID, chats.ListDrivesFilter{})
	require.NoError(t, err)
	require.Len(t, drives, 1)
	require.Equal(t, "do the thing", drives[0].Payload)

	// ── study ───────────────────────────────────────────────────────────
	studyStore, err := study.NewPostgresStore(destDB)
	require.NoError(t, err)
	snapStudy, err := studyStore.Get(context.Background(), data.studyID)
	require.NoError(t, err)
	require.Equal(t, "r1", snapStudy.Study.Plan.Revision)

	// ── webauth ─────────────────────────────────────────────────────────
	authStore, err := webauth.NewPostgresStore(destDB)
	require.NoError(t, err)
	user, ok, err := authStore.UserByGitHubID(context.Background(), data.githubUserID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "alice", user.GitHubLogin)
	sessUser, ok, err := authStore.SessionUser(context.Background(), data.sessionToken)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, user.ID, sessUser.ID)
}

func TestMigrate_DryRun_WritesNothing(t *testing.T) {
	dir := t.TempDir()
	path, _ := buildSource(t, dir)
	destDB := prepareDest(t)
	srcDB := openSource(t, path)

	report := dbmigrate.Migrate(context.Background(), srcDB, destDB, dbmigrate.Options{DryRun: true})
	require.True(t, report.OK())
	var sawSessions bool
	for _, tr := range report.Tables {
		if tr.Label == "store.sessions" {
			sawSessions = true
			require.True(t, tr.Planned)
			require.Equal(t, int64(2), tr.SourceRows)
		}
		require.Zero(t, tr.Inserted, "%s: dry-run must not insert", tr.Label)
	}
	require.True(t, sawSessions)

	var n int64
	require.NoError(t, destDB.QueryRow(`SELECT count(*) FROM sessions`).Scan(&n))
	require.Zero(t, n, "dry-run must leave the destination empty")
}

func TestMigrate_IdempotentReRun_NoDuplicates(t *testing.T) {
	dir := t.TempDir()
	path, _ := buildSource(t, dir)
	destDB := prepareDest(t)

	srcDB1 := openSource(t, path)
	first := dbmigrate.Migrate(context.Background(), srcDB1, destDB, dbmigrate.Options{})
	require.True(t, first.OK(), "err: %v", first.Err())

	srcDB2 := openSource(t, path)
	second := dbmigrate.Migrate(context.Background(), srcDB2, destDB, dbmigrate.Options{AllowNonEmptyDest: true})
	require.True(t, second.OK(), "err: %v", second.Err())
	for _, tr := range second.Tables {
		if !tr.SourcePresent {
			continue
		}
		require.Zero(t, tr.Inserted, "%s: re-run must insert nothing new", tr.Label)
		require.Equal(t, tr.SourceRows, tr.Skipped, "%s: every row must be recognized as already present", tr.Label)
		require.Equal(t, tr.SourceRows, tr.DestRowsAfter, "%s: no duplicate rows", tr.Label)
	}
}

// TestMigrate_ResumeAfterInterruption simulates a migration that was killed
// partway (some batches committed, some never ran) by deleting a subset of
// already-migrated rows from the destination, then re-running Migrate. The
// deleted rows must come back and the untouched rows must not duplicate.
func TestMigrate_ResumeAfterInterruption(t *testing.T) {
	dir := t.TempDir()
	path, data := buildSource(t, dir)
	destDB := prepareDest(t)

	srcDB1 := openSource(t, path)
	first := dbmigrate.Migrate(context.Background(), srcDB1, destDB, dbmigrate.Options{})
	require.True(t, first.OK(), "err: %v", first.Err())

	// Simulate "the migration was killed before this row's batch committed":
	// drop one events row and the artifact_jobs row from the destination.
	_, err := destDB.Exec(`DELETE FROM events WHERE session_id = $1`, string(data.sessionB))
	require.NoError(t, err)
	_, err = destDB.Exec(`DELETE FROM artifactjob.artifact_jobs WHERE id = $1`, string(data.jobID))
	require.NoError(t, err)

	srcDB2 := openSource(t, path)
	second := dbmigrate.Migrate(context.Background(), srcDB2, destDB, dbmigrate.Options{AllowNonEmptyDest: true})
	require.True(t, second.OK(), "err: %v", second.Err())

	var eventsN, jobsN int64
	require.NoError(t, destDB.QueryRow(`SELECT count(*) FROM events`).Scan(&eventsN))
	require.Equal(t, int64(3), eventsN, "the deleted row must be re-inserted, not duplicated")
	require.NoError(t, destDB.QueryRow(`SELECT count(*) FROM artifactjob.artifact_jobs`).Scan(&jobsN))
	require.Equal(t, int64(1), jobsN)

	for _, tr := range second.Tables {
		switch tr.Label {
		case "store.events":
			require.Equal(t, int64(1), tr.Inserted, "only the deleted row should be (re)inserted")
			require.Equal(t, int64(2), tr.Skipped)
		case "artifactjob.artifact_jobs":
			require.Equal(t, int64(1), tr.Inserted)
			require.Equal(t, int64(0), tr.Skipped)
		}
	}
}

func TestMigrate_MissingSourceTables_SkippedNotFailed(t *testing.T) {
	dir := t.TempDir()
	// A bare session store with none of the satellite tables ever created —
	// the common case for a deployment that never used jobs/chats/etc.
	path := filepath.Join(dir, "sessions.db")
	st, err := store.Open(path)
	require.NoError(t, err)
	_, err = st.CreateSession(context.Background(), &app.AppDef{App: app.AppMeta{ID: "bare", Version: "1"}})
	require.NoError(t, err)
	require.NoError(t, st.Close())

	destDB := prepareDest(t)
	srcDB := openSource(t, path)

	report := dbmigrate.Migrate(context.Background(), srcDB, destDB, dbmigrate.Options{})
	require.True(t, report.OK(), "err: %v", report.Err())

	var sawSkippedSatellite bool
	for _, tr := range report.Tables {
		if tr.Label == "artifactjob.artifact_jobs" {
			require.False(t, tr.SourcePresent)
			sawSkippedSatellite = true
		}
	}
	require.True(t, sawSkippedSatellite)
}

func TestPreflightShared_ReportsExistingRows(t *testing.T) {
	dir := t.TempDir()
	path, _ := buildSource(t, dir)
	destDB := prepareDest(t)

	before, err := dbmigrate.PreflightShared(context.Background(), destDB)
	require.NoError(t, err)
	for _, c := range before {
		require.Zero(t, c.Rows, "%s must start empty", c.DestTable)
	}

	srcDB := openSource(t, path)
	report := dbmigrate.Migrate(context.Background(), srcDB, destDB, dbmigrate.Options{})
	require.True(t, report.OK())

	after, err := dbmigrate.PreflightShared(context.Background(), destDB)
	require.NoError(t, err)
	var sawNonEmpty bool
	for _, c := range after {
		if c.DestTable == "sessions" {
			require.Equal(t, int64(2), c.Rows)
			sawNonEmpty = true
		}
	}
	require.True(t, sawNonEmpty)
}
