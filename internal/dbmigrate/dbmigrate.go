// Package dbmigrate copies a live SQLite kitsoki deployment's durable state
// into a Postgres backend (see docs/architecture/storage-backends.md and
// cmd/kitsoki/db_backend.go for the backend seam this fills a gap in).
//
// Coverage. The main session store (sessions, events, snapshots,
// external_keys, journal) plus five satellites that share the main store's
// SQLite file — artifactjob, jobs, chats, study, webauth — are migrated by
// [Migrate]. Two more satellites, turncache and capsule/endpoint, keep their
// SQLite state in their own file (neither is wired to a production
// construction path today — see their doc.go / package comments) and are
// migrated by [MigrateTurnCache] / [MigrateEndpoint] when the caller has such
// a file. Three subsystems are deliberately out of scope, because there is no
// SQLite source to read from:
//
//   - internal/graph/pgcatalog — graph catalogs have no SQLite backend; the
//     YAML file catalog is the source of truth there (kitsoki graph
//     import/export bridges YAML<->Postgres directly).
//   - internal/mining — the SQLite/local watermark store is an in-memory map
//     seeded from a config ledger and file mtimes, not a durable SQLite table.
//   - internal/mcp/studio objectives — MemoryObjectiveStore is the only
//     construction path in front of a SQLite backend today; nothing durable
//     to read.
//
// Also deliberately excluded, on tables that DO exist on both sides: pure
// process-liveness/lock rows (session_locks, chat_locks, chat_pty_sessions).
// A lock or tmux-hosting row describes a live process on the OLD host; on
// the new Postgres-backed deployment (a different host, a different
// dialect's lease semantics, see storage-backends.md) it is not just
// meaningless but actively misleading if copied, so this package never
// touches those tables.
//
// Fidelity. The events table is the one place source and destination
// schemas genuinely differ: SQLite drops state_path, call_id, parent_turn,
// episode_id and match_idx on write (the JSONL trace file is canonical
// there), while Postgres persists them as real columns. A migrated row
// leaves those Postgres-only columns at their schema defaults ('' / 0) —
// this package never invents replay-time fidelity SQLite never had.
//
// Ordering. events also carries stream_pos BIGSERIAL, the durable-stream
// cursor phase 2 tailing readers rely on for no-skip ordering (assignment
// order must equal commit order — see internal/store/postgres_stream.go).
// [Migrate] assigns migrated rows a global order by (ts, session_id, turn,
// seq) — the closest thing SQLite's projection has to an original write
// order — and inserts them one at a time inside transactions that hold
// pg_advisory_xact_lock(store.EventStreamAppendLockKey), the same lock a
// live append takes. That makes migrated stream_pos values monotonic in
// that chosen order and never interleaved with a concurrent live append's
// assignment. It does NOT reorder a migrated row ahead of a live append that
// commits first: run the migration before pointing any server at the
// destination for a clean historical ordering.
//
// Idempotency and resumability. Every insert targets the destination
// table's real primary key with ON CONFLICT (...) DO NOTHING, so re-running
// after an interruption reinserts nothing already present; the only
// observable effect of a resumed run is stream_pos gaps for events rows
// that had already committed (permanent, harmless — see the stream cursor
// contract doc).
//
// Streaming. Every table is copied via one open source cursor, batched into
// fixed-size destination transactions (BatchSize rows each); this package
// never materializes a whole table in memory, so it scales past the current
// production database size.
package dbmigrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"kitsoki/internal/store"
)

// tableSpec describes one table's copy: the shared columns (identical name
// and meaning on both sides, in copy order), the primary key (the ON
// CONFLICT target), and the read order from the source.
type tableSpec struct {
	// Label is the human name used in reports and errors.
	Label string
	// SourceTable is the unqualified SQLite table name.
	SourceTable string
	// DestTable is the Postgres table reference, schema-qualified where the
	// subsystem owns its own schema (e.g. "artifactjob.artifact_jobs"), or
	// unqualified for the default-schema main store.
	DestTable string
	// Columns lists the columns copied verbatim, source name == dest name.
	// Destination-only columns (e.g. events.stream_pos, events.state_path,
	// jobs.jobs.owner_pid when absent on an old SQLite file) are omitted
	// here and left to their Postgres column default.
	Columns []string
	// PrimaryKey is the destination table's real primary key, used as the
	// ON CONFLICT target for idempotent re-runs.
	PrimaryKey []string
	// OrderBy is the SQLite ORDER BY clause the source is read in. Matters
	// operationally only for the events table (stream_pos ordering); every
	// other table just needs a stable, deterministic order.
	OrderBy string
	// AdvisoryLockKey, when non-zero, is taken via
	// pg_advisory_xact_lock before each destination batch transaction's
	// inserts (see the package doc's Ordering section). Only the events
	// table sets this.
	AdvisoryLockKey int64
}

