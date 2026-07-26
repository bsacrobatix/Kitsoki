package store_test

// Postgres ports of the essential sqlite_test.go / sqlite_journal_test.go
// cases, plus the Postgres-only guarantees: full-fidelity event columns, the
// global stream_pos cursor, and the lease-based writer lock (expired-lease
// takeover). Backed by internal/dbruntime/pgtest — an embedded per-process
// Postgres with a fresh database per test — so no external services are
// required; pgtest skips when no server can start in this environment.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"kitsoki/internal/app"
	"kitsoki/internal/dbruntime/pgtest"
	"kitsoki/internal/journal"
	"kitsoki/internal/store"
)

// openPG returns a Store over a fresh embedded-Postgres database. The *sql.DB
// is closed by both pgtest's cleanup and Store.Close; both are idempotent.
func openPG(t *testing.T) store.Store {
	t.Helper()
	db := pgtest.Open(t)
	st, err := store.OpenPostgres(db)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// ─── Open / schema ────────────────────────────────────────────────────────────

func TestPG_OpenIsIdempotent(t *testing.T) {
	db := pgtest.Open(t)
	st1, err := store.OpenPostgres(db)
	require.NoError(t, err)
	// Re-running the DDL over the same database must be a no-op.
	st2, err := store.OpenPostgres(db)
	require.NoError(t, err)
	_ = st1
	t.Cleanup(func() { _ = st2.Close() })

	require.Same(t, db, st2.DB(), "DB() must return the injected handle")
}

// ─── CreateSession / AppendEvents / LoadHistory ───────────────────────────────

func TestPG_CreateSession_AppendLoadHistory(t *testing.T) {
	st := openPG(t)

	def := makeAppDef("test-app", "1.0.0")
	sid, err := st.CreateSession(context.Background(), def)
	require.NoError(t, err)
	require.NotEmpty(t, string(sid))

	require.NoError(t, st.AppendEvents(sid, makeEvents(1, 3)))

	history, err := st.LoadHistory(sid)
	require.NoError(t, err)
	require.Len(t, history, 3)
	for i, ev := range history {
		require.Equal(t, app.TurnNumber(1), ev.Turn)
		require.Equal(t, i, ev.Seq, "seq should be monotonic 0,1,2")
	}
}

func TestPG_AppendEvents_SeqResetsPerTurn(t *testing.T) {
	st := openPG(t)

	sid, err := st.CreateSession(context.Background(), makeAppDef("test-app", "1.0.0"))
	require.NoError(t, err)

	require.NoError(t, st.AppendEvents(sid, makeEvents(1, 2)))
	require.NoError(t, st.AppendEvents(sid, makeEvents(2, 3)))

	history, err := st.LoadHistory(sid)
	require.NoError(t, err)
	require.Len(t, history, 5)

	require.Equal(t, 0, history[0].Seq)
	require.Equal(t, 1, history[1].Seq)
	require.Equal(t, app.TurnNumber(1), history[0].Turn)
	require.Equal(t, 0, history[2].Seq)
	require.Equal(t, 2, history[4].Seq)
	require.Equal(t, app.TurnNumber(2), history[2].Turn)
}

// Same-turn batches coexist: the second batch's seq continues past the rows
// the first batch persisted (MAX(seq)+1) — the web-bootstrap contract pinned
// by TestAppendEvents_SameTurnBatchesContinueSeq for SQLite.
func TestPG_AppendEvents_SameTurnBatchesContinueSeq(t *testing.T) {
	st := openPG(t)

	sid, err := st.CreateSession(context.Background(), makeAppDef("test-app", "1.0.0"))
	require.NoError(t, err)

	require.NoError(t, st.AppendEvents(sid, makeEvents(0, 3)))
	require.NoError(t, st.AppendEvents(sid, makeEvents(0, 2)),
		"second same-turn batch should continue seq past the first, not collide")

	hist, err := st.LoadHistory(sid)
	require.NoError(t, err)
	require.Len(t, hist, 5)
	for i := range hist {
		require.Equal(t, app.TurnNumber(0), hist[i].Turn)
		require.Equal(t, i, hist[i].Seq, "seq must be contiguous 0..4 across the two same-turn batches")
	}
}

func TestPG_AppendEvents_EmptySliceIsNoOp(t *testing.T) {
	st := openPG(t)

	sid, err := st.CreateSession(context.Background(), makeAppDef("test-app", "1.0.0"))
	require.NoError(t, err)

	require.NoError(t, st.AppendEvents(sid, nil))

	history, err := st.LoadHistory(sid)
	require.NoError(t, err)
	require.Empty(t, history)
}

func TestPG_AppendEvents_SessionNotFound(t *testing.T) {
	st := openPG(t)
	err := st.AppendEvents("nonexistent-session", makeEvents(1, 1))
	require.ErrorIs(t, err, store.ErrSessionNotFound)
}

// ─── Full-fidelity columns + stream_pos (Postgres-only guarantees) ────────────

// TestPG_FullFidelityRoundTrip pins the columns the SQLite projection drops:
// state_path, call_id, parent_turn, episode_id and match_idx must round-trip
// through AppendEvents → LoadHistory unchanged.
func TestPG_FullFidelityRoundTrip(t *testing.T) {
	st := openPG(t)

	sid, err := st.CreateSession(context.Background(), makeAppDef("test-app", "1.0.0"))
	require.NoError(t, err)

	ev := store.Event{
		Turn:       2,
		Kind:       store.AgentCalled,
		Payload:    json.RawMessage(`{"verb":"draft"}`),
		StatePath:  app.StatePath("cloakroom.desk"),
		CallID:     "call-abc123",
		ParentTurn: 1,
		EpisodeID:  "ep-7",
		MatchIdx:   3,
	}
	require.NoError(t, st.AppendEvents(sid, []store.Event{ev}))

	hist, err := st.LoadHistory(sid)
	require.NoError(t, err)
	require.Len(t, hist, 1)

	got := hist[0]
	require.Equal(t, app.StatePath("cloakroom.desk"), got.StatePath)
	require.Equal(t, "call-abc123", got.CallID)
	require.Equal(t, app.TurnNumber(1), got.ParentTurn)
	require.Equal(t, "ep-7", got.EpisodeID)
	require.Equal(t, 3, got.MatchIdx)
}

// TestPG_StreamPosIsGlobalAndMonotonic pins the durable-stream cursor: every
// inserted event gets a strictly increasing stream_pos across ALL sessions.
func TestPG_StreamPosIsGlobalAndMonotonic(t *testing.T) {
	st := openPG(t)

	sidA, err := st.CreateSession(context.Background(), makeAppDef("app-a", "1.0.0"))
	require.NoError(t, err)
	sidB, err := st.CreateSession(context.Background(), makeAppDef("app-b", "1.0.0"))
	require.NoError(t, err)

	require.NoError(t, st.AppendEvents(sidA, makeEvents(1, 2)))
	require.NoError(t, st.AppendEvents(sidB, makeEvents(1, 2)))
	require.NoError(t, st.AppendEvents(sidA, makeEvents(2, 1)))

	rows, err := st.DB().Query(`SELECT stream_pos FROM events ORDER BY stream_pos ASC`)
	require.NoError(t, err)
	defer rows.Close()

	var last int64
	var n int
	for rows.Next() {
		var pos int64
		require.NoError(t, rows.Scan(&pos))
		require.Greater(t, pos, last, "stream_pos must be strictly increasing")
		last = pos
		n++
	}
	require.NoError(t, rows.Err())
	require.Equal(t, 5, n)
}

// ─── Closed-session rejection ─────────────────────────────────────────────────

func TestPG_MarkCompleted_RejectsSubsequentAppends(t *testing.T) {
	st := openPG(t)

	sid, err := st.CreateSession(context.Background(), makeAppDef("test-app", "1.0.0"))
	require.NoError(t, err)

	require.NoError(t, st.AppendEvents(sid, makeEvents(1, 1)))
	require.NoError(t, st.MarkCompleted(context.Background(), sid))

	err = st.AppendEvents(sid, makeEvents(2, 1))
	require.ErrorIs(t, err, store.ErrSessionClosed)

	got, err := st.GetSession(context.Background(), sid)
	require.NoError(t, err)
	require.Equal(t, "completed", got.Status)
}

func TestPG_MarkAbandoned_RejectsSubsequentAppends(t *testing.T) {
	st := openPG(t)

	sid, err := st.CreateSession(context.Background(), makeAppDef("test-app", "1.0.0"))
	require.NoError(t, err)

	require.NoError(t, st.MarkAbandoned(context.Background(), sid))

	err = st.AppendEvents(sid, makeEvents(1, 1))
	require.ErrorIs(t, err, store.ErrSessionClosed)
}

func TestPG_MarkCompleted_SessionNotFound(t *testing.T) {
	st := openPG(t)
	err := st.MarkCompleted(context.Background(), "nosuchsession")
	require.ErrorIs(t, err, store.ErrSessionNotFound)
}

// ─── Snapshots ────────────────────────────────────────────────────────────────

func TestPG_Snapshot_LatestSnapshot_RoundTrip(t *testing.T) {
	st := openPG(t)

	sid, err := st.CreateSession(context.Background(), makeAppDef("test-app", "1.0.0"))
	require.NoError(t, err)

	_, ok, err := st.LatestSnapshot(sid)
	require.NoError(t, err)
	require.False(t, ok, "no snapshot should exist for a new session")

	worldJSON, _ := json.Marshal(map[string]any{"wearing_cloak": false, "disturbance": 2})
	snap := store.Snapshot{
		Turn:      app.TurnNumber(5),
		StatePath: app.StatePath("cloakroom"),
		WorldJSON: worldJSON,
		RNGSeed:   42,
	}
	require.NoError(t, st.Snapshot(sid, snap.Turn, snap))

	got, ok, err := st.LatestSnapshot(sid)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, snap.Turn, got.Turn)
	require.Equal(t, snap.StatePath, got.StatePath)
	require.Equal(t, snap.RNGSeed, got.RNGSeed)

	// Replacing the snapshot at the same (session, turn) must succeed.
	snap.StatePath = "foyer"
	require.NoError(t, st.Snapshot(sid, snap.Turn, snap))
	got, ok, err = st.LatestSnapshot(sid)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, app.StatePath("foyer"), got.StatePath)
}

