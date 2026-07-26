package webauth

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	// Registers the "pgx" database/sql driver for callers that open the
	// Postgres handle themselves (kitsoki/internal/dbruntime uses it too).
	_ "github.com/jackc/pgx/v5/stdlib"
)

// dialect selects the SQL flavor a Store speaks. The zero value is SQLite so
// every existing constructor keeps its behavior without change.
type dialect int

const (
	dialectSQLite dialect = iota
	dialectPostgres
)

// pgSchema is the Postgres translation of sqliteSchema: same tables, same
// columns, same unix-millis time encoding. Dialect deltas only — BIGINT for
// the SQLite INTEGER columns (github_id and every timestamp), no STRICT,
// which Postgres's typing subsumes, and a dedicated "webauth" schema
// (schema-per-subsystem on a shared database).
const pgSchema = `
CREATE SCHEMA IF NOT EXISTS webauth;

CREATE TABLE IF NOT EXISTS webauth.webauth_users (
  id            TEXT PRIMARY KEY,
  github_id     BIGINT NOT NULL UNIQUE,
  github_login  TEXT NOT NULL,
  display_name  TEXT NOT NULL DEFAULT '',
  role          TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('admin','user')),
  created_at    BIGINT NOT NULL,
  last_login_at BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS webauth.webauth_invites (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  role        TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('admin','user')),
  code_hash   TEXT NOT NULL UNIQUE,
  created_at  BIGINT NOT NULL,
  redeemed_at BIGINT,
  redeemed_by TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS webauth.webauth_sessions (
  id           TEXT PRIMARY KEY,
  token_hash   TEXT NOT NULL UNIQUE,
  user_id      TEXT NOT NULL REFERENCES webauth.webauth_users(id) ON DELETE CASCADE,
  created_at   BIGINT NOT NULL,
  expires_at   BIGINT NOT NULL,
  last_seen_at BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS webauth_sessions_user ON webauth.webauth_sessions(user_id);
CREATE INDEX IF NOT EXISTS webauth_sessions_expires ON webauth.webauth_sessions(expires_at);
`

// NewPostgresStore runs the idempotent Postgres schema DDL over db (pgx
// stdlib driver) and returns a Store speaking the Postgres dialect. The
// caller keeps ownership of db, same contract as NewStore.
func NewPostgresStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("webauth.NewPostgresStore: nil db")
	}
	if _, err := db.Exec(pgSchema); err != nil {
		return nil, fmt.Errorf("webauth.NewPostgresStore: schema migration: %w", err)
	}
	return &Store{db: db, now: time.Now, dialect: dialectPostgres}, nil
}

// webauthPGTables qualifies this subsystem's table references into the
// "webauth" schema (schema-per-subsystem on a shared database). Rewrites are
// anchored on the preceding SQL keyword so unrelated identifiers are never
// touched.
var webauthPGTables = strings.NewReplacer(
	"INTO webauth_users", "INTO webauth.webauth_users",
	"FROM webauth_users", "FROM webauth.webauth_users",
	"UPDATE webauth_users", "UPDATE webauth.webauth_users",
	"JOIN webauth_users", "JOIN webauth.webauth_users",
	"INTO webauth_invites", "INTO webauth.webauth_invites",
	"FROM webauth_invites", "FROM webauth.webauth_invites",
	"UPDATE webauth_invites", "UPDATE webauth.webauth_invites",
	"INTO webauth_sessions", "INTO webauth.webauth_sessions",
	"FROM webauth_sessions", "FROM webauth.webauth_sessions",
	"UPDATE webauth_sessions", "UPDATE webauth.webauth_sessions",
)

// q rewrites a canonical `?`-placeholder query for the store's dialect.
// SQLite queries pass through untouched; Postgres gets the table names
// schema-qualified into "webauth" plus ordinal $N placeholders. None of the
// package's queries embed a literal '?', so a plain byte scan is sufficient.
func (s *Store) q(query string) string {
	if s.dialect != dialectPostgres {
		return query
	}
	query = webauthPGTables.Replace(query)
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(query[i])
	}
	return b.String()
}
