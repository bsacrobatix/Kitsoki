package store_test

// Tests for the Postgres EventStream implementation (stream.go /
// postgres_stream.go): cursor pagination exactness, the no-gap/no-dup
// contract under concurrent appends (the advisory-lock commit-order
// invariant), per-session vs global consistency, and WaitForEvents wake-up /
// cancellation / lost-notification convergence. Backed by pgtest like
// postgres_test.go; skips when no embedded server can start.

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"kitsoki/internal/app"
	"kitsoki/internal/store"
)

// openPGStream opens a fresh pg store and asserts the stream capability.
func openPGStream(t *testing.T) (store.Store, store.EventStream) {
	t.Helper()
	st := openPG(t)
	es, ok := store.AsEventStream(st)
	require.True(t, ok, "postgres store must implement EventStream")
	return st, es
}

// tailCursor returns the current end-of-stream cursor.
func tailCursor(t *testing.T, es store.EventStream) store.StreamCursor {
	t.Helper()
	entries, err := es.ReadStream(context.Background(), 0, 0)
	require.NoError(t, err)
	if len(entries) == 0 {
		return 0
	}
	return entries[len(entries)-1].Pos
}

// ─── Capability discovery ─────────────────────────────────────────────────────

func TestAsEventStream_SQLiteIsNot(t *testing.T) {
	st, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	es, ok := store.AsEventStream(st)
	require.False(t, ok, "SQLite store must NOT claim the stream capability")
	require.Nil(t, es)
}

// ─── Cursor pagination ────────────────────────────────────────────────────────

// TestPG_Stream_CursorPaginationExact pages the global stream with a small
// limit and asserts exactly-once delivery: no duplicate and no missing
// (session, turn, seq) across pages, positions strictly increasing, and the
// full-fidelity columns intact on the streamed events.
func TestPG_Stream_CursorPaginationExact(t *testing.T) {
	st, es := openPGStream(t)
	ctx := context.Background()

	sidA, err := st.CreateSession(ctx, makeAppDef("app-a", "1.0.0"))
	require.NoError(t, err)
	sidB, err := st.CreateSession(ctx, makeAppDef("app-b", "1.0.0"))
	require.NoError(t, err)

	// Interleave appends: 5 turns x 3 events per session = 30 events total.
	for turn := app.TurnNumber(1); turn <= 5; turn++ {
		for _, sid := range []app.SessionID{sidA, sidB} {
			evs := make([]store.Event, 3)
			for i := range evs {
				payload, _ := json.Marshal(map[string]any{"turn": int(turn), "i": i})
				evs[i] = store.Event{
					Turn:       turn,
					Kind:       store.AgentCalled,
					Payload:    payload,
					StatePath:  app.StatePath("root.working"),
					CallID:     fmt.Sprintf("call-%s-%d-%d", sid[:4], turn, i),
					ParentTurn: turn - 1,
					EpisodeID:  "ep-1",
					MatchIdx:   i,
				}
			}
			require.NoError(t, st.AppendEvents(sid, evs))
		}
	}

	// Page globally with limit 4 (does not divide 30 evenly, so the last
	// page is short) and collect everything exactly once.
	type key struct {
		sid  app.SessionID
		turn app.TurnNumber
		seq  int
	}
	seen := map[key]store.StreamEntry{}
	var cursor store.StreamCursor
	var lastPos store.StreamCursor
	for {
		page, err := es.ReadStream(ctx, cursor, 4)
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		require.LessOrEqual(t, len(page), 4)
		for _, e := range page {
			require.Greater(t, e.Pos, lastPos, "positions must be strictly increasing across pages")
			lastPos = e.Pos
			k := key{e.Session, e.Event.Turn, e.Event.Seq}
			_, dup := seen[k]
			require.False(t, dup, "duplicate entry %+v", k)
			seen[k] = e
		}
		cursor = page[len(page)-1].Pos
	}
	require.Len(t, seen, 30, "pagination must deliver every event exactly once")

	// No missing (session, turn, seq) coordinate.
	for turn := app.TurnNumber(1); turn <= 5; turn++ {
		for _, sid := range []app.SessionID{sidA, sidB} {
			for i := 0; i < 3; i++ {
				e, ok := seen[key{sid, turn, i}]
				require.True(t, ok, "missing entry session=%s turn=%d seq=%d", sid, turn, i)
				// Full-fidelity columns round-trip through the stream read.
				require.Equal(t, app.StatePath("root.working"), e.Event.StatePath)
				require.Equal(t, fmt.Sprintf("call-%s-%d-%d", sid[:4], turn, i), e.Event.CallID)
				require.Equal(t, turn-1, e.Event.ParentTurn)
				require.Equal(t, "ep-1", e.Event.EpisodeID)
				require.Equal(t, i, e.Event.MatchIdx)
			}
		}
	}

	// limit=0 returns the whole tail in one read.
	all, err := es.ReadStream(ctx, 0, 0)
	require.NoError(t, err)
	require.Len(t, all, 30)

	// Cursor at the tail reads empty, not an error.
	empty, err := es.ReadStream(ctx, all[len(all)-1].Pos, 0)
	require.NoError(t, err)
	require.Empty(t, empty)
}