func TestPG_LatestSnapshot_ReturnsNewest(t *testing.T) {
	st := openPG(t)

	sid, err := st.CreateSession(context.Background(), makeAppDef("test-app", "1.0.0"))
	require.NoError(t, err)

	worldJSON := json.RawMessage(`{}`)
	require.NoError(t, st.Snapshot(sid, 5, store.Snapshot{Turn: 5, StatePath: "foyer", WorldJSON: worldJSON}))
	require.NoError(t, st.Snapshot(sid, 20, store.Snapshot{Turn: 20, StatePath: "cloakroom", WorldJSON: worldJSON}))

	got, ok, err := st.LatestSnapshot(sid)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, app.TurnNumber(20), got.Turn)
}

func TestPG_LoadHistory_AfterSnapshot(t *testing.T) {
	st := openPG(t)

	sid, err := st.CreateSession(context.Background(), makeAppDef("test-app", "1.0.0"))
	require.NoError(t, err)

	for turn := app.TurnNumber(1); turn <= 5; turn++ {
		require.NoError(t, st.AppendEvents(sid, makeEvents(turn, 1)))
	}
	require.NoError(t, st.Snapshot(sid, 3, store.Snapshot{
		Turn:      3,
		StatePath: "foyer",
		WorldJSON: json.RawMessage(`{}`),
	}))

	history, err := st.LoadHistory(sid)
	require.NoError(t, err)
	require.Len(t, history, 2, "only events after snapshot turn should be returned")
	require.Equal(t, app.TurnNumber(4), history[0].Turn)
	require.Equal(t, app.TurnNumber(5), history[1].Turn)
}

