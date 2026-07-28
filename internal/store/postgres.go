package store

// postgres.go provides the postgresStore implementation of the [Store]
// interface, added ALONGSIDE the SQLite one (sqlite.go stays the default code
// path; Postgres is selected explicitly via OpenPostgres/OpenPostgresDSN).
// See doc.go for the package overview; the points below record the deliberate
// divergences from the SQLite projection.
//
// Full-fidelity events: the SQLite events table drops StatePath, CallID,
// ParentTurn, EpisodeID and MatchIdx on write. The Postgres table persists
// them as real columns, so LoadHistory round-trips the complete Event. It
// also carries a global BIGSERIAL stream_pos column — the future durable-
// stream cursor — assigned by the database on insert.
//
// Writer lock: SQLite's session_locks is PID-liveness based, which only means
// anything when every writer shares one host. The Postgres table is
// LEASE-based: acquire is an atomic upsert-if-expired keyed by an opaque
// holder_id, the holder heartbeats while fn runs, and a crashed holder's
// lease is taken over once expires_at passes. Same ErrSessionBusy semantics.
//
// Journal: internal/journal speaks both dialects; the append path delegates
// to journal.AppendJournalPgTx (the $N-placeholder counterpart of
// AppendJournalTx) with identical semantics (doc_version = MAX+1 assignment,
// out-of-turn seq auto-assignment) against the same journal table shape.
//
// Concurrency: Postgres has no single-writer model, so the append path locks
// the session row (SELECT ... FOR UPDATE) to serialize same-session appends —
// the seq-continuation read (MAX(seq)+1) would otherwise race between two
// concurrent transactions and collide on the (session_id, turn, seq) PK.

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"kitsoki/internal/app"
	"kitsoki/internal/journal"

	_ "github.com/jackc/pgx/v5/stdlib" // register "pgx" driver
)

//go:embed schema_pg.sql
var schemaPgDDL string

// Lease tuning for the session writer lock. The TTL bounds how long a crashed
// holder blocks a session; the heartbeat renews well inside that window so a
// live holder is never taken over.
const (
	pgLeaseTTL       = 30 * time.Second
	pgLeaseHeartbeat = 10 * time.Second
)

// postgresStore is the concrete Store implementation backed by Postgres.
type postgresStore struct {
	db *sql.DB

	// holderID is the opaque lease identity for this store instance. Two
	// instances (even in one process) never share a holder, so re-entrant
	// acquisition through a second instance is ErrSessionBusy — mirroring
	// the SQLite same-pid behavior.
	holderID string

	leaseTTL  time.Duration
	heartbeat time.Duration

	// streamPoll overrides the WaitForEvents fallback re-check interval
	// (postgres_stream.go); zero means pgStreamPollDefault. Set only in
	// tests via export_test.go.
	streamPoll time.Duration

	// closeOnce makes Close idempotent (pgtest also closes the handle).
	closeOnce sync.Once
	closeErr  error
}

// OpenPostgres wraps an already-open *sql.DB (pgx stdlib driver) as a Store.
// It runs the embedded Postgres DDL idempotently. The store takes ownership
// of db: Store.Close closes it.
func OpenPostgres(db *sql.DB) (Store, error) {
	if db == nil {
		return nil, fmt.Errorf("store.OpenPostgres: db must not be nil")
	}
	// pgx's extended protocol rejects multi-command strings, so the DDL is
	// executed one statement at a time. Comment lines are stripped BEFORE the
	// ";" split so prose comments stay free-form; schema_pg.sql contains no
	// string literals, which is what makes the line-based strip safe.
	for _, stmt := range strings.Split(stripSQLComments(schemaPgDDL), ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			return nil, fmt.Errorf("store.OpenPostgres: schema DDL %q: %w", firstSQLLine(stmt), err)
		}
	}
	return &postgresStore{
		db:        db,
		holderID:  uuid.New().String(),
		leaseTTL:  pgLeaseTTL,
		heartbeat: pgLeaseHeartbeat,
	}, nil
}

