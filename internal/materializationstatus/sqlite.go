package materializationstatus

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

type SQLiteStore struct {
	db      *sql.DB
	now     func() time.Time
	dialect dialect
}

func NewSQLiteStore(db *sql.DB, now func() time.Time) (*SQLiteStore, error) {
	if db == nil {
		return nil, fmt.Errorf("materialization status: database is required")
	}
	if now == nil {
		now = time.Now
	}
	store := &SQLiteStore{db: db, now: now}
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS materialization_projection_jobs (
  application_id TEXT NOT NULL,
  job_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  status TEXT NOT NULL,
  stages_json TEXT NOT NULL,
  artifacts_json TEXT NOT NULL,
  receipt_ids_json TEXT NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (application_id, job_id)
);
CREATE INDEX IF NOT EXISTS idx_materialization_projection_app_updated
  ON materialization_projection_jobs(application_id, updated_at DESC, job_id);
`); err != nil {
		return nil, fmt.Errorf("materialization status: initialize schema: %w", err)
	}
	return store, nil
}

func (s *SQLiteStore) Save(ctx context.Context, record Record) (Record, error) {
	var err error
	record, err = Normalize(record)
	if err != nil {
		return Record{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, fmt.Errorf("materialization status: begin save: %w", err)
	}
	defer tx.Rollback()
	if err := s.lockLifecycleKey(ctx, tx, record.ApplicationID, record.JobID); err != nil {
		return Record{}, err
	}
	current, getErr := scanRecord(tx.QueryRowContext(ctx, s.q(`
SELECT application_id,job_id,session_id,status,stages_json,artifacts_json,receipt_ids_json,updated_at
FROM materialization_projection_jobs
WHERE application_id=? AND job_id=?`), record.ApplicationID, record.JobID))
	switch getErr {
	case nil:
		record, err = mergeLifecycle(current, record)
		if err != nil {
			return Record{}, err
		}
	case sql.ErrNoRows:
	default:
		return Record{}, fmt.Errorf("materialization status: read before save: %w", getErr)
	}
	stages, _ := json.Marshal(record.Stages)
	artifacts, _ := json.Marshal(record.Artifacts)
	receipts, _ := json.Marshal(record.ReceiptIDs)
	_, err = tx.ExecContext(ctx, s.q(`
INSERT INTO materialization_projection_jobs
  (application_id,job_id,session_id,status,stages_json,artifacts_json,receipt_ids_json,updated_at)
VALUES (?,?,?,?,?,?,?,?)
ON CONFLICT(application_id,job_id) DO UPDATE SET
  session_id=excluded.session_id,
  status=excluded.status,
  stages_json=excluded.stages_json,
  artifacts_json=excluded.artifacts_json,
  receipt_ids_json=excluded.receipt_ids_json,
  updated_at=excluded.updated_at
`), record.ApplicationID, record.JobID, record.SessionID, record.Status,
		string(stages), string(artifacts), string(receipts), record.UpdatedAt.UnixMilli())
	if err != nil {
		return Record{}, fmt.Errorf("materialization status: save: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Record{}, fmt.Errorf("materialization status: commit save: %w", err)
	}
	return record, nil
}

func mergeLifecycle(current, next Record) (Record, error) {
	if IsTerminal(current.Status) {
		return current, nil
	}
	if IsTerminal(next.Status) {
		return next, nil
	}
	if current.Status == "awaiting_input" && next.Status == "running" {
		next.Status = current.Status
	}
	stageRank := map[string]int{"waiting": 0, "in-progress": 1, "complete": 2, "failed": 3}
	currentStages := make(map[string]Stage, len(current.Stages))
	for _, stage := range current.Stages {
		currentStages[stage.ID] = stage
	}
	for i, stage := range next.Stages {
		if prior, ok := currentStages[stage.ID]; ok && stageRank[prior.Status] > stageRank[stage.Status] {
			next.Stages[i] = prior
		}
	}
	artifacts := make(map[string]bool, len(next.Artifacts))
	for _, artifact := range next.Artifacts {
		artifacts[artifact.Handle] = true
	}
	for _, artifact := range current.Artifacts {
		if !artifacts[artifact.Handle] {
			next.Artifacts = append(next.Artifacts, artifact)
		}
	}
	if len(next.Artifacts) > 100 {
		return Record{}, fmt.Errorf("%w: merged projection exceeds artifact bounds", ErrInvalid)
	}
	if next.SessionID == "" {
		next.SessionID = current.SessionID
	}
	if current.UpdatedAt.After(next.UpdatedAt) {
		next.UpdatedAt = current.UpdatedAt
	}
	return next, nil
}

func (s *SQLiteStore) Get(ctx context.Context, applicationID, jobID string) (Record, bool, error) {
	if !opaque(applicationID) || !opaque(jobID) {
		return Record{}, false, fmt.Errorf("%w: application and job identities must be opaque", ErrInvalid)
	}
	record, err := scanRecord(s.db.QueryRowContext(ctx, s.q(`
SELECT application_id,job_id,session_id,status,stages_json,artifacts_json,receipt_ids_json,updated_at
FROM materialization_projection_jobs
WHERE application_id=? AND job_id=?`), applicationID, jobID))
	if err == sql.ErrNoRows {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("materialization status: get: %w", err)
	}
	return record, true, nil
}

func (s *SQLiteStore) List(ctx context.Context, applicationID string, limit int) ([]Record, error) {
	if !opaque(applicationID) {
		return nil, fmt.Errorf("%w: application identity must be opaque", ErrInvalid)
	}
	if limit < 1 || limit > 1001 {
		return nil, fmt.Errorf("%w: limit must be between 1 and 1001", ErrInvalid)
	}
	rows, err := s.db.QueryContext(ctx, s.q(`
SELECT application_id,job_id,session_id,status,stages_json,artifacts_json,receipt_ids_json,updated_at
FROM materialization_projection_jobs
WHERE application_id=?
ORDER BY updated_at DESC, job_id
LIMIT ?`), applicationID, limit)
	if err != nil {
		return nil, fmt.Errorf("materialization status: list: %w", err)
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("materialization status: scan: %w", err)
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(...any) error
}

func scanRecord(row rowScanner) (Record, error) {
	var record Record
	var stages, artifacts, receipts string
	var updated int64
	if err := row.Scan(&record.ApplicationID, &record.JobID, &record.SessionID,
		&record.Status, &stages, &artifacts, &receipts, &updated); err != nil {
		return Record{}, err
	}
	if err := json.Unmarshal([]byte(stages), &record.Stages); err != nil {
		return Record{}, fmt.Errorf("decode stages for %s: %w", record.JobID, err)
	}
	if err := json.Unmarshal([]byte(artifacts), &record.Artifacts); err != nil {
		return Record{}, fmt.Errorf("decode artifacts for %s: %w", record.JobID, err)
	}
	if err := json.Unmarshal([]byte(receipts), &record.ReceiptIDs); err != nil {
		return Record{}, fmt.Errorf("decode receipts for %s: %w", record.JobID, err)
	}
	record.UpdatedAt = time.UnixMilli(updated).UTC()
	record, err := Normalize(record)
	if err != nil {
		return Record{}, fmt.Errorf("validate stored record %s: %w", record.JobID, err)
	}
	return record, nil
}

func (s *SQLiteStore) InterruptActive(ctx context.Context, reason string) (int, error) {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("materialization status: begin restart sweep: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, s.activeRowsQuery())
	if err != nil {
		return 0, fmt.Errorf("materialization status: list active jobs: %w", err)
	}
	var records []Record
	for rows.Next() {
		var record Record
		var stages, artifacts, receipts string
		var updated int64
		if err := rows.Scan(&record.ApplicationID, &record.JobID, &record.SessionID,
			&record.Status, &stages, &artifacts, &receipts, &updated); err != nil {
			rows.Close()
			return 0, err
		}
		if err := json.Unmarshal([]byte(stages), &record.Stages); err != nil {
			rows.Close()
			return 0, err
		}
		if err := json.Unmarshal([]byte(artifacts), &record.Artifacts); err != nil {
			rows.Close()
			return 0, err
		}
		_ = json.Unmarshal([]byte(receipts), &record.ReceiptIDs)
		record.Status = "interrupted"
		record.ReceiptIDs = nil
		record.UpdatedAt = now
		record.ReceiptIDs = []string{CanonicalReceiptID(record)}
		jobID := record.JobID
		record, err = Normalize(record)
		if err != nil {
			rows.Close()
			return 0, fmt.Errorf("materialization status: validate active job %s: %w", jobID, err)
		}
		records = append(records, record)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, record := range records {
		stages, _ := json.Marshal(record.Stages)
		artifacts, _ := json.Marshal(record.Artifacts)
		receipts, _ := json.Marshal(record.ReceiptIDs)
		if _, err := tx.ExecContext(ctx, s.q(`
UPDATE materialization_projection_jobs
SET status=?,stages_json=?,artifacts_json=?,receipt_ids_json=?,updated_at=?
WHERE application_id=? AND job_id=?`),
			record.Status, string(stages), string(artifacts), string(receipts),
			record.UpdatedAt.UnixMilli(), record.ApplicationID, record.JobID); err != nil {
			return 0, fmt.Errorf("materialization status: interrupt %s: %w", record.JobID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("materialization status: commit restart sweep: %w", err)
	}
	_ = reason
	return len(records), nil
}