// TestPG_Stream_SessionVsGlobalConsistency pins that ReadSessionStream is
// exactly the global stream filtered to one session — same entries, same
// positions, same order — and that a global cursor is valid in the
// per-session read (shared cursor space).
func TestPG_Stream_SessionVsGlobalConsistency(t *testing.T) {
	st, es := openPGStream(t)
	ctx := context.Background()

	sidA, err := st.CreateSession(ctx, makeAppDef("app-a", "1.0.0"))
	require.NoError(t, err)
	sidB, err := st.CreateSession(ctx, makeAppDef("app-b", "1.0.0"))
	require.NoError(t, err)

	for turn := app.TurnNumber(1); turn <= 4; turn++ {
		require.NoError(t, st.AppendEvents(sidA, makeEvents(turn, 2)))
		require.NoError(t, st.AppendEvents(sidB, makeEvents(turn, 1)))
	}

	global, err := es.ReadStream(ctx, 0, 0)
	require.NoError(t, err)
	require.Len(t, global, 12)

	var filteredA []store.StreamEntry
	for _, e := range global {
		if e.Session == sidA {
			filteredA = append(filteredA, e)
		}
	}

	onlyA, err := es.ReadSessionStream(ctx, sidA, 0, 0)
	require.NoError(t, err)
	require.Equal(t, filteredA, onlyA, "session stream must equal the filtered global stream")

	// A cursor taken mid-global-stream works for the per-session read.
	mid := global[5].Pos
	tailA, err := es.ReadSessionStream(ctx, sidA, mid, 0)
	require.NoError(t, err)
	var wantTailA []store.StreamEntry
	for _, e := range filteredA {
		if e.Pos > mid {
			wantTailA = append(wantTailA, e)
		}
	}
	require.Equal(t, wantTailA, tailA)

	// Per-session pagination with limit.
	page, err := es.ReadSessionStream(ctx, sidA, 0, 3)
	require.NoError(t, err)
	require.Len(t, page, 3)
	require.Equal(t, filteredA[:3], page)
}

// ─── No-gap/no-dup under concurrent appends ───────────────────────────────────

