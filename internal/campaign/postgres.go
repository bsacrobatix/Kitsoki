package campaign

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"kitsoki/internal/ulid"
)

// pgSchema keeps campaign control state separate from session and artifact-job
// tables while preserving the SQLite store's millisecond timestamp contract.
const pgSchema = `
CREATE SCHEMA IF NOT EXISTS campaign;

CREATE TABLE IF NOT EXISTS campaign.campaign_schedules (
  app_id TEXT NOT NULL,
  campaign_id TEXT NOT NULL,
  title TEXT NOT NULL,
  enabled INTEGER NOT NULL,
  paused INTEGER NOT NULL,
  cadence_nanos BIGINT NOT NULL,
  max_ticks_per_day INTEGER NOT NULL,
  max_concurrency INTEGER NOT NULL,
  action_story TEXT NOT NULL,
  action_intent TEXT NOT NULL,
  action_input_json TEXT NOT NULL,
  source_digest TEXT NOT NULL,
  definition_hash TEXT NOT NULL,
  next_due_at BIGINT NOT NULL,
  last_dispatch_at BIGINT,
  ticks_day TEXT NOT NULL DEFAULT '',
  ticks_today INTEGER NOT NULL DEFAULT 0,
  running INTEGER NOT NULL DEFAULT 0,
  last_job_ref TEXT NOT NULL DEFAULT '',
  last_status TEXT NOT NULL DEFAULT '',
  last_error TEXT NOT NULL DEFAULT '',
  updated_at BIGINT NOT NULL,
  PRIMARY KEY (app_id, campaign_id)
);

CREATE INDEX IF NOT EXISTS campaign_schedules_due
  ON campaign.campaign_schedules(app_id, enabled, paused, next_due_at);

CREATE TABLE IF NOT EXISTS campaign.campaign_dispatches (
  idempotency_key TEXT PRIMARY KEY,
  app_id TEXT NOT NULL,
  campaign_id TEXT NOT NULL,
  due_at BIGINT NOT NULL,
  job_ref TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  owner_id TEXT NOT NULL DEFAULT '',
  fence BIGINT NOT NULL DEFAULT 0,
  lease_expires_at BIGINT,
  started_at BIGINT NOT NULL,
  finished_at BIGINT
);

CREATE INDEX IF NOT EXISTS campaign_dispatches_app_started
  ON campaign.campaign_dispatches(app_id, started_at DESC);

ALTER TABLE campaign.campaign_dispatches
  ADD COLUMN IF NOT EXISTS owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE campaign.campaign_dispatches
  ADD COLUMN IF NOT EXISTS fence BIGINT NOT NULL DEFAULT 0;
ALTER TABLE campaign.campaign_dispatches
  ADD COLUMN IF NOT EXISTS lease_expires_at BIGINT;
`

type sqlDialect int

const (
	dialectSQLite sqlDialect = iota
	dialectPostgres
)

// SQLStore names the dialect-neutral campaign store. SQLiteStore remains the
// concrete type for compatibility with existing callers.
type SQLStore = SQLiteStore

// NewPostgresStore applies the campaign schema on a shared pgx-stdlib handle.
// ClaimDue uses FOR UPDATE SKIP LOCKED so multiple daemon processes can claim
// independent schedules without double-dispatching one cadence slot.
func NewPostgresStore(db *sql.DB, opts ...Option) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("campaign.NewPostgresStore: nil db")
	}
	if _, err := db.Exec(pgSchema); err != nil {
		return nil, fmt.Errorf("campaign.NewPostgresStore: schema migration: %w", err)
	}
	store := &SQLStore{
		db: db, dialect: dialectPostgres, ownerID: "campaign_" + ulid.New(),
		now: time.Now,
	}
	for _, option := range opts {
		option(store)
	}
	return store, nil
}

var pgTables = strings.NewReplacer(
	"campaign_schedules", "campaign.campaign_schedules",
	"campaign_dispatches", "campaign.campaign_dispatches",
)

func (s *SQLiteStore) q(query string) string {
	if s.dialect != dialectPostgres {
		return query
	}
	// SQLite's scalar MAX(a, b) is PostgreSQL's GREATEST(a, b).
	query = strings.ReplaceAll(query, "MAX(", "GREATEST(")
	return rebindPositional(pgTables.Replace(query))
}

func rebindPositional(query string) string {
	if !strings.Contains(query, "?") {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 16)
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
