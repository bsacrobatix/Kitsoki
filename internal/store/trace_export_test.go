package store_test

// trace_export_test.go proves the export path (store.ExportTraceJSONL /
// store.FullHistory) reconstructs the canonical JSONL trace from a database
// backend:
//
//   - Determinism / byte-compatibility (the core contract): the SAME event
//     sequence driven through a real JSONLSink and through the Postgres store
//     + export yields byte-identical event lines. The header may differ ONLY
//     in written_at (the sink stamps file-creation time, which the store does
//     not persist; export writes the session's started_at — documented on
//     ExportTraceJSONL).
//   - The exported bytes pass ValidateJSONL (header + seq/turn oracle).
//   - Export reads the FULL log even after a snapshot bounds LoadHistory.
//   - SQLite export omits the fields SQLite genuinely drops (state_path,
//     call_id, …) the way Event's omitempty marshaling naturally does.
//
// Postgres cases use pgtest via openPG (postgres_test.go) and skip when the
// embedded server cannot start.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"kitsoki/internal/app"
	"kitsoki/internal/store"
)

// exportEventBatches is a fidelity-exercising fixture: multiple turns, a
// same-turn second batch (seq continuation), unicode + HTML-escaped payload
// bytes, state_path, parent_turn (off-path), call_id, episode_id, match_idx.
// Timestamps are explicit, UTC, and microsecond-precision — the events-table
// ts column stores unix micros, so micro-precision input makes the JSONL
// sink's RFC3339Nano rendering and the export's byte-identical.
func exportEventBatches() [][]store.Event {
	base := time.Date(2026, 1, 15, 9, 0, 0, 123456000, time.UTC)
	ts := func(i int) time.Time { return base.Add(time.Duration(i) * 7 * time.Millisecond) }
	pay := func(s string) json.RawMessage { return json.RawMessage(s) }

	return [][]store.Event{
		{
			{Turn: 1, Ts: ts(0), Kind: store.TurnStarted, StatePath: "foyer", Payload: pay(`{"input":"café ✓ <&> \"quoted\"","routed_by":"semantic"}`)},
			{Turn: 1, Ts: ts(1), Kind: store.UserInputReceived, StatePath: "foyer", Payload: pay(`{"input":"go west"}`)},
			{Turn: 1, Ts: ts(2), Kind: store.AgentCalled, StatePath: "foyer", CallID: "deadbeef01020304", EpisodeID: "ep-1", MatchIdx: 2, Payload: pay(`{"verb":"ask"}`)},
			{Turn: 1, Ts: ts(3), Kind: store.TurnEnded, StatePath: "foyer", Payload: pay(`{"outcome":"transitioned","to":"hall"}`)},
		},
		{
			{Turn: 2, Ts: ts(4), Kind: store.OffPathQuestion, ParentTurn: 1, Payload: pay(`{"question":"why?"}`)},
			{Turn: 2, Ts: ts(5), Kind: store.OffPathAnswer, ParentTurn: 1, Payload: pay(`{"answer":"because"}`)},
		},
		// Second batch for turn 2: seq must CONTINUE (2, ...), not reset.
		{
			{Turn: 2, Ts: ts(6), Kind: store.EffectApplied, StatePath: "hall", Payload: pay(`{"set":{"n":1}}`)},
			// Nil payload: both paths must write {}.
			{Turn: 2, Ts: ts(7), Kind: store.MachineSay, StatePath: "hall"},
		},
	}
}

// cloneEvents deep-copies a batch: AppendEvents overwrites Seq in place, and
// the sink/store must each receive pristine input.
func cloneEvents(in []store.Event) []store.Event {
	out := make([]store.Event, len(in))
	copy(out, in)
	for i := range out {
		out[i].Payload = append(json.RawMessage(nil), in[i].Payload...)
	}
	return out
}

// splitTraceLines splits trace bytes into lines (without trailing \n),
// requiring a trailing newline on the final line.
func splitTraceLines(t *testing.T, data []byte) [][]byte {
	t.Helper()
	require.NotEmpty(t, data)
	require.Equal(t, byte('\n'), data[len(data)-1], "trace must end with \\n")
	return bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
}