// ─── External keys ────────────────────────────────────────────────────────────

func TestPG_ExternalKeys_BindLookupUniqueness(t *testing.T) {
	st := openPG(t)
	ctx := context.Background()

	sid1, err := st.CreateSession(ctx, makeAppDef("test-app", "1.0.0"))
	require.NoError(t, err)
	sid2, err := st.CreateSession(ctx, makeAppDef("test-app", "1.0.0"))
	require.NoError(t, err)

	require.NoError(t, st.BindExternalKey(ctx, sid1, "jira", "PLTFRM-1"))

	// Re-binding the same key to the same session is an idempotent no-op.
	require.NoError(t, st.BindExternalKey(ctx, sid1, "jira", "PLTFRM-1"))

	// Binding the same key to a DIFFERENT session is rejected.
	err = st.BindExternalKey(ctx, sid2, "jira", "PLTFRM-1")
	require.ErrorIs(t, err, store.ErrExternalKeyTaken)

	// Binding for a nonexistent session is rejected.
	err = st.BindExternalKey(ctx, "no-such-session", "jira", "PLTFRM-2")
	require.ErrorIs(t, err, store.ErrSessionNotFound)

	got, err := st.LookupByKey(ctx, "jira", "PLTFRM-1")
	require.NoError(t, err)
	require.Equal(t, sid1, got)

	_, err = st.LookupByKey(ctx, "jira", "PLTFRM-404")
	require.ErrorIs(t, err, store.ErrSessionNotFound)

	// A session may carry multiple keys.
	require.NoError(t, st.BindExternalKey(ctx, sid1, "bitbucket", "DBI/repo/pulls/42"))
	keys, err := st.ListExternalKeys(ctx, sid1)
	require.NoError(t, err)
	require.Len(t, keys, 2)
	require.Equal(t, "jira", keys[0].Transport, "oldest key first")
}

