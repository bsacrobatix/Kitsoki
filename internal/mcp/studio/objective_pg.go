package studio

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// pgObjectiveSchema keeps objective authority records and their append-only
// receipts in a dedicated "studio" schema (schema-per-subsystem on a shared
// database, mirroring internal/artifactjob). Receipts are immutable evidence:
// a trigger rejects UPDATE/DELETE at the database layer so no future code path
// can silently rewrite history, and global_pos gives cross-objective tailing
// the same shape as the events table's stream position.
const pgObjectiveSchema = `
CREATE SCHEMA IF NOT EXISTS studio;

CREATE TABLE IF NOT EXISTS studio.objectives (
  id         TEXT PRIMARY KEY,
  status     TEXT NOT NULL,
  revision   BIGINT NOT NULL,
  record     JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS studio.objective_receipts (
  objective_id TEXT NOT NULL REFERENCES studio.objectives(id),
  sequence     BIGINT NOT NULL,
  global_pos   BIGSERIAL,
  type         TEXT NOT NULL,
  record       JSONB NOT NULL,
  recorded_at  TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (objective_id, sequence)
);

CREATE UNIQUE INDEX IF NOT EXISTS objective_receipts_global_pos ON studio.objective_receipts(global_pos);

CREATE OR REPLACE FUNCTION studio.objective_receipts_immutable() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'studio.objective_receipts is append-only';
END $$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS objective_receipts_immutable ON studio.objective_receipts;
CREATE TRIGGER objective_receipts_immutable
  BEFORE UPDATE OR DELETE ON studio.objective_receipts
  FOR EACH ROW EXECUTE FUNCTION studio.objective_receipts_immutable();
`

// PostgresObjectiveStore is the durable ObjectiveStore for the Postgres
// backend. Receipts get a server-assigned monotonic per-objective sequence
// inside one transaction (the objective row is locked FOR UPDATE), so
// concurrent appenders — including ones in other processes, which the
// ObjectiveService mutex cannot see — never race a read-modify-write counter.
// SQLite/JSONL deployments are untouched: nothing constructs this store unless
// a host explicitly injects a Postgres handle.
type PostgresObjectiveStore struct {
	db *sql.DB
}

// NewPostgresObjectiveStore migrates the studio schema on the shared handle
// (pgx stdlib driver) and returns the durable store. The caller keeps
// ownership of db; the store never closes it.
func NewPostgresObjectiveStore(db *sql.DB) (*PostgresObjectiveStore, error) {
	if db == nil {
		return nil, errors.New("studio.NewPostgresObjectiveStore: nil db")
	}
	if _, err := db.Exec(pgObjectiveSchema); err != nil {
		return nil, fmt.Errorf("studio.NewPostgresObjectiveStore: schema migration: %w", err)
	}
	return &PostgresObjectiveStore{db: db}, nil
}