func TestTraceExport_PGMatchesJSONLSinkBytes(t *testing.T) {
	st := openPG(t)
	batches := exportEventBatches()

	// Reference: the real JSONL sink.
	tracePath := filepath.Join(t.TempDir(), "reference.jsonl")
	sink, err := store.OpenJSONL(tracePath)
	require.NoError(t, err)
	for _, batch := range batches {
		for _, ev := range cloneEvents(batch) {
			require.NoError(t, sink.Append(ev))
		}
	}
	require.NoError(t, sink.Close())
	refBytes, err := os.ReadFile(tracePath)
	require.NoError(t, err)

	// Same sequence through the pg store, then export.
	sid, err := st.CreateSession(context.Background(), makeAppDef("export-app", "1.0.0"))
	require.NoError(t, err)
	for _, batch := range batches {
		require.NoError(t, st.AppendEvents(sid, cloneEvents(batch)))
	}
	var buf bytes.Buffer
	n, err := store.ExportTraceJSONL(context.Background(), st, sid, &buf)
	require.NoError(t, err)

	refLines := splitTraceLines(t, refBytes)
	gotLines := splitTraceLines(t, buf.Bytes())
	require.Len(t, gotLines, len(refLines), "line count (header + events)")
	require.Equal(t, len(refLines)-1, n, "reported event count")

	// Event lines: byte-for-byte.
	for i := 1; i < len(refLines); i++ {
		require.Equal(t, string(refLines[i]), string(gotLines[i]), "event line %d", i+1)
	}

	// Header: identical except written_at (documented divergence — the sink's
	// file-creation time is not persisted; export writes started_at).
	type hdr struct {
		Kind          string    `json:"kind"`
		SchemaVersion int       `json:"schema_version"`
		WrittenAt     time.Time `json:"written_at"`
	}
	var refHdr, gotHdr hdr
	require.NoError(t, json.Unmarshal(refLines[0], &refHdr))
	require.NoError(t, json.Unmarshal(gotLines[0], &gotHdr))
	require.Equal(t, refHdr.Kind, gotHdr.Kind)
	require.Equal(t, refHdr.SchemaVersion, gotHdr.SchemaVersion)
	require.False(t, gotHdr.WrittenAt.IsZero(), "export header must carry a real written_at")

	// The exported trace passes the canonical read-side oracle.
	exportPath := filepath.Join(t.TempDir(), "exported.jsonl")
	require.NoError(t, os.WriteFile(exportPath, buf.Bytes(), 0o644))
	require.NoError(t, store.ValidateJSONL(exportPath))

	// Determinism: same stored session → same bytes.
	var buf2 bytes.Buffer
	_, err = store.ExportTraceJSONL(context.Background(), st, sid, &buf2)
	require.NoError(t, err)
	require.True(t, bytes.Equal(buf.Bytes(), buf2.Bytes()), "export must be deterministic")
}

func TestTraceExport_PGFullHistoryIgnoresSnapshot(t *testing.T) {
	st := openPG(t)
	sid, err := st.CreateSession(context.Background(), makeAppDef("snap-app", "1.0.0"))
	require.NoError(t, err)

	for turn := app.TurnNumber(1); turn <= 3; turn++ {
		require.NoError(t, st.AppendEvents(sid, makeEvents(turn, 2)))
	}
	require.NoError(t, st.Snapshot(sid, 2, store.Snapshot{
		Turn: 2, StatePath: "hall", WorldJSON: json.RawMessage(`{}`),
	}))

	// LoadHistory is snapshot-bounded (turn 3 only); FullHistory is not.
	bounded, err := st.LoadHistory(sid)
	require.NoError(t, err)
	require.Len(t, bounded, 2)

	full, err := store.FullHistory(st, sid)
	require.NoError(t, err)
	require.Len(t, full, 6)

	var buf bytes.Buffer
	n, err := store.ExportTraceJSONL(context.Background(), st, sid, &buf)
	require.NoError(t, err)
	require.Equal(t, 6, n, "export must carry the full event log, not the resume tail")
}

func TestTraceExport_SQLiteOmitsDroppedFields(t *testing.T) {
	st, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	sid, err := st.CreateSession(context.Background(), makeAppDef("lite-app", "1.0.0"))
	require.NoError(t, err)
	require.NoError(t, st.AppendEvents(sid, cloneEvents(exportEventBatches()[0])))

	var buf bytes.Buffer
	n, err := store.ExportTraceJSONL(context.Background(), st, sid, &buf)
	require.NoError(t, err)
	require.Equal(t, 4, n)

	lines := splitTraceLines(t, buf.Bytes())
	for _, line := range lines[1:] {
		var m map[string]any
		require.NoError(t, json.Unmarshal(line, &m))
		// SQLite genuinely drops these; export must OMIT them (omitempty),
		// never invent placeholders.
		for _, k := range []string{"state_path", "call_id", "parent_turn", "episode_id", "match_idx"} {
			require.NotContains(t, m, k, "sqlite export must omit dropped field %q", k)
		}
		require.Contains(t, m, "payload")
		require.Contains(t, m, "ts")
	}

	exportPath := filepath.Join(t.TempDir(), "lite.jsonl")
	require.NoError(t, os.WriteFile(exportPath, buf.Bytes(), 0o644))
	require.NoError(t, store.ValidateJSONL(exportPath))
}
