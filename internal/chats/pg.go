package chats

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"kitsoki/internal/clock"
	"kitsoki/internal/ulid"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed schema_pg.sql
var chatsSchemaPGDDL string

// pgLeaseTTL is how long a Postgres chat lease stays valid without a
// heartbeat. Unlike the SQLite path, Postgres cannot probe owner-PID
// liveness (the server may be shared across hosts), so a crashed holder is
// reaped by time: any acquirer may take over a lease whose expires_at has
// passed. WithLock renews the lease every pgLeaseHeartbeat while fn runs
// (well inside the TTL window), so a live holder — chat drives routinely
// wrap agent calls far longer than the TTL — is never taken over; only a
// holder that stops beating (crash, partition) expires.
const (
	pgLeaseTTL       = 30 * time.Second
	pgLeaseHeartbeat = 10 * time.Second
)

// NewPostgresStore creates a Store backed by PostgreSQL (via the pgx stdlib
// driver) and applies the chats schema migration idempotently. The SQLite
// NewStore constructor is unchanged and remains the default code path;
// callers select Postgres explicitly by constructing through this function.
//
// The schema version is stamped in the chats_schema_meta table (the Postgres
// stand-in for SQLite's PRAGMA user_version). Because the Postgres schema
// was introduced at version 3 there are no pre-v3 Postgres databases to
// migrate from: any non-zero version other than expectedSchemaVersion is
// rejected outright.
func NewPostgresStore(db *sql.DB, opts ...Option) (*Store, error) {
	s := &Store{
		db:                  db,
		clock:               clock.Real(),
		dialect:             dialectPostgres,
		leaseHolder:         newLeaseHolder(),
		leaseHeartbeatEvery: pgLeaseHeartbeat,
	}
	for _, o := range opts {
		o(s)
	}

	gotVersion, err := pgChatsSchemaVersion(db)
	if err != nil {
		return nil, fmt.Errorf("chats.NewPostgresStore: read schema version: %w", err)
	}
	if gotVersion != 0 && gotVersion != expectedSchemaVersion {
		return nil, fmt.Errorf(
			"chats.NewPostgresStore: unexpected schema version %d (want %d) — DB written by a different kitsoki build; refusing to open",
			gotVersion, expectedSchemaVersion,
		)
	}

	if err := applyPGDDL(db, chatsSchemaPGDDL); err != nil {
		return nil, fmt.Errorf("chats.NewPostgresStore: schema migration: %w", err)
	}
	// Sanity-check post-migration: the DDL ends with the chats_schema_meta
	// upsert, so a successful apply must have stamped expectedSchemaVersion.
	if gotVersion, err = pgChatsSchemaVersion(db); err != nil {
		return nil, fmt.Errorf("chats.NewPostgresStore: read schema version after migration: %w", err)
	}
	if gotVersion != expectedSchemaVersion {
		return nil, fmt.Errorf(
			"chats.NewPostgresStore: post-migration schema version %d (want %d) — schema_pg.sql out of sync with expectedSchemaVersion",
			gotVersion, expectedSchemaVersion,
		)
	}
	return s, nil
}

// newLeaseHolder builds the opaque per-Store lease-holder token. host and
// pid make the token diagnosable in `SELECT * FROM chat_locks`; the ULID
// suffix keeps two Stores in one process (tests, embedded servers) from
// masquerading as each other's lease.
func newLeaseHolder() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%s:%d:%s", host, os.Getpid(), ulid.New())
}

// pgChatsSchemaVersion reads the stamped schema version, mapping "table (or
// its schema) does not exist yet" and "no row" to 0 (fresh database),
// mirroring what SQLite's PRAGMA user_version reports before first apply.
func pgChatsSchemaVersion(db *sql.DB) (int, error) {
	var v int
	err := db.QueryRow(`SELECT version FROM chats.chats_schema_meta WHERE id = 1`).Scan(&v)
	switch {
	case err == nil:
		return v, nil
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil
	case isPGMissingRelation(err):
		return 0, nil
	default:
		return 0, err
	}
}

// isPGMissingRelation reports whether err is Postgres SQLSTATE 42P01
// (undefined_table) or 3F000 (invalid_schema_name) — both mean the chats
// schema has not been applied to this database yet.
func isPGMissingRelation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "42P01" || pgErr.Code == "3F000")
}

// applyPGDDL executes a DDL script statement by statement inside one
// transaction. The pgx extended query protocol rejects multi-statement
// strings, so the script is split first.
func applyPGDDL(db *sql.DB, script string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range splitSQLStatements(script) {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("apply %q: %w", firstLine(stmt), err)
		}
	}
	return tx.Commit()
}

