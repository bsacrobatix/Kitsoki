package runstatus_test

// TestFromHistory_PassThrough verifies that FromHistory is a pure pass-through:
// every store.Event in the history maps 1:1 to a TraceEvent in Snapshot.Events,
// no synthesis, no back-fill, no oracle-specific code path.
//
// Covers all EventKind values including OracleCalled/OracleReturned/OracleError
// (the wave 3-oracle events) to prove they emerge verbatim and the function
// does not synthesise extra events that weren't in the input.
//
// Runtime budget: <5 ms (no I/O, no LLM calls, in-memory only).

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/runstatus"
	"kitsoki/internal/store"
)

// buildMinimalAppDef returns the simplest AppDef that FromHistory accepts
// (Compile + FlowchartWithMap must not error).
func buildMinimalAppDef() *app.AppDef {
	return &app.AppDef{
		App: app.AppMeta{
			ID:      "test-app",
			Version: "0.0.1",
		},
	}
}

func TestFromHistory_PassThrough(t *testing.T) {
	t.Parallel()

	def := buildMinimalAppDef()

	base := time.Date(2026, 5, 28, 10, 0, 0, 0, time.UTC)

	// Construct a synthetic History that covers:
	//   - a normal turn (TurnStarted, StateEntered, TurnEnded)
	//   - an oracle exchange (OracleCalled, OracleReturned)
	//   - an error event (HarnessError — must produce level ERROR)
	//   - an OracleError event
	calledPayload, err := json.Marshal(map[string]any{
		"verb":   "ask",
		"agent":  "my-agent",
		"model":  "claude-sonnet",
		"prompt": "What is the answer?",
	})
	require.NoError(t, err)

	returnedPayload, err := json.Marshal(map[string]any{
		"verb":        "ask",
		"duration_ms": float64(120),
		"response":    "42",
	})
	require.NoError(t, err)

	errorPayload, err := json.Marshal(map[string]any{"error": "something went wrong"})
	require.NoError(t, err)

	hist := store.History{
		{Turn: 1, Ts: base.Add(0), Kind: store.TurnStarted, StatePath: "foyer", Seq: 0},
		{Turn: 1, Ts: base.Add(1 * time.Millisecond), Kind: store.StateEntered, StatePath: "foyer", Seq: 1,
			Payload: json.RawMessage(`{"state":"foyer"}`)},
		{Turn: 1, Ts: base.Add(2 * time.Millisecond), Kind: store.OracleCalled, StatePath: "foyer", Seq: 2,
			CallID: "abc123def456abcd", Payload: calledPayload},
		{Turn: 1, Ts: base.Add(3 * time.Millisecond), Kind: store.OracleReturned, StatePath: "foyer", Seq: 3,
			CallID: "abc123def456abcd", Payload: returnedPayload},
		{Turn: 1, Ts: base.Add(4 * time.Millisecond), Kind: store.HarnessError, StatePath: "foyer", Seq: 4,
			Payload: errorPayload},
		{Turn: 1, Ts: base.Add(5 * time.Millisecond), Kind: store.OracleError, StatePath: "foyer", Seq: 5,
			CallID: "abc123def456abcd", Payload: errorPayload},
		{Turn: 1, Ts: base.Add(6 * time.Millisecond), Kind: store.TurnEnded, StatePath: "foyer", Seq: 6},
	}

	snap, err := runstatus.FromHistory(hist, def, "sess-001")
	require.NoError(t, err)

	// Length must match exactly — no synthesised extra events.
	assert.Equal(t, len(hist), len(snap.Events),
		"Snapshot.Events length must equal History length (no synthesis, no injection)")

	// Verify each event maps 1:1.
	for i, ev := range snap.Events {
		orig := hist[i]
		assert.Equal(t, string(orig.Kind), ev.Msg,
			"events[%d].Msg must equal Kind string", i)
		assert.Equal(t, int(orig.Turn), ev.Turn,
			"events[%d].Turn", i)
		assert.Equal(t, string(orig.StatePath), ev.StatePath,
			"events[%d].StatePath", i)
		assert.Equal(t, int(orig.ParentTurn), ev.ParentTurn,
			"events[%d].ParentTurn", i)
		assert.True(t, orig.Ts.Equal(ev.Time),
			"events[%d].Time: want %v got %v", i, orig.Ts, ev.Time)
	}

	// Level mapping: HarnessError must be ERROR; all others INFO.
	for i, ev := range snap.Events {
		orig := hist[i]
		switch orig.Kind {
		case store.HarnessError, store.ValidationFailed, store.GuardRejected:
			assert.Equal(t, "ERROR", ev.Level, "events[%d] (%s) must be ERROR", i, orig.Kind)
		default:
			assert.Equal(t, "INFO", ev.Level, "events[%d] (%s) must be INFO", i, orig.Kind)
		}
	}

	// call_id must be surfaced in Attrs for oracle events.
	oracleCalledIdx := 2
	assert.Equal(t, "abc123def456abcd", snap.Events[oracleCalledIdx].Attrs["call_id"],
		"OracleCalled.Attrs[call_id] must carry CallID from store.Event")
	oracleReturnedIdx := 3
	assert.Equal(t, "abc123def456abcd", snap.Events[oracleReturnedIdx].Attrs["call_id"],
		"OracleReturned.Attrs[call_id] must carry CallID from store.Event")

	// Session header fields.
	assert.Equal(t, "sess-001", snap.Session.SessionID)
	assert.Equal(t, "test-app", snap.Session.AppID)
	assert.Equal(t, "foyer", snap.Session.CurrentState)
	assert.Equal(t, 1, snap.Session.Turn)
	assert.True(t, base.Equal(snap.Session.StartedAt),
		"StartedAt must be the timestamp of the first event")

	// Empty history must not panic.
	snapEmpty, errEmpty := runstatus.FromHistory(store.History{}, def, "sess-empty")
	require.NoError(t, errEmpty, "empty history must not error")
	assert.Equal(t, 0, len(snapEmpty.Events))
	assert.Equal(t, "", snapEmpty.Session.CurrentState)
}

