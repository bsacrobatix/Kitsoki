package pgverify

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"kitsoki/internal/app"
	"kitsoki/internal/store"
)

// CheckTypeFidelity round-trips the two classic SQLite->Postgres breakage
// points this schema actually has, both through the real service/schema, not
// a synthetic table:
//
//   - BLOB/BYTEA: internal/study's governed_studies.payload column is the
//     schema's one BLOB-typed column (BYTEA in Postgres, BLOB in SQLite —
//     see internal/study/postgres.go / sqlite.go). This check writes bytes
//     containing every value 0x00-0xFF (the pattern most likely to reveal
//     truncation-at-NUL or charset-mangling bugs) directly into that table
//     and reads it back.
//   - Timestamp precision: the events table's ts column is unix
//     MICROSECONDS (see postgres.go's appendEventsPgTx comment on the one
//     documented fidelity risk: sub-microsecond truncation). This check
//     writes an event with a timestamp carrying non-zero microsecond
//     residue and confirms the exact microsecond value survives through
//     store.Store, not just second-level precision.
//
// There is no genuine BOOLEAN-typed column anywhere in this schema (status
// enums are TEXT, e.g. "active"/"completed"); this is reported explicitly as
// not applicable rather than fabricating a check against a column that does
// not exist.
func CheckTypeFidelity(ctx context.Context, svc store.Store, pgDB *sql.DB, keepData bool) CheckResult {
	const id = "type_fidelity"
	const name = "BLOB and timestamp-microsecond fidelity through the real schema"

	if pgDB == nil {
		return notEstablished(id, name, "no direct Postgres handle configured (Options.PGDB)", nil)
	}

	evidence := map[string]any{
		"boolean_check": "not applicable — no BOOLEAN-typed column exists in this schema (status fields are TEXT enums)",
	}

	// ── BLOB/BYTEA ──────────────────────────────────────────────────────
	blobID := MarkerPrefix + uuid.New().String()
	blobKey := MarkerPrefix + uuid.New().String()
	payload := make([]byte, 256)
	for i := range payload {
		payload[i] = byte(i) // 0x00..0xFF inclusive
	}
	if _, err := pgDB.ExecContext(ctx,
		`CREATE SCHEMA IF NOT EXISTS study; CREATE TABLE IF NOT EXISTS study.governed_studies (id TEXT PRIMARY KEY, idempotency_key TEXT NOT NULL UNIQUE, payload BYTEA NOT NULL)`,
	); err != nil {
		return notEstablished(id, name, fmt.Sprintf("ensure study schema: %v", err), evidence)
	}
	if _, err := pgDB.ExecContext(ctx,
		`INSERT INTO study.governed_studies (id, idempotency_key, payload) VALUES ($1, $2, $3)`,
		blobID, blobKey, payload,
	); err != nil {
		return notEstablished(id, name, fmt.Sprintf("insert BYTEA probe row: %v", err), evidence)
	}
	defer func() {
		if !keepData {
			_, _ = pgDB.ExecContext(context.Background(), `DELETE FROM study.governed_studies WHERE id = $1`, blobID)
		}
	}()

	var readBack []byte
	if err := pgDB.QueryRowContext(ctx, `SELECT payload FROM study.governed_studies WHERE id = $1`, blobID).Scan(&readBack); err != nil {
		return notEstablished(id, name, fmt.Sprintf("read back BYTEA probe row: %v", err), evidence)
	}
	evidence["blob_bytes_written"] = len(payload)
	evidence["blob_bytes_read"] = len(readBack)
	if !bytes.Equal(payload, readBack) {
		return fail(id, name, "BYTEA payload (256 bytes, values 0x00-0xFF) did not round-trip byte-identical", evidence)
	}

	// ── Timestamp microsecond precision ─────────────────────────────────
	if svc == nil {
		evidence["timestamp_check"] = "skipped — no service store configured (Options.OpenServiceStore)"
		return pass(id, name, "BYTEA fidelity confirmed; timestamp check skipped (no service store configured)", evidence)
	}
	// A timestamp with deliberately non-round microsecond residue (777),
	// truncated to microsecond precision up front so the comparison below
	// is exact rather than dependent on the store's own truncation policy.
	ts := time.Date(2026, 3, 4, 5, 6, 7, 777000, time.UTC) // 777000ns = 777us, 0ns sub-microsecond residue
	def := &app.AppDef{App: app.AppMeta{ID: MarkerPrefix + "fidelity", Version: "pgverify-1"}}
	sid, err := svc.CreateSession(ctx, def)
	if err != nil {
		evidence["timestamp_check_error"] = err.Error()
		return notEstablished(id, name, fmt.Sprintf("CreateSession for timestamp probe: %v", err), evidence)
	}
	if !keepData {
		defer func() { _ = svc.DeleteSession(context.Background(), sid) }()
	}
	if err := svc.AppendEvents(sid, []store.Event{{Turn: 1, Kind: store.TransitionApplied, Ts: ts, Payload: []byte(`{}`)}}); err != nil {
		return notEstablished(id, name, fmt.Sprintf("AppendEvents for timestamp probe: %v", err), evidence)
	}
	history, err := svc.LoadHistory(sid)
	if err != nil || len(history) != 1 {
		return notEstablished(id, name, fmt.Sprintf("LoadHistory for timestamp probe: err=%v len=%d", err, len(history)), evidence)
	}
	wantMicro := ts.UnixMicro()
	gotMicro := history[0].Ts.UnixMicro()
	evidence["timestamp_want_unix_micro"] = wantMicro
	evidence["timestamp_got_unix_micro"] = gotMicro
	if wantMicro != gotMicro {
		return fail(id, name, "timestamp did not round-trip at microsecond precision through the real service", evidence)
	}

	return pass(id, name, "BYTEA (256 bytes, full 0x00-0xFF range) and microsecond-precision timestamps both round-tripped exactly through the real schema", evidence)
}