// ApplicationName is the Postgres application_name every kitsoki-opened
// connection identifies itself with (see withApplicationName below). It is a
// runtime-reported identity signal that lives in Postgres's own
// pg_stat_activity view, independent of anything the kitsoki process
// declares about itself: a deployment verification tool with DSN access can
// query "SELECT count(*) FROM pg_stat_activity WHERE application_name =
// store.ApplicationName" to confirm the running service is genuinely holding
// live connections to THIS database, rather than trusting a --db-backend
// flag or an environment variable. See cmd/kitsoki/db_verify.go /
// internal/pgverify for the consumer.
const ApplicationName = "kitsoki"

// OpenPostgresDSN opens a Postgres session store from a DSN (URL or
// keyword=value form) using the pgx stdlib driver. The DSN is stamped with
// application_name=kitsoki (see [ApplicationName]) unless the caller already
// set one explicitly, so every live connection this package opens is
// independently identifiable from Postgres's side.
func OpenPostgresDSN(dsn string) (Store, error) {
	dsn, err := WithApplicationName(dsn, ApplicationName)
	if err != nil {
		return nil, fmt.Errorf("store.OpenPostgresDSN: %w", err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("store.OpenPostgresDSN: sql.Open: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store.OpenPostgresDSN: ping: %w", err)
	}
	st, err := OpenPostgres(db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return st, nil
}

// WithApplicationName returns dsn with application_name=name added, unless
// dsn already specifies one (an explicit caller choice always wins). Accepts
// both URL (postgres://...) and libpq keyword=value DSN forms, mirroring
// internal/dbruntime/pgtest's dsnWithDatabase. Exported so a caller opening
// its OWN separate connection to the same server as OpenPostgresDSN — e.g.
// internal/pgverify's round-trip probe — can stamp a deliberately different
// application_name, keeping the two identifiable apart in pg_stat_activity
// (see internal/pgverify.CheckBackendIdentity's doc comment for why that
// distinction matters: without it, a verification tool's own connection
// could be mistaken for the real service's).
func WithApplicationName(dsn, name string) (string, error) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "", fmt.Errorf("parse DSN: %w", err)
		}
		q := u.Query()
		if q.Get("application_name") == "" {
			q.Set("application_name", name)
			u.RawQuery = q.Encode()
		}
		return u.String(), nil
	}
	if strings.Contains(dsn, "application_name=") {
		return dsn, nil
	}
	sep := " "
	if strings.TrimSpace(dsn) == "" {
		sep = ""
	}
	return dsn + sep + "application_name=" + name, nil
}