func TestPG_ListSessionsByTransport(t *testing.T) {
	st := openPG(t)
	ctx := context.Background()

	sid1, err := st.CreateSession(ctx, makeAppDef("test-app", "1.0.0"))
	require.NoError(t, err)
	sid2, err := st.CreateSession(ctx, makeAppDef("test-app", "1.0.0"))
	require.NoError(t, err)
	sid3, err := st.CreateSession(ctx, makeAppDef("test-app", "1.0.0"))
	require.NoError(t, err)

	require.NoError(t, st.BindExternalKey(ctx, sid1, "jira", "T-1"))
	time.Sleep(time.Millisecond) // distinct created_at for deterministic order
	require.NoError(t, st.BindExternalKey(ctx, sid2, "jira", "T-2"))
	require.NoError(t, st.BindExternalKey(ctx, sid3, "slack", "C-1"))

	list, err := st.ListSessionsByTransport(ctx, "jira", 0)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, sid2, list[0].ID, "newest-key-first ordering")
	require.Equal(t, sid1, list[1].ID)

	limited, err := st.ListSessionsByTransport(ctx, "jira", 1)
	require.NoError(t, err)
	require.Len(t, limited, 1)
	require.Equal(t, sid2, limited[0].ID)
}

// ─── ListSessions / GetSession ────────────────────────────────────────────────

func TestPG_ListSessions(t *testing.T) {
	st := openPG(t)
	ctx := context.Background()

	def := makeAppDef("my-app", "1.0.0")
	other := makeAppDef("other-app", "1.0.0")

	sid1, err := st.CreateSession(ctx, def)
	require.NoError(t, err)
	sid2, err := st.CreateSession(ctx, def)
	require.NoError(t, err)
	_, err = st.CreateSession(ctx, other) // different app; should not appear
	require.NoError(t, err)

	require.NoError(t, st.AppendEvents(sid2, makeEvents(3, 1)))

	list, err := st.ListSessions(ctx, "my-app", 0)
	require.NoError(t, err)
	require.Len(t, list, 2)

	ids := map[app.SessionID]bool{sid1: true, sid2: true}
	for _, s := range list {
		require.True(t, ids[s.ID], "unexpected session ID %s", s.ID)
		require.Equal(t, "my-app", s.AppID)
	}

	limited, err := st.ListSessions(ctx, "my-app", 1)
	require.NoError(t, err)
	require.Len(t, limited, 1)
}

// ─── DeleteSession cascade ────────────────────────────────────────────────────

func TestPG_DeleteSession_RemovesAllRelatedRows(t *testing.T) {
	st := openPG(t)
	ctx := context.Background()

	def := makeAppDef("test-app", "1.0.0")
	sid, err := st.CreateSession(ctx, def)
	require.NoError(t, err)

	// Populate every session-scoped table (journal included via dual-write).
	require.NoError(t, st.AppendEventsAndJournal(sid, makeEvents(1, 2),
		[]journal.Entry{makeJournalEntry(sid, 1, 0)}))
	require.NoError(t, st.Snapshot(sid, 1, store.Snapshot{}))
	require.NoError(t, st.BindExternalKey(ctx, sid, "jira", "TEST-1"))

	require.NoError(t, st.DeleteSession(ctx, sid))

	_, err = st.GetSession(ctx, sid)
	require.ErrorIs(t, err, store.ErrSessionNotFound)

	_, err = st.LookupByKey(ctx, "jira", "TEST-1")
	require.ErrorIs(t, err, store.ErrSessionNotFound)

	hist, err := st.LoadHistory(sid)
	require.NoError(t, err)
	require.Empty(t, hist)

	var journalRows int
	require.NoError(t, st.DB().QueryRow(
		`SELECT COUNT(*) FROM journal WHERE session_id = $1`, string(sid)).Scan(&journalRows))
	require.Zero(t, journalRows, "journal rows must be deleted with the session")

	// The key can be re-bound to a freshly-created session.
	sid2, err := st.CreateSession(ctx, def)
	require.NoError(t, err)
	require.NoError(t, st.BindExternalKey(ctx, sid2, "jira", "TEST-1"))
}