// TestPG_Stream_ConcurrentAppends_NoGapNoDup pins the commit-order invariant
// documented in postgres_stream.go: while writers append concurrently across
// sessions, a tailing reader that advances its cursor to the last Pos it saw
// must end up with EVERY committed event exactly once — even though
// BIGSERIAL assignment order and commit order could otherwise invert.
func TestPG_Stream_ConcurrentAppends_NoGapNoDup(t *testing.T) {
	st, es := openPGStream(t)
	store.SetStreamPollInterval(st, 100*time.Millisecond)
	ctx := context.Background()

	const (
		writers          = 4
		appendsPerWriter = 20
		eventsPerAppend  = 3
	)
	total := writers * appendsPerWriter * eventsPerAppend

	sids := make([]app.SessionID, writers)
	for i := range sids {
		sid, err := st.CreateSession(ctx, makeAppDef(fmt.Sprintf("app-%d", i), "1.0.0"))
		require.NoError(t, err)
		sids[i] = sid
	}

	// Tail concurrently with the writers, advancing the cursor to the last
	// Pos of each page — the exact pattern a durable consumer uses.
	collected := make(chan []store.StreamEntry, 1)
	readerCtx, readerCancel := context.WithTimeout(ctx, 60*time.Second)
	defer readerCancel()
	go func() {
		var got []store.StreamEntry
		var cursor store.StreamCursor
		for len(got) < total {
			if err := es.WaitForEvents(readerCtx, cursor); err != nil {
				break
			}
			page, err := es.ReadStream(readerCtx, cursor, 7)
			if err != nil {
				break
			}
			if len(page) > 0 {
				got = append(got, page...)
				cursor = page[len(page)-1].Pos
			}
		}
		collected <- got
	}()

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for a := 0; a < appendsPerWriter; a++ {
				evs := make([]store.Event, eventsPerAppend)
				for i := range evs {
					payload, _ := json.Marshal(map[string]any{"w": w, "a": a, "i": i})
					evs[i] = store.Event{
						Turn:    app.TurnNumber(a + 1),
						Kind:    store.TransitionApplied,
						Payload: payload,
					}
				}
				if err := st.AppendEvents(sids[w], evs); err != nil {
					t.Errorf("writer %d append %d: %v", w, a, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	got := <-collected
	require.Len(t, got, total, "tailing reader must see every committed event")

	// Exactly-once: unique (session, turn, seq), strictly increasing Pos.
	type key struct {
		sid  app.SessionID
		turn app.TurnNumber
		seq  int
	}
	seen := map[key]bool{}
	var lastPos store.StreamCursor
	for _, e := range got {
		require.Greater(t, e.Pos, lastPos, "cursor tail must be strictly increasing")
		lastPos = e.Pos
		k := key{e.Session, e.Event.Turn, e.Event.Seq}
		require.False(t, seen[k], "duplicate %+v", k)
		seen[k] = true
	}

	// Complete coverage: every (writer, turn, seq) coordinate present.
	for w := 0; w < writers; w++ {
		for a := 0; a < appendsPerWriter; a++ {
			for i := 0; i < eventsPerAppend; i++ {
				require.True(t, seen[key{sids[w], app.TurnNumber(a + 1), i}],
					"missing writer=%d turn=%d seq=%d", w, a+1, i)
			}
		}
	}
}

// ─── WaitForEvents ────────────────────────────────────────────────────────────

// TestPG_Stream_WaitForEvents_ReturnsImmediatelyWhenBehind pins the fast
// path: a cursor already behind the tail never blocks.
func TestPG_Stream_WaitForEvents_ReturnsImmediatelyWhenBehind(t *testing.T) {
	st, es := openPGStream(t)
	ctx := context.Background()

	sid, err := st.CreateSession(ctx, makeAppDef("test-app", "1.0.0"))
	require.NoError(t, err)
	require.NoError(t, st.AppendEvents(sid, makeEvents(1, 2)))

	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	require.NoError(t, es.WaitForEvents(waitCtx, 0))
}

// TestPG_Stream_WaitForEvents_WakesOnAppend starts a waiter at the tail and
// asserts an append (whose tx carries the pg_notify) wakes it promptly —
// well inside the poll fallback interval, proving the NOTIFY path works.
func TestPG_Stream_WaitForEvents_WakesOnAppend(t *testing.T) {
	st, es := openPGStream(t)
	// Poll interval deliberately LONG so a pass proves NOTIFY did the wake.
	store.SetStreamPollInterval(st, 30*time.Second)
	ctx := context.Background()

	sid, err := st.CreateSession(ctx, makeAppDef("test-app", "1.0.0"))
	require.NoError(t, err)
	require.NoError(t, st.AppendEvents(sid, makeEvents(1, 1)))
	after := tailCursor(t, es)

	waitErr := make(chan error, 1)
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	go func() { waitErr <- es.WaitForEvents(waitCtx, after) }()

	// Give the waiter time to reach LISTEN, then append.
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	require.NoError(t, st.AppendEvents(sid, makeEvents(2, 1)))

	select {
	case err := <-waitErr:
		require.NoError(t, err)
		require.Less(t, time.Since(start), 10*time.Second,
			"waiter must wake via NOTIFY, not the 30s poll fallback")
	case <-time.After(15 * time.Second):
		t.Fatal("WaitForEvents did not wake on append")
	}

	// The woken reader actually finds the new row.
	entries, err := es.ReadStream(ctx, after, 0)
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	require.Equal(t, app.TurnNumber(2), entries[0].Event.Turn)
}

// TestPG_Stream_WaitForEvents_CtxCancel pins that cancellation interrupts a
// blocked waiter promptly with ctx.Err().
func TestPG_Stream_WaitForEvents_CtxCancel(t *testing.T) {
	st, es := openPGStream(t)
	store.SetStreamPollInterval(st, 30*time.Second)
	ctx := context.Background()

	after := tailCursor(t, es)
	_ = st // no appends: the waiter has nothing to find

	waitCtx, cancel := context.WithCancel(ctx)
	waitErr := make(chan error, 1)
	go func() { waitErr <- es.WaitForEvents(waitCtx, after) }()

	time.Sleep(300 * time.Millisecond) // let it block in WaitForNotification
	cancel()

	select {
	case err := <-waitErr:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(10 * time.Second):
		t.Fatal("WaitForEvents did not return after ctx cancel")
	}
}

// TestPG_Stream_WaitForEvents_LostNotifyConvergesViaPoll pins the fallback:
// rows inserted WITHOUT a pg_notify (direct SQL, simulating a lost/never-sent
// notification) still wake the waiter via the periodic cursor re-check —
// the cursor read is the source of truth, NOTIFY is only an accelerant.
func TestPG_Stream_WaitForEvents_LostNotifyConvergesViaPoll(t *testing.T) {
	st, es := openPGStream(t)
	store.SetStreamPollInterval(st, 150*time.Millisecond)
	ctx := context.Background()

	sid, err := st.CreateSession(ctx, makeAppDef("test-app", "1.0.0"))
	require.NoError(t, err)
	after := tailCursor(t, es)

	waitErr := make(chan error, 1)
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	go func() { waitErr <- es.WaitForEvents(waitCtx, after) }()

	time.Sleep(300 * time.Millisecond) // waiter is blocked listening

	// Insert directly — bypasses appendEventsPgTx, so NO notification fires.
	_, err = st.DB().ExecContext(ctx,
		`INSERT INTO events (session_id, turn, seq, ts, kind, payload_json)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		string(sid), int64(1), 0, time.Now().UnixMicro(), string(store.TransitionApplied), `{}`,
	)
	require.NoError(t, err)

	select {
	case err := <-waitErr:
		require.NoError(t, err, "poll fallback must converge without a NOTIFY")
	case <-time.After(15 * time.Second):
		t.Fatal("WaitForEvents never converged via the poll fallback")
	}

	entries, err := es.ReadStream(ctx, after, 0)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, sid, entries[0].Session)
}