// TestFromHistory_OracleEventsPassThroughVerbatim confirms that when a History
// already contains OracleCalled/OracleReturned events, FromHistory does not
// inject any additional oracle events. This is the core wave 4a contract: the
// JSONL is authoritative; no synthesis path exists.
func TestFromHistory_OracleEventsPassThroughVerbatim(t *testing.T) {
	t.Parallel()

	def := buildMinimalAppDef()
	base := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)

	calledPayload, err := json.Marshal(map[string]any{"verb": "decide", "prompt": "left or right?"})
	require.NoError(t, err)
	returnedPayload, err := json.Marshal(map[string]any{"verb": "decide", "response": "left"})
	require.NoError(t, err)

	hist := store.History{
		{Turn: 1, Ts: base, Kind: store.TurnStarted, StatePath: "start", Seq: 0},
		{Turn: 1, Ts: base.Add(1 * time.Millisecond), Kind: store.OracleCalled, StatePath: "start", Seq: 1,
			CallID: "callid-001", Payload: calledPayload},
		{Turn: 1, Ts: base.Add(2 * time.Millisecond), Kind: store.OracleReturned, StatePath: "start", Seq: 2,
			CallID: "callid-001", Payload: returnedPayload},
		{Turn: 1, Ts: base.Add(3 * time.Millisecond), Kind: store.TurnEnded, StatePath: "start", Seq: 3},
	}

	snap, err := runstatus.FromHistory(hist, def, "sess-oracle")
	require.NoError(t, err)

	// Exactly 4 events — no synthesis of extra oracle events.
	assert.Equal(t, 4, len(snap.Events),
		"must have exactly 4 events (no extra synthesised oracle pairs)")

	// Kinds in order.
	wantMsgs := []string{
		string(store.TurnStarted),
		string(store.OracleCalled),
		string(store.OracleReturned),
		string(store.TurnEnded),
	}
	for i, want := range wantMsgs {
		assert.Equal(t, want, snap.Events[i].Msg, "events[%d].Msg", i)
	}
}
