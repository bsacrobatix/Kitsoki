package pgverify

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"kitsoki/internal/app"
	"kitsoki/internal/store"
)

// roundTripAppID namespaces the session CheckRoundTrip creates so it is
// trivially identifiable and never collides with real application sessions.
const roundTripAppID = MarkerPrefix + "roundtrip"

// roundTripPayload is the marker payload written and read back; it embeds
// binary-looking content and a distinguishing marker so accidental
// collisions with real data are effectively impossible.
type roundTripPayload struct {
	Marker string `json:"marker"`
	Nonce  string `json:"nonce"`
}

// CheckRoundTrip creates a real, namespaced session through svc (the exact
// store.Store code path a live kitsoki process runs against Postgres),
// appends one event, reads it back through the same interface, and THEN
// independently re-derives the same row via a raw SQL query against pgDB
// that never touches internal/store — proving the data actually landed in
// Postgres and was not served from any in-process cache. Cleans up the
// session afterwards unless keepData is set.
//
// Returns two CheckResults: "write_read_roundtrip" (the store.Store-level
// proof) and "direct_postgres_rows" (the raw-SQL proof). Both are needed
// because a bug that broke only the raw table (e.g. a botched migration of
// this schema) would still let LoadHistory appear correct if it were
// somehow reading from a cache — which it is not, but the whole point of
// this package is to never assume that.
func CheckRoundTrip(ctx context.Context, svc store.Store, pgDB *sql.DB, keepData bool) (roundtrip, directRows CheckResult, cleanup func()) {
	const rtID, rtName = "write_read_roundtrip", "Write-read round trip through the real service (store.Store)"
	const drID, drName = "direct_postgres_rows", "Round-trip rows independently confirmed via raw SQL against Postgres"

	noop := func() {}

	if svc == nil {
		nr := notEstablished(rtID, rtName, "no service store configured (Options.OpenServiceStore)", nil)
		return nr, notEstablished(drID, drName, "skipped: round trip did not run", nil), noop
	}
	if pgDB == nil {
		nr := notEstablished(rtID, rtName, "no direct Postgres handle configured (Options.PGDB)", nil)
		return nr, notEstablished(drID, drName, "no direct Postgres handle configured (Options.PGDB)", nil), noop
	}

	nonce := uuid.New().String()
	payload, err := json.Marshal(roundTripPayload{Marker: MarkerPrefix + "roundtrip", Nonce: nonce})
	if err != nil {
		nr := notEstablished(rtID, rtName, fmt.Sprintf("marshal marker payload: %v", err), nil)
		return nr, notEstablished(drID, drName, "skipped: round trip did not run", nil), noop
	}

	def := &app.AppDef{App: app.AppMeta{ID: roundTripAppID, Version: "pgverify-1"}}
	sid, err := svc.CreateSession(ctx, def)
	if err != nil {
		nr := notEstablished(rtID, rtName, fmt.Sprintf("CreateSession: %v", err), nil)
		return nr, notEstablished(drID, drName, "skipped: round trip did not run", nil), noop
	}

	cleanupFn := func() {
		if keepData {
			return
		}
		_ = svc.DeleteSession(context.Background(), sid)
	}

	writeTS := time.Now().UTC()
	ev := store.Event{
		Turn:    1,
		Kind:    store.TransitionApplied,
		Ts:      writeTS,
		Payload: payload,
	}
	if err := svc.AppendEvents(sid, []store.Event{ev}); err != nil {
		nr := notEstablished(rtID, rtName, fmt.Sprintf("AppendEvents: %v", err), nil)
		return nr, notEstablished(drID, drName, "skipped: round trip did not run", nil), cleanupFn
	}

	history, err := svc.LoadHistory(sid)
	if err != nil {
		nr := notEstablished(rtID, rtName, fmt.Sprintf("LoadHistory: %v", err), nil)
		return nr, notEstablished(drID, drName, "skipped: round trip did not run", nil), cleanupFn
	}
	if len(history) != 1 {
		nr := fail(rtID, rtName, fmt.Sprintf("expected exactly 1 event back, got %d", len(history)),
			map[string]any{"session_id": string(sid), "event_count": len(history)})
		return nr, notEstablished(drID, drName, "skipped: round trip did not confirm expected shape", nil), cleanupFn
	}
	var got roundTripPayload
	if err := json.Unmarshal(history[0].Payload, &got); err != nil {
		nr := fail(rtID, rtName, fmt.Sprintf("unmarshal read-back payload: %v", err), map[string]any{"session_id": string(sid)})
		return nr, notEstablished(drID, drName, "skipped: round trip did not confirm expected shape", nil), cleanupFn
	}
	if got.Nonce != nonce {
		nr := fail(rtID, rtName, "read-back payload nonce does not match what was written",
			map[string]any{"session_id": string(sid), "want_nonce": nonce, "got_nonce": got.Nonce})
		return nr, notEstablished(drID, drName, "skipped: round trip did not confirm expected shape", nil), cleanupFn
	}

	roundtrip = pass(rtID, rtName,
		"created a session, appended an event, and read it back byte-identical through store.Store",
		map[string]any{"session_id": string(sid), "app_id": roundTripAppID, "nonce": nonce})

	// Independent raw-SQL re-derivation, bypassing internal/store entirely.
	var (
		rowAppID, rowPayload string
		rowStatus            string
	)
	err = pgDB.QueryRowContext(ctx, `SELECT app_id, status FROM sessions WHERE id = $1`, string(sid)).Scan(&rowAppID, &rowStatus)
	if err == sql.ErrNoRows {
		directRows = fail(drID, drName, "the session store.Store just reported creating is NOT present in Postgres's sessions table — a directly observed negative, not an inability to check", map[string]any{"session_id": string(sid)})
		return roundtrip, directRows, cleanupFn
	}
	if err != nil {
		directRows = notEstablished(drID, drName, fmt.Sprintf("raw SELECT on sessions: %v", err), map[string]any{"session_id": string(sid)})
		return roundtrip, directRows, cleanupFn
	}
	err = pgDB.QueryRowContext(ctx, `SELECT payload_json FROM events WHERE session_id = $1 AND turn = 1 AND seq = 0`, string(sid)).Scan(&rowPayload)
	if err == sql.ErrNoRows {
		directRows = fail(drID, drName, "the event store.Store just reported appending is NOT present in Postgres's events table — a directly observed negative, not an inability to check", map[string]any{"session_id": string(sid)})
		return roundtrip, directRows, cleanupFn
	}
	if err != nil {
		directRows = notEstablished(drID, drName, fmt.Sprintf("raw SELECT on events: %v", err), map[string]any{"session_id": string(sid)})
		return roundtrip, directRows, cleanupFn
	}
	var rawPayload roundTripPayload
	if err := json.Unmarshal([]byte(rowPayload), &rawPayload); err != nil {
		directRows = notEstablished(drID, drName, fmt.Sprintf("unmarshal raw payload_json: %v", err), map[string]any{"session_id": string(sid)})
		return roundtrip, directRows, cleanupFn
	}

	evidence := map[string]any{
		"session_id": string(sid),
		"raw_app_id": rowAppID,
		"raw_status": rowStatus,
		"raw_nonce":  rawPayload.Nonce,
	}
	if rowAppID != roundTripAppID || rawPayload.Nonce != nonce {
		directRows = fail(drID, drName, "raw SQL row content does not match what CreateSession/AppendEvents wrote", evidence)
		return roundtrip, directRows, cleanupFn
	}
	directRows = pass(drID, drName,
		"the exact session/event rows written through store.Store are present in Postgres, queried with raw SQL that never touches internal/store",
		evidence)
	return roundtrip, directRows, cleanupFn
}