func placeholderList(n int) string {
	ph := make([]string, n)
	for i := range ph {
		ph[i] = fmt.Sprintf("$%d", i+1)
	}
	return strings.Join(ph, ", ")
}

func (t tableSpec) selectSQL() string {
	return fmt.Sprintf("SELECT %s FROM %s ORDER BY %s", strings.Join(t.Columns, ", "), t.SourceTable, t.OrderBy)
}

func (t tableSpec) insertSQL() string {
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (%s) DO NOTHING",
		t.DestTable, strings.Join(t.Columns, ", "), placeholderList(len(t.Columns)), strings.Join(t.PrimaryKey, ", "))
}

func (t tableSpec) countSQL(table string) string {
	return "SELECT COUNT(*) FROM " + table
}

// Options controls a Migrate*/CopyTables run.
type Options struct {
	// DryRun reports planned row counts and touches no destination data
	// (the destination's own idempotent schema DDL may still have run
	// before Options is even consulted — see the *Cmd wiring in
	// cmd/kitsoki/db_migrate.go — but CopyTables itself performs no writes
	// under DryRun).
	DryRun bool
	// AllowNonEmptyDest permits copying into a destination that already has
	// rows in one or more of the targeted tables. Without it, Preflight
	// (called by the CLI before CopyTables) refuses to run.
	AllowNonEmptyDest bool
	// BatchSize is the number of rows per destination transaction. Zero
	// means DefaultBatchSize.
	BatchSize int
}

// DefaultBatchSize is used when Options.BatchSize is zero.
const DefaultBatchSize = 500

func (o Options) batchSize() int {
	if o.BatchSize > 0 {
		return o.BatchSize
	}
	return DefaultBatchSize
}

// TableResult reports one table's copy outcome.
type TableResult struct {
	Label          string
	SourcePresent  bool // false when the SQLite table does not exist (feature never used on that deployment)
	SourceRows     int64
	DestRowsBefore int64
	DestRowsAfter  int64
	Attempted      int64 // rows read from the source and offered to the destination
	Inserted       int64 // rows the destination actually inserted (new)
	Skipped        int64 // rows the destination already had (ON CONFLICT DO NOTHING)
	Planned        bool  // true under DryRun: SourceRows/DestRowsBefore are populated, nothing else is
	OK             bool  // false means Err is set
	Err            error
}

// Report aggregates every table copied by one Migrate*/CopyTables call.
type Report struct {
	Tables []TableResult
}

// OK reports whether every table in the report either copied cleanly or was
// legitimately absent from the source.
func (r Report) OK() bool {
	for _, t := range r.Tables {
		if !t.OK {
			return false
		}
	}
	return true
}

// Err joins every table's error (nil if none).
func (r Report) Err() error {
	var errs []error
	for _, t := range r.Tables {
		if t.Err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", t.Label, t.Err))
		}
	}
	return errors.Join(errs...)
}

// sqliteTableExists reports whether name is a table in the SQLite database
// db is open on.
func sqliteTableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var ignored string
	err := db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name,
	).Scan(&ignored)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// CopyTables runs every spec against srcDB (SQLite) / destDB (Postgres) in
// the given order — callers pass tables in FK-safe order (parents before
// children) since dependent rows are copied in the same pass. It never
// stops early on a per-table failure; every spec gets a TableResult so a
// caller can see the whole picture, and Report.Err joins every failure.
func CopyTables(ctx context.Context, srcDB, destDB *sql.DB, specs []tableSpec, opts Options) Report {
	var report Report
	for _, spec := range specs {
		report.Tables = append(report.Tables, copyOne(ctx, srcDB, destDB, spec, opts))
	}
	return report
}