func (s *PostgresObjectiveStore) Create(ctx context.Context, objective Objective) error {
	record, err := json.Marshal(objective)
	if err != nil {
		return fmt.Errorf("marshal objective %s: %w", objective.ID, err)
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO studio.objectives (id, status, revision, record, updated_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (id) DO NOTHING`,
		objective.ID, string(objective.Status), objective.Revision, record, objective.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create objective %s: %w", objective.ID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("objective %q already exists", objective.ID)
	}
	return nil
}

func (s *PostgresObjectiveStore) Get(ctx context.Context, id string) (Objective, error) {
	var record []byte
	err := s.db.QueryRowContext(ctx, `SELECT record FROM studio.objectives WHERE id = $1`, id).Scan(&record)
	if errors.Is(err, sql.ErrNoRows) {
		return Objective{}, fmt.Errorf("%w: %s", ErrObjectiveNotFound, id)
	}
	if err != nil {
		return Objective{}, fmt.Errorf("get objective %s: %w", id, err)
	}
	var objective Objective
	if err := json.Unmarshal(record, &objective); err != nil {
		return Objective{}, fmt.Errorf("unmarshal objective %s: %w", id, err)
	}
	return objective, nil
}

func (s *PostgresObjectiveStore) Save(ctx context.Context, objective Objective) error {
	record, err := json.Marshal(objective)
	if err != nil {
		return fmt.Errorf("marshal objective %s: %w", objective.ID, err)
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE studio.objectives
		SET status = $2, revision = $3, record = $4, updated_at = $5
		WHERE id = $1`,
		objective.ID, string(objective.Status), objective.Revision, record, objective.UpdatedAt)
	if err != nil {
		return fmt.Errorf("save objective %s: %w", objective.ID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("%w: %s", ErrObjectiveNotFound, objective.ID)
	}
	return nil
}

// AppendReceipt satisfies ObjectiveStore. The store — not the caller — owns
// the sequence: any caller-supplied Sequence is replaced by the transactional
// per-objective assignment so the counter can never be forked by concurrent
// writers.
func (s *PostgresObjectiveStore) AppendReceipt(ctx context.Context, receipt Receipt) error {
	_, err := s.AppendReceiptAssign(ctx, receipt)
	return err
}

// AppendReceiptAssign implements the optional receiptSequencer capability: it
// appends the receipt with a server-assigned sequence and returns the stored
// receipt so callers observe the authoritative number.
func (s *PostgresObjectiveStore) AppendReceiptAssign(ctx context.Context, receipt Receipt) (Receipt, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Receipt{}, fmt.Errorf("append receipt for %s: begin: %w", receipt.ObjectiveID, err)
	}
	defer tx.Rollback() //nolint:errcheck // commit path returns first

	// FOR UPDATE both proves the objective exists and serializes appenders per
	// objective, making MAX(sequence)+1 race-free without a global lock.
	var exists int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM studio.objectives WHERE id = $1 FOR UPDATE`, receipt.ObjectiveID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, fmt.Errorf("%w: %s", ErrObjectiveNotFound, receipt.ObjectiveID)
	}
	if err != nil {
		return Receipt{}, fmt.Errorf("append receipt for %s: lock objective: %w", receipt.ObjectiveID, err)
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(sequence), 0) + 1 FROM studio.objective_receipts WHERE objective_id = $1`,
		receipt.ObjectiveID).Scan(&receipt.Sequence); err != nil {
		return Receipt{}, fmt.Errorf("append receipt for %s: next sequence: %w", receipt.ObjectiveID, err)
	}
	record, err := json.Marshal(receipt)
	if err != nil {
		return Receipt{}, fmt.Errorf("marshal receipt for %s: %w", receipt.ObjectiveID, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO studio.objective_receipts (objective_id, sequence, type, record, recorded_at)
		VALUES ($1, $2, $3, $4, $5)`,
		receipt.ObjectiveID, receipt.Sequence, string(receipt.Type), record, receipt.RecordedAt); err != nil {
		return Receipt{}, fmt.Errorf("append receipt for %s: insert: %w", receipt.ObjectiveID, err)
	}
	if err := tx.Commit(); err != nil {
		return Receipt{}, fmt.Errorf("append receipt for %s: commit: %w", receipt.ObjectiveID, err)
	}
	return receipt, nil
}

func (s *PostgresObjectiveStore) ListReceipts(ctx context.Context, id string) ([]Receipt, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM studio.objectives WHERE id = $1`, id).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrObjectiveNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("list receipts for %s: %w", id, err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT record FROM studio.objective_receipts WHERE objective_id = $1 ORDER BY sequence ASC`, id)
	if err != nil {
		return nil, fmt.Errorf("list receipts for %s: %w", id, err)
	}
	defer rows.Close()
	receipts := make([]Receipt, 0)
	for rows.Next() {
		var record []byte
		if err := rows.Scan(&record); err != nil {
			return nil, fmt.Errorf("list receipts for %s: scan: %w", id, err)
		}
		var receipt Receipt
		if err := json.Unmarshal(record, &receipt); err != nil {
			return nil, fmt.Errorf("list receipts for %s: unmarshal: %w", id, err)
		}
		receipts = append(receipts, receipt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list receipts for %s: %w", id, err)
	}
	return receipts, nil
}
