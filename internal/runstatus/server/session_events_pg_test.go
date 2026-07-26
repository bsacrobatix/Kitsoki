package server_test

// Tests for the cursor-based session-events surface (session_events.go)
// against a REAL Postgres-backed store: the runstatus.session.events RPC
// (paging, cursor resume, full-fidelity field mapping, the typed
// not-supported fallback) and the cursor-driven SSE path behind /rpc/events
// (post-subscribe delivery, the additive cursor frame param, client-held
// cursor resume via ?since=, and the terminal session_gone frame). Backed by
// pgtest like internal/store's pg tests: each test gets an isolated database
// and skips when no embedded server can start. No LLMs, no external services.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/dbruntime/pgtest"
	"kitsoki/internal/runstatus"
	"kitsoki/internal/runstatus/server"
	"kitsoki/internal/store"
)

// pgStreamFixture is one live-session-over-postgres server fixture: a real pg
// store, one created session, and a stubProvider entry that carries the
// session's SessionStream (exactly what the cmd/kitsoki registry stamps).
type pgStreamFixture struct {
	st       store.Store
	stream   store.EventStream
	sid      app.SessionID
	provider *stubProvider
	ts       *httptest.Server
}

// newPGStreamFixture opens a fresh pg store (skipping when no embedded server
// can start), creates one session, and serves it as web session id "web-1".
func newPGStreamFixture(t *testing.T) *pgStreamFixture {
	t.Helper()
	db := pgtest.Open(t)
	st, err := store.OpenPostgres(db)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	es, ok := store.AsEventStream(st)
	require.True(t, ok, "postgres store must implement EventStream")

	sid, err := st.CreateSession(context.Background(), testDef())
	require.NoError(t, err)

	p := newStubProvider()
	p.entries["web-1"] = server.Entry{
		Source: &stubSource{def: testDef()},
		Stream: &server.SessionStream{Stream: es, SID: sid},
	}
	ts := httptest.NewServer(server.NewMulti(p, server.WithPollInterval(20*time.Millisecond)).Handler())
	t.Cleanup(ts.Close)
	return &pgStreamFixture{st: st, stream: es, sid: sid, provider: p, ts: ts}
}

// appendPGEvents appends n full-fidelity events for turn to the fixture's
// session; each payload carries a unique marker {"n": start+i} so tests can
// assert exactly-once delivery.
func (f *pgStreamFixture) appendPGEvents(t *testing.T, turn app.TurnNumber, start, n int) {
	t.Helper()
	evs := make([]store.Event, n)
	for i := range evs {
		payload, err := json.Marshal(map[string]any{"n": start + i})
		require.NoError(t, err)
		evs[i] = store.Event{
			Turn:       turn,
			Ts:         time.Now().UTC(),
			Kind:       store.AgentCalled,
			StatePath:  app.StatePath("root.working"),
			Payload:    payload,
			ParentTurn: 1,
			CallID:     fmt.Sprintf("call-%d", start+i),
		}
	}
	require.NoError(t, f.st.AppendEvents(f.sid, evs))
}

// sessionEventsPage is the decoded runstatus.session.events result.
type sessionEventsPage struct {
	Events     []runstatus.TraceEvent `json:"events"`
	NextCursor int64                  `json:"next_cursor"`
	Live       bool                   `json:"live"`
}

// rpcCallErr posts a JSON-RPC request and returns the structured error frame
// (nil when the call succeeded).
func rpcCallErr(t *testing.T, ts *httptest.Server, method string, params map[string]any) *struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
} {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	require.NoError(t, err)
	resp, err := http.Post(ts.URL+"/rpc", "application/json", strings.NewReader(string(body)))
	require.NoError(t, err)
	defer resp.Body.Close()
	var frame struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&frame))
	return frame.Error
}

// markerOf extracts the {"n": …} payload marker a test append stamped.
func markerOf(t *testing.T, ev runstatus.TraceEvent) int {
	t.Helper()
	n, ok := ev.Attrs["n"].(float64)
	require.True(t, ok, "event %+v lacks the payload marker", ev)
	return int(n)
}

// ─── runstatus.session.events (RPC) ──────────────────────────────────────────