// splitSQLStatements splits a DDL script on top-level semicolons, stripping
// `--` line comments. The script must not use semicolons inside string
// literals or dollar-quoted bodies (schema_pg.sql does not).
func splitSQLStatements(script string) []string {
	var stmts []string
	for _, chunk := range strings.Split(script, ";") {
		var lines []string
		for _, line := range strings.Split(chunk, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "--") {
				continue
			}
			lines = append(lines, line)
		}
		if stmt := strings.TrimSpace(strings.Join(lines, "\n")); stmt != "" {
			stmts = append(stmts, stmt)
		}
	}
	return stmts
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ─── lease-based lock (Postgres chat_locks) ───────────────────────────────────

// chatLeaseBusyError is the Postgres counterpart of chatBusyError: it carries
// the current lease holder instead of a PID while still satisfying
// errors.Is(err, ErrChatBusy).
type chatLeaseBusyError struct {
	holder string
	ts     time.Time
}

func (e *chatLeaseBusyError) Error() string {
	return fmt.Sprintf("chats: chat busy: leased by %s since %s", e.holder, e.ts.Format(time.RFC3339))
}

func (e *chatLeaseBusyError) Is(target error) bool {
	return target == ErrChatBusy
}

// acquireChatLease is the Postgres implementation behind WithLock. A single
// upsert either inserts a fresh lease or takes over an expired one — the
// WHERE clause on the conflict update makes the takeover atomic, so at most
// one contender wins even under concurrent acquires. An unexpired lease is
// busy regardless of holder: like the SQLite path, re-entry from the same
// process is not supported.
func (s *Store) acquireChatLease(ctx context.Context, chatID string) error {
	for attempt := 0; attempt < 2; attempt++ {
		now := s.clock.Now()
		nowMicro := now.UnixMicro()
		expires := now.Add(pgLeaseTTL).UnixMicro()

		res, err := s.db.ExecContext(ctx, s.q(`
			INSERT INTO chat_locks (chat_id, holder, acquired_at, heartbeat_at, expires_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (chat_id) DO UPDATE
			SET holder = EXCLUDED.holder, acquired_at = EXCLUDED.acquired_at,
			    heartbeat_at = EXCLUDED.heartbeat_at, expires_at = EXCLUDED.expires_at
			WHERE chat_locks.expires_at <= EXCLUDED.acquired_at`),
			chatID, s.leaseHolder, nowMicro, nowMicro, expires,
		)
		if err != nil {
			return fmt.Errorf("chats.acquireChatLease: upsert: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			return nil
		}

		// Lease held and unexpired — read the holder for a useful error.
		var (
			holder      string
			heartbeatAt int64
		)
		err = s.db.QueryRowContext(ctx, s.q(
			`SELECT holder, heartbeat_at FROM chat_locks WHERE chat_id = ?`), chatID,
		).Scan(&holder, &heartbeatAt)
		if errors.Is(err, sql.ErrNoRows) {
			// Holder released between the upsert and the read — retry.
			continue
		}
		if err != nil {
			return fmt.Errorf("chats.acquireChatLease: read holder: %w", err)
		}
		return &chatLeaseBusyError{holder: holder, ts: time.UnixMicro(heartbeatAt)}
	}
	return ErrChatBusy
}

// releaseChatLease deletes this Store's lease. Scoped to holder so a lease
// taken over after expiry is never deleted by the previous owner's deferred
// release.
func (s *Store) releaseChatLease(ctx context.Context, chatID string) error {
	if _, err := s.db.ExecContext(ctx, s.q(
		`DELETE FROM chat_locks WHERE chat_id = ? AND holder = ?`),
		chatID, s.leaseHolder,
	); err != nil {
		return fmt.Errorf("chats.releaseChatLease: %w", err)
	}
	return nil
}

// heartbeatChatLease extends this Store's lease. Returns an error if this
// Store does not hold the lease (misuse guard, mirroring the SQLite
// Heartbeat contract).
func (s *Store) heartbeatChatLease(ctx context.Context, chatID string) error {
	now := s.clock.Now()
	res, err := s.db.ExecContext(ctx, s.q(
		`UPDATE chat_locks SET heartbeat_at = ?, expires_at = ? WHERE chat_id = ? AND holder = ?`),
		now.UnixMicro(), now.Add(pgLeaseTTL).UnixMicro(), chatID, s.leaseHolder,
	)
	if err != nil {
		return fmt.Errorf("chats.Heartbeat: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("chats.Heartbeat: this process does not own the lock for chat %s", chatID)
	}
	return nil
}