func TestPG_DeleteSession_NotFound(t *testing.T) {
	st := openPG(t)
	err := st.DeleteSession(context.Background(), "nosuchsession")
	require.ErrorIs(t, err, store.ErrSessionNotFound)
}

// ─── Writer lock (lease-based) ────────────────────────────────────────────────

// TestPG_WriterLock_Contention pins ErrSessionBusy: while one store instance
// holds the lease, a second instance (different holder_id, same database)
// cannot acquire it.
func TestPG_WriterLock_Contention(t *testing.T) {
	db := pgtest.Open(t)
	st1, err := store.OpenPostgres(db)
	require.NoError(t, err)
	st2, err := store.OpenPostgres(db)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st1.Close() })

	ctx := context.Background()
	sid, err := st1.CreateSession(ctx, makeAppDef("test-app", "1.0.0"))
	require.NoError(t, err)

	err = st1.WithWriterLock(ctx, sid, func() error {
		// Same holder re-entering via a different instance: busy.
		inner := st2.WithWriterLock(ctx, sid, func() error { return nil })
		require.ErrorIs(t, inner, store.ErrSessionBusy)
		return nil
	})
	require.NoError(t, err)

	// After release the second instance can acquire it.
	require.NoError(t, st2.WithWriterLock(ctx, sid, func() error { return nil }))
}

// TestPG_WriterLock_ExpiredLeaseTakeover pins the stale-lease takeover that
// replaces the SQLite PID-liveness reaping: a lease whose expires_at has
// passed (crashed holder) is taken over atomically by the next acquirer,
// while a live foreign lease stays ErrSessionBusy.
func TestPG_WriterLock_ExpiredLeaseTakeover(t *testing.T) {
	st := openPG(t)
	ctx := context.Background()

	sid, err := st.CreateSession(ctx, makeAppDef("test-app", "1.0.0"))
	require.NoError(t, err)

	// Simulate a crashed holder: lease row expired one minute ago.
	past := time.Now().Add(-time.Minute).UnixMicro()
	_, err = st.DB().Exec(
		`INSERT INTO session_locks (session_id, holder_id, acquired_at, heartbeat_at, expires_at)
		 VALUES ($1, 'dead-holder', $2, $2, $2)`,
		string(sid), past,
	)
	require.NoError(t, err)

	ran := false
	require.NoError(t, st.WithWriterLock(ctx, sid, func() error {
		ran = true
		return nil
	}), "expired lease must be taken over")
	require.True(t, ran)

	// The lock row is released after fn.
	var locks int
	require.NoError(t, st.DB().QueryRow(
		`SELECT COUNT(*) FROM session_locks WHERE session_id = $1`, string(sid)).Scan(&locks))
	require.Zero(t, locks, "lease must be released after fn returns")

	// A live foreign lease is NOT taken over.
	future := time.Now().Add(time.Hour).UnixMicro()
	_, err = st.DB().Exec(
		`INSERT INTO session_locks (session_id, holder_id, acquired_at, heartbeat_at, expires_at)
		 VALUES ($1, 'live-holder', $2, $2, $3)`,
		string(sid), time.Now().UnixMicro(), future,
	)
	require.NoError(t, err)

	err = st.WithWriterLock(ctx, sid, func() error { return nil })
	require.ErrorIs(t, err, store.ErrSessionBusy)
}

// ─── AppendEventsAndJournal atomicity ─────────────────────────────────────────

