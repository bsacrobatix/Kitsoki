package artifactjob

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// pgSchema mirrors sqliteSchema for Postgres. The tables live in a dedicated
// "artifactjob" schema (schema-per-subsystem on a shared database), and the
// millisecond timestamps use BIGINT because Postgres INTEGER is 32-bit.
const pgSchema = `
CREATE SCHEMA IF NOT EXISTS artifactjob;

CREATE TABLE IF NOT EXISTS artifactjob.artifact_jobs (
  id                       TEXT PRIMARY KEY,
  session_id               TEXT NOT NULL DEFAULT '',
  app_id                   TEXT NOT NULL DEFAULT '',
  story                    TEXT NOT NULL DEFAULT '',
  origin_kind              TEXT NOT NULL DEFAULT '',
  origin_ref               TEXT NOT NULL DEFAULT '',
  origin_url               TEXT NOT NULL DEFAULT '',
  status                   TEXT NOT NULL,
  run_url                  TEXT NOT NULL DEFAULT '',
  trace_path               TEXT NOT NULL DEFAULT '',
  workspace_instance_id    TEXT NOT NULL DEFAULT '',
  terminal_artifact_handle TEXT NOT NULL DEFAULT '',
  summary                  TEXT NOT NULL DEFAULT '',
  phase                    TEXT NOT NULL DEFAULT '',
  visibility               TEXT NOT NULL DEFAULT 'local',
  owner                    TEXT NOT NULL DEFAULT '',
  interrupted_reason       TEXT NOT NULL DEFAULT '',
  created_at               BIGINT NOT NULL,
  updated_at               BIGINT NOT NULL,
  finished_at              BIGINT
);

CREATE INDEX IF NOT EXISTS artifact_jobs_app_story_updated ON artifactjob.artifact_jobs(app_id, story, updated_at DESC);
CREATE INDEX IF NOT EXISTS artifact_jobs_session_status ON artifactjob.artifact_jobs(session_id, status);
CREATE INDEX IF NOT EXISTS artifact_jobs_origin ON artifactjob.artifact_jobs(origin_kind, origin_ref);

CREATE TABLE IF NOT EXISTS artifactjob.artifact_runs (
  job_id      TEXT PRIMARY KEY,
  session_id  TEXT NOT NULL,
  story       TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL,
  started_at  BIGINT NOT NULL,
  ended_at    BIGINT,
  last_turn   BIGINT NOT NULL DEFAULT 0,
  trace_path  TEXT NOT NULL,
  FOREIGN KEY(job_id) REFERENCES artifactjob.artifact_jobs(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS artifact_runs_session ON artifactjob.artifact_runs(session_id);
CREATE INDEX IF NOT EXISTS artifact_runs_status_started ON artifactjob.artifact_runs(status, started_at DESC);

CREATE TABLE IF NOT EXISTS artifactjob.artifact_run_artifacts (
  job_id      TEXT NOT NULL,
  handle      TEXT NOT NULL,
  kind        TEXT NOT NULL,
  mime        TEXT NOT NULL,
  label       TEXT NOT NULL DEFAULT '',
  path        TEXT NOT NULL,
  size_bytes  BIGINT NOT NULL DEFAULT 0,
  created_at  BIGINT NOT NULL,
  PRIMARY KEY(job_id, handle),
  FOREIGN KEY(job_id) REFERENCES artifactjob.artifact_runs(job_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS artifact_run_artifacts_job_created ON artifactjob.artifact_run_artifacts(job_id, created_at);
`

// dialect selects the SQL flavor a store emits. SQLite stays the default; the
// Postgres value is only set by NewPostgresStore.
type dialect int

const (
	dialectSQLite dialect = iota
	dialectPostgres
)

// SQLStore is the dialect-neutral name for the SQL-backed store. SQLiteStore
// predates the Postgres backend and is kept as the canonical declaration so
// existing callers (and method sets) are untouched.
type SQLStore = SQLiteStore

// NewPostgresStore stores the same rows on a shared Postgres handle (pgx
// stdlib driver). Tables are created under the dedicated "artifactjob" schema
// so subsystems sharing one database stay disjoint. The SQLite path is
// unchanged and remains the default; callers opt into Postgres explicitly.
func NewPostgresStore(db *sql.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("artifactjob.NewPostgresStore: nil db")
	}
	if _, err := db.Exec(pgSchema); err != nil {
		return nil, fmt.Errorf("artifactjob.NewPostgresStore: schema migration: %w", err)
	}
	return &SQLStore{db: db, dialect: dialectPostgres, now: time.Now}, nil
}

// pgTables qualifies this subsystem's table names into the artifactjob schema.
// Longest name first so artifact_run_artifacts is not clobbered by the
// artifact_runs rewrite (the Replacer tries patterns in argument order).
var pgTables = strings.NewReplacer(
	"artifact_run_artifacts", "artifactjob.artifact_run_artifacts",
	"artifact_runs", "artifactjob.artifact_runs",
	"artifact_jobs", "artifactjob.artifact_jobs",
)

// q adapts a canonical (SQLite-shaped) query to the store's dialect: for
// Postgres it schema-qualifies the table names and converts ?-placeholders to
// $N ordinals. SQLite queries pass through untouched.
func (s *SQLiteStore) q(query string) string {
	if s.dialect != dialectPostgres {
		return query
	}
	return rebindPositional(pgTables.Replace(query))
}

// rebindPositional converts ?-placeholders to Postgres $N ordinals. Queries in
// this package never contain a literal '?'.
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
