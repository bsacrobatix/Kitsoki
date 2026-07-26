package applicationjob

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var ErrNotFound = errors.New("applicationjob: not found")

// Store persists private target application/session/child identities. Public
// callers receive only the matching artifactjob.Job identity.
type Store interface {
	Register(context.Context, Record) (Record, error)
	BindChild(context.Context, string, DispatchResult) (Record, error)
	Complete(context.Context, string, []string, string) (Record, error)
	Get(context.Context, string) (Record, error)
}

type dialect int

const (
	dialectSQLite dialect = iota
	dialectPostgres
)

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS application_job_mappings (
  job_ref                TEXT PRIMARY KEY,
  caller_application_id  TEXT NOT NULL,
  template_id            TEXT NOT NULL,
  target_application_id  TEXT NOT NULL,
  target_event           TEXT NOT NULL,
  artifact_outputs_json  TEXT NOT NULL,
  primary_output         TEXT NOT NULL,
  max_input_bytes        INTEGER NOT NULL,
  max_runtime_seconds    INTEGER NOT NULL,
  target_route_id        TEXT NOT NULL DEFAULT '',
  target_session_id      TEXT NOT NULL DEFAULT '',
  child_job_id           TEXT NOT NULL DEFAULT '',
  input_digest           TEXT NOT NULL,
  artifact_handles_json  TEXT NOT NULL DEFAULT '[]',
  primary_handle         TEXT NOT NULL DEFAULT '',
  created_at             INTEGER NOT NULL,
  updated_at             INTEGER NOT NULL
) STRICT;

CREATE UNIQUE INDEX IF NOT EXISTS application_job_mappings_replay
ON application_job_mappings(caller_application_id, template_id, input_digest);
`

const postgresSchema = `
CREATE SCHEMA IF NOT EXISTS applicationjob;

CREATE TABLE IF NOT EXISTS applicationjob.application_job_mappings (
  job_ref                TEXT PRIMARY KEY,
  caller_application_id  TEXT NOT NULL,
  template_id            TEXT NOT NULL,
  target_application_id  TEXT NOT NULL,
  target_event           TEXT NOT NULL,
  artifact_outputs_json  TEXT NOT NULL,
  primary_output         TEXT NOT NULL,
  max_input_bytes        BIGINT NOT NULL,
  max_runtime_seconds    BIGINT NOT NULL,
  target_route_id        TEXT NOT NULL DEFAULT '',
  target_session_id      TEXT NOT NULL DEFAULT '',
  child_job_id           TEXT NOT NULL DEFAULT '',
  input_digest           TEXT NOT NULL,
  artifact_handles_json  TEXT NOT NULL DEFAULT '[]',
  primary_handle         TEXT NOT NULL DEFAULT '',
  created_at             BIGINT NOT NULL,
  updated_at             BIGINT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS application_job_mappings_replay
ON applicationjob.application_job_mappings(caller_application_id, template_id, input_digest);
`

type SQLStore struct {
	db      *sql.DB
	dialect dialect
	now     func() time.Time
}

func NewSQLiteStore(db *sql.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("applicationjob.NewSQLiteStore: nil db")
	}
	if _, err := db.Exec(sqliteSchema); err != nil {
		return nil, fmt.Errorf("applicationjob.NewSQLiteStore: schema migration: %w", err)
	}
	return &SQLStore{db: db, now: time.Now}, nil
}

func NewPostgresStore(db *sql.DB) (*SQLStore, error) {
	if db == nil {
		return nil, fmt.Errorf("applicationjob.NewPostgresStore: nil db")
	}
	if _, err := db.Exec(postgresSchema); err != nil {
		return nil, fmt.Errorf("applicationjob.NewPostgresStore: schema migration: %w", err)
	}
	return &SQLStore{db: db, dialect: dialectPostgres, now: time.Now}, nil
}

func (s *SQLStore) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

func (s *SQLStore) Register(ctx context.Context, record Record) (Record, error) {
	now := s.now().UTC()
	outputs, err := json.Marshal(record.ArtifactOutputs)
	if err != nil {
		return Record{}, fmt.Errorf("applicationjob.Register: encode artifact outputs: %w", err)
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	_, err = s.db.ExecContext(ctx, s.q(`
		INSERT INTO application_job_mappings
		  (job_ref, caller_application_id, template_id, target_application_id,
		   target_event, artifact_outputs_json, primary_output, max_input_bytes,
		   max_runtime_seconds, target_route_id, target_session_id, child_job_id,
		   input_digest, artifact_handles_json, primary_handle, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		record.JobRef, record.CallerApplicationID, record.TemplateID,
		record.TargetApplicationID, record.TargetEvent, string(outputs),
		record.PrimaryOutput, record.MaxInputBytes, record.MaxRuntimeSeconds,
		record.TargetRouteID, record.TargetSessionID, record.ChildJobID, record.InputDigest,
		"[]", record.PrimaryHandle, record.CreatedAt.UnixMilli(), record.UpdatedAt.UnixMilli(),
	)
	if err != nil {
		return Record{}, fmt.Errorf("applicationjob.Register: %w", err)
	}
	return record, nil
}