func countTable(ctx context.Context, db *sql.DB, table string) (int64, error) {
	var n int64
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func copyOne(ctx context.Context, srcDB, destDB *sql.DB, spec tableSpec, opts Options) TableResult {
	res := TableResult{Label: spec.Label}

	present, err := sqliteTableExists(ctx, srcDB, spec.SourceTable)
	if err != nil {
		res.Err = fmt.Errorf("check source table: %w", err)
		return res
	}
	if !present {
		res.OK = true
		return res
	}
	res.SourcePresent = true

	srcCount, err := countTable(ctx, srcDB, spec.SourceTable)
	if err != nil {
		res.Err = fmt.Errorf("count source rows: %w", err)
		return res
	}
	res.SourceRows = srcCount

	destBefore, err := countTable(ctx, destDB, spec.DestTable)
	if err != nil {
		res.Err = fmt.Errorf("count destination rows: %w", err)
		return res
	}
	res.DestRowsBefore = destBefore

	if opts.DryRun {
		res.Planned = true
		res.OK = true
		return res
	}

	rows, err := srcDB.QueryContext(ctx, spec.selectSQL())
	if err != nil {
		res.Err = fmt.Errorf("read source: %w", err)
		return res
	}
	defer rows.Close()

	insertSQL := spec.insertSQL()
	batchSize := opts.batchSize()
	ncols := len(spec.Columns)

	var attempted, inserted int64
	for {
		n, txErr := copyBatch(ctx, destDB, rows, insertSQL, spec.AdvisoryLockKey, ncols, batchSize, &attempted, &inserted)
		if txErr != nil {
			res.Err = fmt.Errorf("copy batch: %w", txErr)
			res.Attempted, res.Inserted, res.Skipped = attempted, inserted, attempted-inserted
			return res
		}
		if n == 0 {
			break
		}
	}
	if err := rows.Err(); err != nil {
		res.Err = fmt.Errorf("read source: %w", err)
		return res
	}

	res.Attempted = attempted
	res.Inserted = inserted
	res.Skipped = attempted - inserted

	if attempted != srcCount {
		res.Err = fmt.Errorf("read %d source rows but expected %d (source mutated during migration?)", attempted, srcCount)
		return res
	}

	destAfter, err := countTable(ctx, destDB, spec.DestTable)
	if err != nil {
		res.Err = fmt.Errorf("count destination rows after copy: %w", err)
		return res
	}
	res.DestRowsAfter = destAfter

	if wantAfter := destBefore + inserted; destAfter != wantAfter {
		res.Err = fmt.Errorf("destination row count drifted: before=%d inserted=%d after=%d (want %d) — concurrent writer during migration?",
			destBefore, inserted, destAfter, wantAfter)
		return res
	}
	if !opts.AllowNonEmptyDest && destAfter != srcCount {
		res.Err = fmt.Errorf("destination has %d rows, source has %d — mismatch after copy", destAfter, srcCount)
		return res
	}

	res.OK = true
	return res
}

// copyBatch reads up to batchSize rows from the open source cursor and
// inserts them inside one destination transaction, returning the number of
// rows it consumed (0 means the cursor is exhausted). attempted/inserted
// accumulate across calls.
func copyBatch(ctx context.Context, destDB *sql.DB, rows *sql.Rows, insertSQL string, lockKey int64, ncols, batchSize int, attempted, inserted *int64) (int, error) {
	tx, err := destDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if lockKey != 0 {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, lockKey); err != nil {
			return 0, fmt.Errorf("advisory lock: %w", err)
		}
	}

	stmt, err := tx.PrepareContext(ctx, insertSQL)
	if err != nil {
		return 0, fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	n := 0
	for n < batchSize && rows.Next() {
		vals := make([]any, ncols)
		ptrs := make([]any, ncols)
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return 0, fmt.Errorf("scan source row: %w", err)
		}
		result, err := stmt.ExecContext(ctx, vals...)
		if err != nil {
			return 0, fmt.Errorf("insert: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("rows affected: %w", err)
		}
		*attempted++
		if affected > 0 {
			*inserted++
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return n, fmt.Errorf("iterate source rows: %w", err)
	}
	if n == 0 {
		return 0, nil
	}
	if err := tx.Commit(); err != nil {
		return n, fmt.Errorf("commit: %w", err)
	}
	committed = true
	return n, nil
}

// mainStoreSpecs is internal/store's own tables. session_locks is
// deliberately absent — see the package doc.
var mainStoreSpecs = []tableSpec{
	{
		Label:       "store.sessions",
		SourceTable: "sessions",
		DestTable:   "sessions",
		Columns:     []string{"id", "app_id", "app_version", "started_at", "last_turn", "status"},
		PrimaryKey:  []string{"id"},
		OrderBy:     "id",
	},
	{
		// Postgres-only columns (state_path, call_id, parent_turn,
		// episode_id, match_idx, stream_pos) are intentionally absent from
		// Columns; they take their schema defaults ('' / 0) or, for
		// stream_pos, the next BIGSERIAL value — see the package doc's
		// Fidelity and Ordering sections.
		Label:           "store.events",
		SourceTable:     "events",
		DestTable:       "events",
		Columns:         []string{"session_id", "turn", "seq", "ts", "kind", "payload_json"},
		PrimaryKey:      []string{"session_id", "turn", "seq"},
		OrderBy:         "ts, session_id, turn, seq",
		AdvisoryLockKey: store.EventStreamAppendLockKey,
	},
	{
		Label:       "store.snapshots",
		SourceTable: "snapshots",
		DestTable:   "snapshots",
		Columns:     []string{"session_id", "turn", "state_path", "world_json", "rng_seed"},
		PrimaryKey:  []string{"session_id", "turn"},
		OrderBy:     "session_id, turn",
	},
	{
		Label:       "store.external_keys",
		SourceTable: "external_keys",
		DestTable:   "external_keys",
		Columns:     []string{"transport", "thread", "session_id", "created_at"},
		PrimaryKey:  []string{"transport", "thread"},
		OrderBy:     "transport, thread",
	},
	{
		Label:       "store.journal",
		SourceTable: "journal",
		DestTable:   "journal",
		Columns:     []string{"session_id", "turn", "seq", "ts", "kind", "doc", "doc_version", "body_json"},
		PrimaryKey:  []string{"session_id", "turn", "seq"},
		OrderBy:     "session_id, turn, seq",
	},
}

// artifactJobSpecs is internal/artifactjob's tables, parent-before-child.
var artifactJobSpecs = []tableSpec{
	{
		Label:       "artifactjob.artifact_jobs",
		SourceTable: "artifact_jobs",
		DestTable:   "artifactjob.artifact_jobs",
		Columns: []string{
			"id", "session_id", "app_id", "story", "origin_kind", "origin_ref", "origin_url",
			"status", "run_url", "trace_path", "workspace_instance_id", "terminal_artifact_handle",
			"summary", "phase", "visibility", "owner", "interrupted_reason",
			"created_at", "updated_at", "finished_at",
		},
		PrimaryKey: []string{"id"},
		OrderBy:    "id",
	},
	{
		Label:       "artifactjob.artifact_runs",
		SourceTable: "artifact_runs",
		DestTable:   "artifactjob.artifact_runs",
		Columns:     []string{"job_id", "session_id", "story", "status", "started_at", "ended_at", "last_turn", "trace_path"},
		PrimaryKey:  []string{"job_id"},
		OrderBy:     "job_id",
	},
	{
		Label:       "artifactjob.artifact_run_artifacts",
		SourceTable: "artifact_run_artifacts",
		DestTable:   "artifactjob.artifact_run_artifacts",
		Columns:     []string{"job_id", "handle", "kind", "mime", "label", "path", "size_bytes", "created_at"},
		PrimaryKey:  []string{"job_id", "handle"},
		OrderBy:     "job_id, handle",
	},
}

// jobsSpecs is internal/jobs's tables. owner_pid is included: it is added to
// the SQLite table by an ALTER on NewJobStore (see internal/jobs/store.go),
// so any SQLite file a live JobStore has opened already carries it.
var jobsSpecs = []tableSpec{
	{
		Label:       "jobs.jobs",
		SourceTable: "jobs",
		DestTable:   "jobs.jobs",
		Columns: []string{
			"id", "session_id", "kind", "status", "origin_state", "origin_proposal_id",
			"payload", "progress", "result", "error", "clarification_schema", "clarification_answer",
			"retry_count", "created_at", "updated_at", "started_at", "finished_at", "owner_pid",
		},
		PrimaryKey: []string{"id"},
		OrderBy:    "id",
	},
	{
		Label:       "jobs.notifications",
		SourceTable: "notifications",
		DestTable:   "jobs.notifications",
		Columns: []string{
			"id", "session_id", "created_at", "read_at", "dismissed_at", "snoozed_until",
			"severity", "title", "body", "teleport_state", "teleport_slots", "teleport_proposal_id",
			"teleport_job_id", "origin_kind", "origin_ref", "origin_url",
		},
		PrimaryKey: []string{"id"},
		OrderBy:    "id",
	},
}

// chatsSpecs is internal/chats's tables. chat_locks and chat_pty_sessions
// are deliberately absent — see the package doc.
var chatsSpecs = []tableSpec{
	{
		Label:       "chats.chats",
		SourceTable: "chats",
		DestTable:   "chats.chats",
		Columns: []string{
			"id", "app_id", "room", "scope_key", "title", "status", "claude_session_id",
			"parent_chat_id", "session_id", "created_at", "updated_at", "last_active_at",
		},
		PrimaryKey: []string{"id"},
		OrderBy:    "id",
	},
	{
		Label:       "chats.chat_messages",
		SourceTable: "chat_messages",
		DestTable:   "chats.chat_messages",
		Columns:     []string{"chat_id", "seq", "role", "content", "metadata", "created_at"},
		PrimaryKey:  []string{"chat_id", "seq"},
		OrderBy:     "chat_id, seq",
	},
	{
		Label:       "chats.chat_input_queue",
		SourceTable: "chat_input_queue",
		DestTable:   "chats.chat_input_queue",
		Columns: []string{
			"drive_id", "chat_id", "transport", "thread", "actor", "correlation_id", "payload",
			"status", "received_at", "dispatched_at", "completed_at", "result_seq", "error_message",
			"on_complete_json", "origin_session_id", "origin_state",
		},
		PrimaryKey: []string{"drive_id"},
		OrderBy:    "drive_id",
	},
}

// studySpecs is internal/study's one table.
var studySpecs = []tableSpec{
	{
		Label:       "study.governed_studies",
		SourceTable: "governed_studies",
		DestTable:   "study.governed_studies",
		Columns:     []string{"id", "idempotency_key", "payload"},
		PrimaryKey:  []string{"id"},
		OrderBy:     "id",
	},
}

// webauthSpecs is internal/webauth's tables, parent-before-child
// (webauth_sessions references webauth_users).
var webauthSpecs = []tableSpec{
	{
		Label:       "webauth.webauth_users",
		SourceTable: "webauth_users",
		DestTable:   "webauth.webauth_users",
		Columns:     []string{"id", "github_id", "github_login", "display_name", "role", "created_at", "last_login_at"},
		PrimaryKey:  []string{"id"},
		OrderBy:     "id",
	},
	{
		Label:       "webauth.webauth_invites",
		SourceTable: "webauth_invites",
		DestTable:   "webauth.webauth_invites",
		Columns:     []string{"id", "name", "role", "code_hash", "created_at", "redeemed_at", "redeemed_by"},
		PrimaryKey:  []string{"id"},
		OrderBy:     "id",
	},
	{
		Label:       "webauth.webauth_sessions",
		SourceTable: "webauth_sessions",
		DestTable:   "webauth.webauth_sessions",
		Columns:     []string{"id", "token_hash", "user_id", "created_at", "expires_at", "last_seen_at"},
		PrimaryKey:  []string{"id"},
		OrderBy:     "id",
	},
}

// turncacheSpecs is internal/turncache's tables — its own SQLite file, not
// the shared sessions.db (see MigrateTurnCache).
var turncacheSpecs = []tableSpec{
	{
		Label:       "turncache.turn_cache",
		SourceTable: "turn_cache",
		DestTable:   "turncache.turn_cache",
		Columns: []string{
			"app", "app_hash", "state_path", "signature", "intent", "slots_json", "confidence",
			"source_model", "source_turn_id", "hit_count", "last_hit_at", "last_verified_at",
			"revalidate_fails", "created_at",
		},
		PrimaryKey: []string{"app", "app_hash", "state_path", "signature"},
		OrderBy:    "app, app_hash, state_path, signature",
	},
	{
		Label:       "turncache.synonym_hits",
		SourceTable: "synonym_hits",
		DestTable:   "turncache.synonym_hits",
		Columns:     []string{"app_hash", "intent", "pattern", "kind", "hit_count", "last_hit_at"},
		PrimaryKey:  []string{"app_hash", "intent", "pattern", "kind"},
		OrderBy:     "app_hash, intent, pattern, kind",
	},
}

// endpointSpecs is internal/capsule/endpoint's tables — its own SQLite file,
// not the shared sessions.db (see MigrateEndpoint).
var endpointSpecs = []tableSpec{
	{
		Label:       "endpoint.endpoint_reservations",
		SourceTable: "endpoint_reservations",
		DestTable:   "endpoint.endpoint_reservations",
		Columns:     []string{"protocol", "address", "port", "description"},
		PrimaryKey:  []string{"protocol", "address", "port"},
		OrderBy:     "protocol, address, port",
	},
	{
		Label:       "endpoint.endpoint_leases",
		SourceTable: "endpoint_leases",
		DestTable:   "endpoint.endpoint_leases",
		Columns: []string{
			"id", "runtime_id", "service", "role", "owner", "generation", "source_digest",
			"protocol", "address", "port", "exposure", "acquired_at", "expires_at",
		},
		PrimaryKey: []string{"id"},
		OrderBy:    "id",
	},
}

// AllSharedFileSpecs returns every table copied by [Migrate], in dependency
// order, for callers (e.g. Preflight) that need the full list without
// running a copy.
func AllSharedFileSpecs() []tableSpec {
	var all []tableSpec
	all = append(all, mainStoreSpecs...)
	all = append(all, artifactJobSpecs...)
	all = append(all, jobsSpecs...)
	all = append(all, chatsSpecs...)
	all = append(all, studySpecs...)
	all = append(all, webauthSpecs...)
	return all
}

// Migrate copies the main session store and the five satellites that share
// its SQLite file (artifactjob, jobs, chats, study, webauth) from srcDB
// (opened on the live sessions.db) into destDB (a Postgres handle whose
// schemas the caller has already created — e.g. via the same
// newChatStore/newJobStore/newArtifactJobStore/newStudyStore helpers
// cmd/kitsoki/db_backend.go uses for live construction, plus
// webauth.NewPostgresStore).
func Migrate(ctx context.Context, srcDB, destDB *sql.DB, opts Options) Report {
	return CopyTables(ctx, srcDB, destDB, AllSharedFileSpecs(), opts)
}

// MigrateTurnCache copies internal/turncache's SQLite-backed cache tables.
// Safe to skip entirely: turncache.NewSQLite is not wired into any
// production construction path today (see internal/turncache/doc.go); this
// exists for deployments that have opted into it directly.
func MigrateTurnCache(ctx context.Context, srcDB, destDB *sql.DB, opts Options) Report {
	return CopyTables(ctx, srcDB, destDB, turncacheSpecs, opts)
}

// DestCount is one table's row count on the destination, as reported by
// Preflight*.
type DestCount struct {
	Label     string
	DestTable string
	Rows      int64
}

func destCounts(ctx context.Context, destDB *sql.DB, specs []tableSpec) ([]DestCount, error) {
	out := make([]DestCount, 0, len(specs))
	for _, spec := range specs {
		n, err := countTable(ctx, destDB, spec.DestTable)
		if err != nil {
			return nil, fmt.Errorf("count %s: %w", spec.DestTable, err)
		}
		out = append(out, DestCount{Label: spec.Label, DestTable: spec.DestTable, Rows: n})
	}
	return out, nil
}

// PreflightShared reports the destination row count of every table [Migrate]
// targets. The caller (cmd/kitsoki's `db migrate`) uses this to refuse a
// non-empty destination unless the operator explicitly opts in.
func PreflightShared(ctx context.Context, destDB *sql.DB) ([]DestCount, error) {
	return destCounts(ctx, destDB, AllSharedFileSpecs())
}

// PreflightTurnCache is PreflightShared for [MigrateTurnCache]'s tables.
func PreflightTurnCache(ctx context.Context, destDB *sql.DB) ([]DestCount, error) {
	return destCounts(ctx, destDB, turncacheSpecs)
}

// PreflightEndpoint is PreflightShared for [MigrateEndpoint]'s tables.
func PreflightEndpoint(ctx context.Context, destDB *sql.DB) ([]DestCount, error) {
	return destCounts(ctx, destDB, endpointSpecs)
}

// MigrateEndpoint copies internal/capsule/endpoint's SQLite-backed broker
// state. Like turncache, endpoint.OpenSQLite is not wired into any
// production construction path today; this exists for deployments that have
// opted into it directly. Note endpoint_leases in particular describes
// leases held by processes on the OLD host — they are copied as historical
// record, not reactivated (nothing re-validates them against live processes
// on import).
func MigrateEndpoint(ctx context.Context, srcDB, destDB *sql.DB, opts Options) Report {
	return CopyTables(ctx, srcDB, destDB, endpointSpecs, opts)
}
