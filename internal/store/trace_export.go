package store

// trace_export.go reconstructs the canonical JSONL session trace (the format
// jsonl.go's JSONLSink writes and docs/tracing/trace-format.md specifies) from
// a database-backed [Store]. It exists for sessions that ran on the Postgres
// backend, where the events live in the full-fidelity pg events table instead
// of a trace file — trace.read, trace to-flow, and the mining pipeline all
// consume the JSONL shape, so this is the bridge back to them. It works
// against the SQLite backend too; SQLite simply has lower fidelity (see the
// divergences below).
//
// Byte compatibility: event lines are produced by [MarshalEventLine] — the
// exact traceEvent encoding JSONLSink.Append writes — so for the Postgres
// backend an exported line is byte-identical to what a JSONLSink fed the same
// event would have written, with one caveat: the events table stores ts as
// unix MICROseconds, so sub-microsecond timestamp precision is truncated.
// Output is deterministic: the same stored session always yields the same
// bytes (nothing is stamped at export time).
//
// Documented divergences from a sink-written trace file:
//   - header written_at: the original sink stamped file-creation wall-clock
//     time, which is not persisted; the export writes the session's started_at
//     (deterministic per session).
//   - ts precision: microseconds (both backends store unix micros).
//   - session.story / story.changed events are written ONLY to the JSONL
//     trace, never the DB event log (trace-format.md §3), so an exported
//     trace does not carry the embedded story. Same for agent transcript
//     sidecar files — the pointer payloads survive, the sidecars do not.
//   - SQLite additionally drops state_path, call_id, parent_turn, episode_id,
//     match_idx, and the caller-set ts (append time is stored instead). The
//     omitted fields disappear from the JSON exactly as Event's omitempty
//     marshaling naturally omits them; nothing is invented in their place.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"kitsoki/internal/app"
)

// FullHistory returns the COMPLETE ordered event log for a session, from turn
// 0, regardless of snapshots — unlike Store.LoadHistory, which starts after
// the latest snapshot (the resume path). Snapshotting never deletes event
// rows on either backend, so the full log is always present. Store
// implementations without a full-history query fall back to LoadHistory.
func FullHistory(s Store, session app.SessionID) (History, error) {
	ctx := context.Background()
	switch st := s.(type) {
	case *postgresStore:
		return st.loadHistorySince(ctx, session, -1)
	case *sqliteStore:
		return sqliteFullHistory(ctx, st, session)
	default:
		return s.LoadHistory(session)
	}
}

// sqliteFullHistory is the snapshot-ignoring counterpart of the sqlite
// loadHistoryCtx query. The SQLite events table only persists (turn, seq, ts,
// kind, payload_json); the full-fidelity columns do not exist to restore.
func sqliteFullHistory(ctx context.Context, s *sqliteStore, session app.SessionID) (History, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT turn, seq, ts, kind, payload_json
		 FROM events
		 WHERE session_id = ?
		 ORDER BY turn ASC, seq ASC`,
		string(session),
	)
	if err != nil {
		return nil, fmt.Errorf("store.FullHistory: query: %w", err)
	}
	defer rows.Close()

	var history History
	for rows.Next() {
		var (
			turnN   int64
			seq     int
			tsMicro int64
			kind    string
			payload string
		)
		if err := rows.Scan(&turnN, &seq, &tsMicro, &kind, &payload); err != nil {
			return nil, fmt.Errorf("store.FullHistory: scan: %w", err)
		}
		history = append(history, Event{
			Turn:    app.TurnNumber(turnN),
			Seq:     seq,
			Ts:      time.UnixMicro(tsMicro),
			Kind:    EventKind(kind),
			Payload: json.RawMessage(payload),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store.FullHistory: rows: %w", err)
	}
	return history, nil
}

// ExportTraceJSONL writes the canonical JSONL trace for one stored session to
// w: the session.header line followed by one event line per stored event, in
// (turn, seq) order — the same shape JSONLSink would have written (see the
// file comment for the documented divergences). Returns the number of event
// lines written. The output passes ValidateJSONL.
func ExportTraceJSONL(ctx context.Context, s Store, session app.SessionID, w io.Writer) (int, error) {
	sum, err := s.GetSession(ctx, session)
	if err != nil {
		return 0, fmt.Errorf("store.ExportTraceJSONL: %w", err)
	}

	hist, err := FullHistory(s, session)
	if err != nil {
		return 0, fmt.Errorf("store.ExportTraceJSONL: %w", err)
	}

	bw := bufio.NewWriter(w)

	// Header. written_at is the session's started_at — the only deterministic
	// creation timestamp the store persists (the sink's file-creation time is
	// not recorded anywhere).
	hdr := sessionHeader{
		Kind:          sessionHeaderKind,
		SchemaVersion: maxSchemaVersion,
		WrittenAt:     sum.StartedAt.UTC(),
	}
	line, err := marshalLine(hdr)
	if err != nil {
		return 0, fmt.Errorf("store.ExportTraceJSONL: marshal header: %w", err)
	}
	if _, err := bw.Write(line); err != nil {
		return 0, fmt.Errorf("store.ExportTraceJSONL: write header: %w", err)
	}

	for i, ev := range hist {
		b, err := MarshalEventLine(ev)
		if err != nil {
			return 0, fmt.Errorf("store.ExportTraceJSONL: event %d (turn %d seq %d): %w", i, ev.Turn, ev.Seq, err)
		}
		// Enforce the same write-time constraint the sink does: a NUL byte
		// could only come from a payload that bypassed the sink path; fail
		// loudly rather than emit a trace ValidateJSONL rejects.
		if bytes.IndexByte(b, 0) >= 0 {
			return 0, fmt.Errorf("store.ExportTraceJSONL: event %d (turn %d seq %d): marshalled line contains NUL byte", i, ev.Turn, ev.Seq)
		}
		if _, err := bw.Write(append(b, '\n')); err != nil {
			return 0, fmt.Errorf("store.ExportTraceJSONL: write event %d: %w", i, err)
		}
	}

	if err := bw.Flush(); err != nil {
		return 0, fmt.Errorf("store.ExportTraceJSONL: flush: %w", err)
	}
	return len(hist), nil
}