func (s *SQLStore) BindChild(ctx context.Context, jobRef string, result DispatchResult) (Record, error) {
	now := s.now().UTC()
	res, err := s.db.ExecContext(ctx, s.q(`
		UPDATE application_job_mappings
		SET target_route_id=?, target_session_id=?, child_job_id=?, updated_at=?
		WHERE job_ref=?`),
		result.RouteID, result.SessionID, result.ChildID, now.UnixMilli(), jobRef,
	)
	if err != nil {
		return Record{}, fmt.Errorf("applicationjob.BindChild: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return Record{}, err
	}
	if affected == 0 {
		return Record{}, ErrNotFound
	}
	return s.Get(ctx, jobRef)
}

func (s *SQLStore) Complete(ctx context.Context, jobRef string, artifacts []string, primary string) (Record, error) {
	raw, err := json.Marshal(artifacts)
	if err != nil {
		return Record{}, fmt.Errorf("applicationjob.Complete: encode artifacts: %w", err)
	}
	now := s.now().UTC()
	res, err := s.db.ExecContext(ctx, s.q(`
		UPDATE application_job_mappings
		SET artifact_handles_json=?, primary_handle=?, updated_at=?
		WHERE job_ref=?`), string(raw), primary, now.UnixMilli(), jobRef)
	if err != nil {
		return Record{}, fmt.Errorf("applicationjob.Complete: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return Record{}, err
	}
	if affected == 0 {
		return Record{}, ErrNotFound
	}
	return s.Get(ctx, jobRef)
}

func (s *SQLStore) Get(ctx context.Context, jobRef string) (Record, error) {
	row := s.db.QueryRowContext(ctx, s.q(`
		SELECT job_ref, caller_application_id, template_id, target_application_id,
		       target_event, artifact_outputs_json, primary_output, max_input_bytes,
		       max_runtime_seconds, target_route_id, target_session_id, child_job_id,
		       input_digest, artifact_handles_json, primary_handle, created_at, updated_at
		FROM application_job_mappings WHERE job_ref=?`), jobRef)
	var record Record
	var outputs, artifacts string
	var created, updated int64
	if err := row.Scan(
		&record.JobRef, &record.CallerApplicationID, &record.TemplateID,
		&record.TargetApplicationID, &record.TargetEvent, &outputs,
		&record.PrimaryOutput, &record.MaxInputBytes, &record.MaxRuntimeSeconds,
		&record.TargetRouteID,
		&record.TargetSessionID, &record.ChildJobID, &record.InputDigest,
		&artifacts, &record.PrimaryHandle,
		&created, &updated,
	); errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	} else if err != nil {
		return Record{}, fmt.Errorf("applicationjob.Get: %w", err)
	}
	if err := json.Unmarshal([]byte(outputs), &record.ArtifactOutputs); err != nil {
		return Record{}, fmt.Errorf("applicationjob.Get: decode artifact outputs: %w", err)
	}
	if err := json.Unmarshal([]byte(artifacts), &record.Artifacts); err != nil {
		return Record{}, fmt.Errorf("applicationjob.Get: decode artifacts: %w", err)
	}
	record.CreatedAt = time.UnixMilli(created).UTC()
	record.UpdatedAt = time.UnixMilli(updated).UTC()
	return record, nil
}

func (s *SQLStore) q(query string) string {
	if s.dialect != dialectPostgres {
		return query
	}
	query = strings.ReplaceAll(
		query,
		"application_job_mappings",
		"applicationjob.application_job_mappings",
	)
	var b strings.Builder
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
