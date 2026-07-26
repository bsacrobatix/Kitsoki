package materializationstatus

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type dialect int

const (
	dialectSQLite dialect = iota
	dialectPostgres
)

// SQLStore is the backend-neutral name for the lifecycle store. SQLiteStore
// predates daemon Postgres support and remains as a compatibility alias.
type SQLStore = SQLiteStore

const postgresSchema = `
CREATE SCHEMA IF NOT EXISTS materializationstatus;
CREATE TABLE IF NOT EXISTS materializationstatus.materialization_projection_jobs (
  application_id TEXT NOT NULL,
  job_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  status TEXT NOT NULL,
  stages_json TEXT NOT NULL,
  artifacts_json TEXT NOT NULL,
  receipt_ids_json TEXT NOT NULL,
  updated_at BIGINT NOT NULL,
  PRIMARY KEY (application_id, job_id)
);
CREATE INDEX IF NOT EXISTS materialization_projection_app_updated
  ON materializationstatus.materialization_projection_jobs(application_id, updated_at DESC, job_id);
`

// NewPostgresStore persists the same bounded lifecycle projection as
// NewSQLiteStore in a dedicated Postgres schema.
func NewPostgresStore(db *sql.DB, now func() time.Time) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("materialization status: database is required")
	}
	if now == nil {
		now = time.Now
	}
	if _, err := db.Exec(postgresSchema); err != nil {
		return nil, fmt.Errorf("materialization status: initialize postgres schema: %w", err)
	}
	return &SQLStore{db: db, now: now, dialect: dialectPostgres}, nil
}

func (s *SQLiteStore) q(query string) string {
	if s.dialect != dialectPostgres {
		return query
	}
	query = strings.ReplaceAll(
		query,
		"materialization_projection_jobs",
		"materializationstatus.materialization_projection_jobs",
	)
	var b strings.Builder
	b.Grow(len(query) + 16)
	ordinal := 0
	for i := 0; i < len(query); i++ {
		if query[i] != '?' {
			b.WriteByte(query[i])
			continue
		}
		ordinal++
		b.WriteByte('$')
		b.WriteString(strconv.Itoa(ordinal))
	}
	return b.String()
}

func (s *SQLiteStore) lockLifecycleKey(ctx context.Context, tx *sql.Tx, applicationID, jobID string) error {
	if s.dialect != dialectPostgres {
		return nil
	}
	if _, err := tx.ExecContext(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1 || ':' || $2, 0))`,
		applicationID,
		jobID,
	); err != nil {
		return fmt.Errorf("materialization status: lock lifecycle key: %w", err)
	}
	return nil
}

func (s *SQLiteStore) activeRowsQuery() string {
	query := s.q(`
SELECT application_id,job_id,session_id,status,stages_json,artifacts_json,receipt_ids_json,updated_at
FROM materialization_projection_jobs
WHERE status IN ('running','awaiting_input')
ORDER BY application_id,job_id`)
	if s.dialect == dialectPostgres {
		query += " FOR UPDATE"
	}
	return query
}