// stripSQLComments removes "--" line comments from ddl so the statement split
// on ";" cannot be confused by punctuation inside comment prose.
func stripSQLComments(ddl string) string {
	lines := strings.Split(ddl, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if idx := strings.Index(line, "--"); idx >= 0 {
			line = line[:idx]
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// firstSQLLine returns the first non-empty line of chunk, for error context.
func firstSQLLine(chunk string) string {
	for _, line := range strings.Split(chunk, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// IsPostgres reports whether st is the Postgres-backed Store implementation.
// Callers that hang satellite stores off Store.DB() use it to pick the
// matching satellite constructor — the shared handle's dialect must follow
// the session store's backend.
func IsPostgres(st Store) bool {
	_, ok := st.(*postgresStore)
	return ok
}

// DB returns the underlying *sql.DB, allowing auxiliary packages to share the
// same pool. The returned *sql.DB must not be closed by the caller.
func (s *postgresStore) DB() *sql.DB { return s.db }

// Close closes the underlying *sql.DB. Safe to call more than once.
func (s *postgresStore) Close() error {
	s.closeOnce.Do(func() { s.closeErr = s.db.Close() })
	return s.closeErr
}

// CreateSession inserts a new session row and returns its ID.
func (s *postgresStore) CreateSession(ctx context.Context, def *app.AppDef) (app.SessionID, error) {
	sid := app.SessionID(uuid.New().String())
	now := time.Now().UnixMicro()

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id, app_id, app_version, started_at, last_turn, status)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		string(sid),
		def.App.ID,
		def.App.Version,
		now,
		int64(0),
		"active",
	)
	if err != nil {
		return "", fmt.Errorf("store.CreateSession: %w", err)
	}
	return sid, nil
}

// AppendEvents atomically appends events for one turn in a single transaction.
// seq is overwritten: events within a turn get monotonic seq continuing past
// any rows already persisted for that turn. All events in the slice must share
// the same Turn value; the turn is taken from events[0].Turn.
//
// Returns ErrSessionClosed if the session status is completed or abandoned.
// Returns ErrSessionNotFound if the session does not exist.
func (s *postgresStore) AppendEvents(session app.SessionID, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	return s.appendTx(context.Background(), session, events, nil)
}

// AppendEventsAndJournal atomically appends events and journal entries in a
// single transaction. Either both writes succeed or both are rolled back.
// The events constraint (ErrSessionClosed / ErrSessionNotFound) is checked
// before either insert proceeds.
func (s *postgresStore) AppendEventsAndJournal(session app.SessionID, events []Event, journalEntries []journal.Entry) error {
	if len(events) == 0 && len(journalEntries) == 0 {
		return nil
	}
	return s.appendTx(context.Background(), session, events, journalEntries)
}

// appendTx is the shared transactional write path for AppendEvents and
// AppendEventsAndJournal.
func (s *postgresStore) appendTx(ctx context.Context, session app.SessionID, events []Event, journalEntries []journal.Entry) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("store.appendTx: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Check session exists and is active. FOR UPDATE serializes concurrent
	// appends to the same session so the MAX(seq) reads below cannot race.
	var status string
	err = tx.QueryRowContext(ctx,
		`SELECT status FROM sessions WHERE id = $1 FOR UPDATE`, string(session)).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSessionNotFound
	}
	if err != nil {
		return fmt.Errorf("store.appendTx: check session: %w", err)
	}
	if status != "active" {
		return ErrSessionClosed
	}

	if len(events) > 0 {
		if err := appendEventsPgTx(ctx, tx, session, events); err != nil {
			return err
		}
	}

	if len(journalEntries) > 0 {
		if err := journal.AppendJournalPgTx(ctx, tx, session, journalEntries); err != nil {
			return fmt.Errorf("store.appendTx: journal: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store.appendTx: commit: %w", err)
	}
	return nil
}

// appendEventsPgTx inserts events rows and updates last_turn within an
// existing transaction. Seq values are overwritten with monotonic indices
// that CONTINUE past any rows already persisted for this turn (MAX(seq)+1,
// or 0 when the turn is empty) — same contract as the SQLite appendEventsTx;
// see that function for the same-turn-batches rationale. Unlike SQLite, the
// full-fidelity columns (state_path, call_id, parent_turn, episode_id,
// match_idx) are persisted; stream_pos is assigned by the BIGSERIAL default.
func appendEventsPgTx(ctx context.Context, tx *sql.Tx, session app.SessionID, events []Event) error {
	turn := events[0].Turn
	now := time.Now().UnixMicro()

	// Serialize ALL event appends (across sessions) for the remainder of this
	// transaction so stream_pos assignment order equals commit order — the
	// invariant that lets EventStream readers page `stream_pos > cursor`
	// without ever missing a row (see postgres_stream.go for the full
	// argument). Advisory xact locks release at commit/rollback. Lock order
	// is always sessions-row FOR UPDATE (appendTx) → this advisory lock, so
	// the two cannot deadlock.
	if _, err := tx.ExecContext(ctx,
		`SELECT pg_advisory_xact_lock($1)`, pgStreamAppendLockKey,
	); err != nil {
		return fmt.Errorf("store.appendEventsPgTx: stream append lock: %w", err)
	}

	var seqBase int
	{
		var maxSeq sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			`SELECT MAX(seq) FROM events WHERE session_id = $1 AND turn = $2`,
			string(session), int64(turn),
		).Scan(&maxSeq); err != nil {
			return fmt.Errorf("store.appendEventsPgTx: max seq for turn %d: %w", turn, err)
		}
		if maxSeq.Valid {
			seqBase = int(maxSeq.Int64) + 1
		}
	}

	// Insert all events, assigning monotonic seq starting at seqBase.
	for i := range events {
		if err := ctx.Err(); err != nil {
			return err
		}
		events[i].Seq = seqBase + i

		payload := events[i].Payload
		if payload == nil {
			payload = json.RawMessage("{}")
		}

		// Full-fidelity ts: persist the event's own timestamp when the caller
		// set one (what the JSONL sink records), falling back to append time
		// for unstamped events — the SQLite behavior. The column is unix
		// MICROseconds, so sub-microsecond precision is truncated; trace
		// export documents that as its one event-field divergence.
		tsMicro := now
		if !events[i].Ts.IsZero() {
			tsMicro = events[i].Ts.UTC().UnixMicro()
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO events (session_id, turn, seq, ts, kind, payload_json,
			                     state_path, call_id, parent_turn, episode_id, match_idx)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			string(session),
			int64(events[i].Turn),
			events[i].Seq,
			tsMicro,
			string(events[i].Kind),
			string(payload),
			string(events[i].StatePath),
			events[i].CallID,
			int64(events[i].ParentTurn),
			events[i].EpisodeID,
			events[i].MatchIdx,
		); err != nil {
			return fmt.Errorf("store.appendEventsPgTx: insert event %d: %w", i, err)
		}
	}

	// Update last_turn on the session.
	if _, err := tx.ExecContext(ctx,
		`UPDATE sessions SET last_turn = $1 WHERE id = $2`,
		int64(turn), string(session),
	); err != nil {
		return fmt.Errorf("store.appendEventsPgTx: update last_turn: %w", err)
	}

	// Wake stream tailers (EventStream.WaitForEvents). In the same tx so the
	// notification fires exactly at commit, never for a rolled-back append.
	// Payload = session id; delivery is best-effort — readers converge via
	// their periodic cursor re-check even if this is lost.
	if _, err := tx.ExecContext(ctx,
		`SELECT pg_notify($1, $2)`, pgEventsNotifyChannel, string(session),
	); err != nil {
		return fmt.Errorf("store.appendEventsPgTx: notify: %w", err)
	}
	return nil
}

// LoadHistory returns the ordered event log for a session, starting after the
// latest snapshot's turn (or from turn 0 if no snapshot exists). All
// full-fidelity columns are restored onto the returned Events.
func (s *postgresStore) LoadHistory(session app.SessionID) (History, error) {
	ctx := context.Background()

	afterTurn := int64(-1)
	snap, ok, err := s.LatestSnapshot(session)
	if err != nil {
		return nil, fmt.Errorf("store.LoadHistory: latest snapshot: %w", err)
	}
	if ok {
		afterTurn = int64(snap.Turn)
	}
	return s.loadHistorySince(ctx, session, afterTurn)
}

// loadHistorySince returns the ordered events with turn > afterTurn. Pass
// afterTurn = -1 for the complete event log regardless of snapshots (the
// trace-export path).
func (s *postgresStore) loadHistorySince(ctx context.Context, session app.SessionID, afterTurn int64) (History, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT turn, seq, ts, kind, payload_json,
		        state_path, call_id, parent_turn, episode_id, match_idx
		 FROM events
		 WHERE session_id = $1 AND turn > $2
		 ORDER BY turn ASC, seq ASC`,
		string(session), afterTurn,
	)
	if err != nil {
		return nil, fmt.Errorf("store.LoadHistory: query: %w", err)
	}
	defer rows.Close()

	var history History
	for rows.Next() {
		var (
			turnN      int64
			seq        int
			tsMicro    int64
			kind       string
			payload    string
			statePath  string
			callID     string
			parentTurn int64
			episodeID  string
			matchIdx   int
		)
		if err := rows.Scan(&turnN, &seq, &tsMicro, &kind, &payload,
			&statePath, &callID, &parentTurn, &episodeID, &matchIdx); err != nil {
			return nil, fmt.Errorf("store.LoadHistory: scan: %w", err)
		}
		history = append(history, Event{
			Turn:       app.TurnNumber(turnN),
			Seq:        seq,
			Ts:         time.UnixMicro(tsMicro),
			Kind:       EventKind(kind),
			Payload:    json.RawMessage(payload),
			StatePath:  app.StatePath(statePath),
			CallID:     callID,
			ParentTurn: app.TurnNumber(parentTurn),
			EpisodeID:  episodeID,
			MatchIdx:   matchIdx,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store.LoadHistory: rows: %w", err)
	}
	return history, nil
}

// Snapshot materializes a state snapshot at a given turn.
// If a snapshot at the same (session, turn) already exists, it is replaced.
func (s *postgresStore) Snapshot(session app.SessionID, at app.TurnNumber, snap Snapshot) error {
	worldBytes, err := json.Marshal(snap.WorldJSON)
	if err != nil {
		return fmt.Errorf("store.Snapshot: marshal world: %w", err)
	}

	_, err = s.db.ExecContext(context.Background(),
		`INSERT INTO snapshots (session_id, turn, state_path, world_json, rng_seed)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (session_id, turn) DO UPDATE
		    SET state_path = EXCLUDED.state_path,
		        world_json = EXCLUDED.world_json,
		        rng_seed   = EXCLUDED.rng_seed`,
		string(session),
		int64(at),
		string(snap.StatePath),
		string(worldBytes),
		snap.RNGSeed,
	)
	if err != nil {
		return fmt.Errorf("store.Snapshot: insert: %w", err)
	}
	return nil
}

// LatestSnapshot loads the most recent snapshot for the resume path.
// Returns (snapshot, false, nil) if no snapshot exists yet.
func (s *postgresStore) LatestSnapshot(session app.SessionID) (Snapshot, bool, error) {
	var (
		turnN     int64
		statePath string
		worldJSON string
		rngSeed   int64
	)
	err := s.db.QueryRowContext(context.Background(),
		`SELECT turn, state_path, world_json, rng_seed
		 FROM snapshots
		 WHERE session_id = $1
		 ORDER BY turn DESC
		 LIMIT 1`,
		string(session),
	).Scan(&turnN, &statePath, &worldJSON, &rngSeed)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, fmt.Errorf("store.LatestSnapshot: %w", err)
	}
	return Snapshot{
		Turn:      app.TurnNumber(turnN),
		StatePath: app.StatePath(statePath),
		WorldJSON: json.RawMessage(worldJSON),
		RNGSeed:   rngSeed,
	}, true, nil
}

// MarkCompleted sets the session status to "completed".
// After this call, AppendEvents returns ErrSessionClosed.
func (s *postgresStore) MarkCompleted(ctx context.Context, session app.SessionID) error {
	return s.setStatus(ctx, session, "completed")
}

// MarkAbandoned sets the session status to "abandoned".
// After this call, AppendEvents returns ErrSessionClosed.
func (s *postgresStore) MarkAbandoned(ctx context.Context, session app.SessionID) error {
	return s.setStatus(ctx, session, "abandoned")
}

func (s *postgresStore) setStatus(ctx context.Context, session app.SessionID, status string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET status = $1 WHERE id = $2`,
		status, string(session),
	)
	if err != nil {
		return fmt.Errorf("store.setStatus %s: %w", status, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrSessionNotFound
	}
	return nil
}

// DeleteSession removes a session and all rows that reference it
// (events, snapshots, external_keys, session_locks, journal) atomically.
// The row in `sessions` is deleted last so a partial failure leaves the
// session id resolvable for retry.
func (s *postgresStore) DeleteSession(ctx context.Context, session app.SessionID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store.DeleteSession: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	sid := string(session)
	for _, q := range []string{
		`DELETE FROM events         WHERE session_id = $1`,
		`DELETE FROM snapshots      WHERE session_id = $1`,
		`DELETE FROM external_keys  WHERE session_id = $1`,
		`DELETE FROM session_locks  WHERE session_id = $1`,
		`DELETE FROM journal        WHERE session_id = $1`,
	} {
		if _, err := tx.ExecContext(ctx, q, sid); err != nil {
			return fmt.Errorf("store.DeleteSession: %s: %w", q, err)
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id = $1`, sid)
	if err != nil {
		return fmt.Errorf("store.DeleteSession: DELETE sessions: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrSessionNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store.DeleteSession: commit: %w", err)
	}
	return nil
}

// GetSession returns a single session by id.
func (s *postgresStore) GetSession(ctx context.Context, session app.SessionID) (SessionSummary, error) {
	const q = `SELECT id, app_id, app_version, started_at, last_turn, status
	           FROM sessions WHERE id = $1`
	var (
		id        string
		aid       string
		aver      string
		startedAt int64
		lastTurn  int64
		status    string
	)
	err := s.db.QueryRowContext(ctx, q, string(session)).Scan(&id, &aid, &aver, &startedAt, &lastTurn, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionSummary{}, ErrSessionNotFound
	}
	if err != nil {
		return SessionSummary{}, fmt.Errorf("store.GetSession: %w", err)
	}
	return SessionSummary{
		ID:         app.SessionID(id),
		AppID:      aid,
		AppVersion: aver,
		StartedAt:  time.UnixMicro(startedAt),
		LastTurn:   app.TurnNumber(lastTurn),
		Status:     status,
	}, nil
}

// ListSessions returns up to limit sessions for the given app ID, ordered by
// started_at descending. Pass limit=0 for no limit.
func (s *postgresStore) ListSessions(ctx context.Context, appID string, limit int) ([]SessionSummary, error) {
	q := `SELECT id, app_id, app_version, started_at, last_turn, status
	      FROM sessions
	      WHERE app_id = $1
	      ORDER BY started_at DESC`
	args := []any{appID}
	if limit > 0 {
		q += " LIMIT $2"
		args = append(args, limit)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store.ListSessions: %w", err)
	}
	defer rows.Close()
	return scanSessionSummaries(rows, "store.ListSessions")
}

// BindExternalKey inserts (transport, thread) → session into the index.
// Returns ErrExternalKeyTaken if the pair is bound to a different session.
// Re-binding to the same session is a no-op success.
func (s *postgresStore) BindExternalKey(ctx context.Context, session app.SessionID, transport, thread string) error {
	if transport == "" || thread == "" {
		return fmt.Errorf("store.BindExternalKey: transport and thread must be non-empty")
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("store.BindExternalKey: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Verify the session exists.
	var existsCount int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sessions WHERE id = $1`, string(session),
	).Scan(&existsCount); err != nil {
		return fmt.Errorf("store.BindExternalKey: check session: %w", err)
	}
	if existsCount == 0 {
		return ErrSessionNotFound
	}

	// Atomic claim: insert wins the (transport, thread) PK or leaves the
	// existing row untouched; the follow-up read decides idempotent vs taken.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO external_keys (transport, thread, session_id, created_at)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (transport, thread) DO NOTHING`,
		transport, thread, string(session), time.Now().UnixMicro(),
	); err != nil {
		return fmt.Errorf("store.BindExternalKey: insert: %w", err)
	}

	var existing string
	if err := tx.QueryRowContext(ctx,
		`SELECT session_id FROM external_keys WHERE transport = $1 AND thread = $2`,
		transport, thread,
	).Scan(&existing); err != nil {
		return fmt.Errorf("store.BindExternalKey: query: %w", err)
	}
	if existing != string(session) {
		return ErrExternalKeyTaken
	}

	return tx.Commit()
}

// LookupByKey returns the session ID bound to (transport, thread).
// Returns ErrSessionNotFound if no binding exists.
func (s *postgresStore) LookupByKey(ctx context.Context, transport, thread string) (app.SessionID, error) {
	var sid string
	err := s.db.QueryRowContext(ctx,
		`SELECT session_id FROM external_keys WHERE transport = $1 AND thread = $2`,
		transport, thread,
	).Scan(&sid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrSessionNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store.LookupByKey: %w", err)
	}
	return app.SessionID(sid), nil
}

// ListExternalKeys returns all (transport, thread) bindings for a session,
// ordered by created_at ascending (oldest first).
func (s *postgresStore) ListExternalKeys(ctx context.Context, session app.SessionID) ([]ExternalKey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT transport, thread, created_at
		 FROM external_keys
		 WHERE session_id = $1
		 ORDER BY created_at ASC`,
		string(session),
	)
	if err != nil {
		return nil, fmt.Errorf("store.ListExternalKeys: %w", err)
	}
	defer rows.Close()

	var out []ExternalKey
	for rows.Next() {
		var (
			transport, thread string
			createdAt         int64
		)
		if err := rows.Scan(&transport, &thread, &createdAt); err != nil {
			return nil, fmt.Errorf("store.ListExternalKeys: scan: %w", err)
		}
		out = append(out, ExternalKey{
			Transport: transport,
			Thread:    thread,
			CreatedAt: time.UnixMicro(createdAt),
		})
	}
	return out, rows.Err()
}

// ListSessionsByTransport returns sessions that have at least one external
// key with the given transport, newest-key-first. Pass limit=0 for no limit.
func (s *postgresStore) ListSessionsByTransport(ctx context.Context, transport string, limit int) ([]SessionSummary, error) {
	q := `SELECT s.id, s.app_id, s.app_version, s.started_at, s.last_turn, s.status
	      FROM sessions s
	      JOIN external_keys ek ON ek.session_id = s.id
	      WHERE ek.transport = $1
	      GROUP BY s.id
	      ORDER BY MAX(ek.created_at) DESC`
	args := []any{transport}
	if limit > 0 {
		q += " LIMIT $2"
		args = append(args, limit)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store.ListSessionsByTransport: %w", err)
	}
	defer rows.Close()
	return scanSessionSummaries(rows, "store.ListSessionsByTransport")
}

// scanSessionSummaries drains a (id, app_id, app_version, started_at,
// last_turn, status) rowset into SessionSummary values.
func scanSessionSummaries(rows *sql.Rows, op string) ([]SessionSummary, error) {
	var out []SessionSummary
	for rows.Next() {
		var (
			id, aid, aver, status string
			startedAt, lastTurn   int64
		)
		if err := rows.Scan(&id, &aid, &aver, &startedAt, &lastTurn, &status); err != nil {
			return nil, fmt.Errorf("%s: scan: %w", op, err)
		}
		out = append(out, SessionSummary{
			ID:         app.SessionID(id),
			AppID:      aid,
			AppVersion: aver,
			StartedAt:  time.UnixMicro(startedAt),
			LastTurn:   app.TurnNumber(lastTurn),
			Status:     status,
		})
	}
	return out, rows.Err()
}

// WithWriterLock acquires a session-scoped LEASE, runs fn, and releases the
// lease. Returns ErrSessionBusy if another holder's lease is still live.
// While fn runs a background heartbeat renews expires_at, so a long critical
// section is never taken over; a crashed holder's lease expires after
// pgLeaseTTL and the next acquirer takes it over atomically. This replaces
// the SQLite PID-liveness reaping, which is meaningless across hosts.
func (s *postgresStore) WithWriterLock(ctx context.Context, session app.SessionID, fn func() error) error {
	if session == "" {
		return fmt.Errorf("store.WithWriterLock: empty session ID")
	}
	if err := s.acquireLease(ctx, session); err != nil {
		return err
	}

	// Heartbeat until fn returns.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(s.heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				// Best-effort renewal; a failed beat only matters if it
				// persists past the TTL, at which point takeover is correct.
				_ = s.heartbeatLease(context.Background(), session)
			}
		}
	}()
	defer func() {
		close(stop)
		wg.Wait()
		_ = s.releaseLease(context.Background(), session)
	}()
	return fn()
}

// acquireLease attempts an atomic upsert-if-expired on the session_locks row.
// Exactly one of three outcomes: fresh insert (no row), takeover (row expired),
// or ErrSessionBusy (row live — including our own holder re-entering, which
// mirrors the SQLite same-pid ErrSessionBusy behavior).
func (s *postgresStore) acquireLease(ctx context.Context, session app.SessionID) error {
	now := time.Now()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO session_locks (session_id, holder_id, acquired_at, heartbeat_at, expires_at)
		 VALUES ($1, $2, $3, $3, $4)
		 ON CONFLICT (session_id) DO UPDATE
		    SET holder_id    = EXCLUDED.holder_id,
		        acquired_at  = EXCLUDED.acquired_at,
		        heartbeat_at = EXCLUDED.heartbeat_at,
		        expires_at   = EXCLUDED.expires_at
		  WHERE session_locks.expires_at <= EXCLUDED.acquired_at`,
		string(session), s.holderID, now.UnixMicro(), now.Add(s.leaseTTL).UnixMicro(),
	)
	if err != nil {
		return fmt.Errorf("store.acquireLease: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrSessionBusy
	}
	return nil
}

// heartbeatLease renews our lease. The holder_id guard means a lease we lost
// (e.g. taken over after a long stall) is never resurrected.
func (s *postgresStore) heartbeatLease(ctx context.Context, session app.SessionID) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx,
		`UPDATE session_locks
		    SET heartbeat_at = $1, expires_at = $2
		  WHERE session_id = $3 AND holder_id = $4`,
		now.UnixMicro(), now.Add(s.leaseTTL).UnixMicro(),
		string(session), s.holderID,
	)
	if err != nil {
		return fmt.Errorf("store.heartbeatLease: %w", err)
	}
	return nil
}

// releaseLease removes our lease row; a row another holder took over is left
// alone (holder_id guard).
func (s *postgresStore) releaseLease(ctx context.Context, session app.SessionID) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM session_locks WHERE session_id = $1 AND holder_id = $2`,
		string(session), s.holderID,
	)
	if err != nil {
		return fmt.Errorf("store.releaseLease: %w", err)
	}
	return nil
}