func TestPG_AppendEventsAndJournal_HappyPath(t *testing.T) {
	st := openPG(t)

	sid, err := st.CreateSession(context.Background(), makeAppDef("dual-write-app", "1.0.0"))
	require.NoError(t, err)

	events := makeEvents(1, 2)
	entry := makeJournalEntry(sid, 1, 0)

	require.NoError(t, st.AppendEventsAndJournal(sid, events, []journal.Entry{entry}))

	hist, err := st.LoadHistory(sid)
	require.NoError(t, err)
	require.Len(t, hist, 2, "expected 2 events after successful dual-write")

	var journalRows int
	require.NoError(t, st.DB().QueryRow(
		`SELECT COUNT(*) FROM journal WHERE session_id = $1`, string(sid)).Scan(&journalRows))
	require.Equal(t, 1, journalRows)
}

// TestPG_AppendEventsAndJournal_Atomicity confirms that when the journal
// insert fails (duplicate PRIMARY KEY in the journal table) the events row is
// also rolled back — the induced-failure atomicity case from the SQLite suite.
func TestPG_AppendEventsAndJournal_Atomicity_RollbackOnJournalFailure(t *testing.T) {
	st := openPG(t)

	sid, err := st.CreateSession(context.Background(), makeAppDef("dual-write-rollback-app", "1.0.0"))
	require.NoError(t, err)

	// Seed a journal entry at (turn=1, seq=0).
	require.NoError(t, st.AppendEventsAndJournal(sid, nil,
		[]journal.Entry{makeJournalEntry(sid, 1, 0)}))

	// Dual-write whose journal half duplicates the seeded PK → whole tx fails.
	conflictEntry := makeJournalEntry(sid, 1, 0)
	err = st.AppendEventsAndJournal(sid, makeEvents(2, 1), []journal.Entry{conflictEntry})
	require.Error(t, err, "conflicting journal PK should cause an error")

	hist, err := st.LoadHistory(sid)
	require.NoError(t, err)
	for _, ev := range hist {
		require.NotEqual(t, app.TurnNumber(2), ev.Turn,
			"events for turn 2 must have been rolled back with the journal failure")
	}
}

// TestPG_Journal_DocVersionAssignment pins the journal.AppendJournalPgTx
// path used by AppendEventsAndJournal: patch entries targeting a doc get
// doc_version MAX+1, and out-of-turn (turn=0, seq=0) entries get seq
// auto-assigned.
func TestPG_Journal_DocVersionAssignment(t *testing.T) {
	st := openPG(t)

	sid, err := st.CreateSession(context.Background(), makeAppDef("journal-app", "1.0.0"))
	require.NoError(t, err)

	patch := func(turn app.TurnNumber, seq int) journal.Entry {
		return journal.Entry{
			Ts:      time.Now(),
			Session: sid,
			Turn:    turn,
			Seq:     seq,
			Kind:    journal.KindWorldPatch,
			Doc:     journal.DocID("world"),
			Body:    json.RawMessage(`{"patch":[]}`),
		}
	}
	require.NoError(t, st.AppendEventsAndJournal(sid, nil, []journal.Entry{patch(1, 0)}))
	require.NoError(t, st.AppendEventsAndJournal(sid, nil, []journal.Entry{patch(2, 0)}))

	rows, err := st.DB().Query(
		`SELECT doc_version FROM journal WHERE session_id = $1 AND doc = 'world' ORDER BY doc_version`,
		string(sid))
	require.NoError(t, err)
	defer rows.Close()
	var versions []int64
	for rows.Next() {
		var v int64
		require.NoError(t, rows.Scan(&v))
		versions = append(versions, v)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []int64{1, 2}, versions, "doc_version must be MAX+1 per (session, doc)")

	// Two out-of-turn typed entries (turn=0, seq=0) coexist via seq auto-assign.
	outOfTurn := makeJournalEntry(sid, 0, 0)
	require.NoError(t, st.AppendEventsAndJournal(sid, nil, []journal.Entry{outOfTurn}))
	require.NoError(t, st.AppendEventsAndJournal(sid, nil, []journal.Entry{outOfTurn}))

	var count int
	require.NoError(t, st.DB().QueryRow(
		`SELECT COUNT(*) FROM journal WHERE session_id = $1 AND turn = 0`, string(sid)).Scan(&count))
	require.Equal(t, 2, count, "out-of-turn entries must not collide on (turn=0, seq=0)")
}