func TestSessionEventsRPC_FullFidelityAndPaging(t *testing.T) {
	f := newPGStreamFixture(t)
	f.appendPGEvents(t, 2, 0, 3)
	f.appendPGEvents(t, 3, 3, 3)

	// Full read from the zero cursor.
	var full sessionEventsPage
	rpcCall(t, f.ts, "runstatus.session.events", map[string]any{"session_id": "web-1"}, &full)
	require.Len(t, full.Events, 6)
	assert.True(t, full.Live)
	assert.Positive(t, full.NextCursor)
	for i, ev := range full.Events {
		assert.Equal(t, i, markerOf(t, ev), "events must arrive oldest-first, exactly once")
		// Full-fidelity mapping: store columns → TraceEvent wire fields.
		assert.Equal(t, string(store.AgentCalled), ev.Msg)
		assert.Equal(t, "root.working", ev.StatePath)
		assert.Equal(t, 1, ev.ParentTurn)
		assert.Equal(t, fmt.Sprintf("call-%d", i), ev.Attrs["call_id"])
		assert.Equal(t, string(f.sid), ev.SessionID)
	}
	assert.Equal(t, 2, full.Events[0].Turn)
	assert.Equal(t, 3, full.Events[5].Turn)

	// The tail: reading from next_cursor yields nothing and keeps the cursor.
	var tail sessionEventsPage
	rpcCall(t, f.ts, "runstatus.session.events",
		map[string]any{"session_id": "web-1", "since": full.NextCursor}, &tail)
	assert.Empty(t, tail.Events)
	assert.Equal(t, full.NextCursor, tail.NextCursor)
	assert.True(t, tail.Live)

	// Paging: limit=2 then resume from next_cursor covers the set exactly.
	var got []int
	cursor := int64(0)
	for {
		var page sessionEventsPage
		rpcCall(t, f.ts, "runstatus.session.events",
			map[string]any{"session_id": "web-1", "since": cursor, "limit": 2}, &page)
		if len(page.Events) == 0 {
			break
		}
		require.LessOrEqual(t, len(page.Events), 2)
		for _, ev := range page.Events {
			got = append(got, markerOf(t, ev))
		}
		cursor = page.NextCursor
	}
	assert.Equal(t, []int{0, 1, 2, 3, 4, 5}, got)
}

func TestSessionEventsRPC_UnsupportedWithoutStream(t *testing.T) {
	t.Parallel()
	// A plain trace-file source has no durable stream: typed error, not a crash.
	ts := httptest.NewServer(server.New(twoTurnTrace(t), testDef()).Handler())
	defer ts.Close()

	rerr := rpcCallErr(t, ts, "runstatus.session.events", map[string]any{"session_id": "s-1"})
	require.NotNil(t, rerr)
	assert.Equal(t, -32004, rerr.Code)
	assert.Contains(t, rerr.Message, "no durable event stream")
}

func TestSessionEventsRPC_UnknownSession(t *testing.T) {
	f := newPGStreamFixture(t)
	rerr := rpcCallErr(t, f.ts, "runstatus.session.events", map[string]any{"session_id": "nope"})
	require.NotNil(t, rerr)
	assert.Equal(t, -32002, rerr.Code)
}

// ─── Cursor-driven SSE (/rpc/events) ─────────────────────────────────────────

// sseFrame is one decoded /rpc/events data frame from the cursor path.
type sseFrame struct {
	Method string `json:"method"`
	Params struct {
		SubscriptionID string               `json:"subscription_id"`
		SessionID      string               `json:"session_id"`
		Event          runstatus.TraceEvent `json:"event"`
		Cursor         int64                `json:"cursor"`
	} `json:"params"`
}

// openSSE subscribes to web-1 and opens the SSE stream (plus extra query
// params), returning a channel of decoded frames. The reader goroutine closes
// the channel when the stream ends.
func openSSE(t *testing.T, ts *httptest.Server, extraQuery string) (<-chan sseFrame, context.CancelFunc) {
	t.Helper()
	var sub struct {
		SubscriptionID string `json:"subscription_id"`
	}
	rpcCall(t, ts, "runstatus.session.subscribe", map[string]any{"session_id": "web-1"}, &sub)
	require.NotEmpty(t, sub.SubscriptionID)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	url := ts.URL + "/rpc/events?subscription_id=" + sub.SubscriptionID + extraQuery
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	frames := make(chan sseFrame, 64)
	go func() {
		defer close(frames)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			data, ok := strings.CutPrefix(scanner.Text(), "data: ")
			if !ok {
				continue
			}
			var frame sseFrame
			if json.Unmarshal([]byte(data), &frame) == nil {
				frames <- frame
			}
		}
	}()
	t.Cleanup(cancel)
	return frames, cancel
}

// collectFrames receives n runstatus.event frames (failing the test on
// timeout or an unexpected terminal frame).
func collectFrames(t *testing.T, frames <-chan sseFrame, n int) []sseFrame {
	t.Helper()
	out := make([]sseFrame, 0, n)
	deadline := time.After(15 * time.Second)
	for len(out) < n {
		select {
		case fr, ok := <-frames:
			require.True(t, ok, "SSE stream closed after %d/%d frames", len(out), n)
			if fr.Method == "runstatus.event" {
				out = append(out, fr)
			}
		case <-deadline:
			t.Fatalf("timed out after %d/%d frames", len(out), n)
		}
	}
	return out
}

func TestSSEStream_DeliversOnlyPostSubscribeEvents(t *testing.T) {
	f := newPGStreamFixture(t)
	// Pre-subscribe history must NOT ride the stream (initial load is the RPC).
	f.appendPGEvents(t, 1, 100, 2)

	frames, cancel := openSSE(t, f.ts, "")
	defer cancel()

	// Appends after subscribe arrive, in order, with monotonic cursors.
	f.appendPGEvents(t, 2, 0, 3)
	got := collectFrames(t, frames, 3)
	var lastCursor int64
	for i, fr := range got {
		assert.Equal(t, i, markerOf(t, fr.Params.Event))
		assert.Equal(t, string(f.sid), fr.Params.Event.SessionID)
		assert.Equal(t, "root.working", fr.Params.Event.StatePath)
		assert.Greater(t, fr.Params.Cursor, lastCursor, "cursor param must be strictly increasing")
		lastCursor = fr.Params.Cursor
	}

	// A second batch keeps flowing on the same connection (WaitForEvents wake).
	f.appendPGEvents(t, 3, 3, 2)
	more := collectFrames(t, frames, 2)
	assert.Equal(t, 3, markerOf(t, more[0].Params.Event))
	assert.Equal(t, 4, markerOf(t, more[1].Params.Event))
	assert.Greater(t, more[0].Params.Cursor, lastCursor)
}

func TestSSEStream_SinceQueryReplaysFromClientCursor(t *testing.T) {
	f := newPGStreamFixture(t)
	f.appendPGEvents(t, 1, 0, 4)

	// A fresh subscription normally starts at the tail; ?since=0 overrides it
	// with the client-held cursor — the restart-safe resume path.
	frames, cancel := openSSE(t, f.ts, "&since=0")
	defer cancel()

	got := collectFrames(t, frames, 4)
	for i, fr := range got {
		assert.Equal(t, i, markerOf(t, fr.Params.Event))
	}

	// Resume from the middle: replay only events after that cursor.
	mid := got[1].Params.Cursor
	frames2, cancel2 := openSSE(t, f.ts, fmt.Sprintf("&since=%d", mid))
	defer cancel2()
	rest := collectFrames(t, frames2, 2)
	assert.Equal(t, 2, markerOf(t, rest[0].Params.Event))
	assert.Equal(t, 3, markerOf(t, rest[1].Params.Event))
}

func TestSSEStream_SessionGone(t *testing.T) {
	f := newPGStreamFixture(t)
	frames, cancel := openSSE(t, f.ts, "")
	defer cancel()

	// Evict the session; the liveness check (poll interval) must emit the
	// terminal session_gone frame and close the stream.
	f.provider.mu.Lock()
	delete(f.provider.entries, "web-1")
	f.provider.mu.Unlock()

	deadline := time.After(15 * time.Second)
	for {
		select {
		case fr, ok := <-frames:
			require.True(t, ok, "stream closed without a session_gone frame")
			if fr.Method == "runstatus.session_gone" {
				assert.Equal(t, "web-1", fr.Params.SessionID)
				// Terminal: the server closes the stream after the frame.
				for range frames { //nolint:revive // drain until close
				}
				return
			}
		case <-deadline:
			t.Fatal("no session_gone frame")
		}
	}
}
