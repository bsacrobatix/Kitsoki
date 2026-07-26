package reviewedfeedback

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	appplatform "kitsoki/internal/application"
	"kitsoki/internal/clock"
)

const dispatchSchema = `
CREATE TABLE IF NOT EXISTS reviewed_feedback_dispatches (
  scope_id           TEXT NOT NULL,
  dispatch_id        TEXT NOT NULL,
  request_digest     TEXT NOT NULL,
  job_id             TEXT NOT NULL,
  status             TEXT NOT NULL,
  receipts_json      TEXT NOT NULL DEFAULT '[]',
  attempt            INTEGER NOT NULL DEFAULT 1,
  interrupted_reason TEXT NOT NULL DEFAULT '',
  created_at         INTEGER NOT NULL,
  updated_at         INTEGER NOT NULL,
  PRIMARY KEY(scope_id, dispatch_id)
) STRICT;

CREATE UNIQUE INDEX IF NOT EXISTS reviewed_feedback_dispatches_job
ON reviewed_feedback_dispatches(job_id);
`

type SQLiteDispatchStore struct {
	db    *sql.DB
	clock clock.Clock
}

func NewSQLiteDispatchStore(db *sql.DB, clk clock.Clock) (*SQLiteDispatchStore, error) {
	if db == nil {
		return nil, fmt.Errorf("reviewed feedback dispatch store requires a database")
	}
	if clk == nil {
		clk = clock.Real()
	}
	if _, err := db.Exec(dispatchSchema); err != nil {
		return nil, fmt.Errorf("initialize reviewed feedback dispatch store: %w", err)
	}
	return &SQLiteDispatchStore{db: db, clock: clk}, nil
}

func (s *SQLiteDispatchStore) Claim(
	ctx context.Context,
	request DispatchState,
) (DispatchState, error) {
	if request.ScopeID == "" || request.DispatchID == "" ||
		request.RequestDigest == "" || request.JobID == "" {
		return DispatchState{}, fmt.Errorf("claim reviewed feedback dispatch: incomplete identity")
	}
	now := s.clock.Now().UTC()
	inserted, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO reviewed_feedback_dispatches
		  (scope_id, dispatch_id, request_digest, job_id, status, receipts_json,
		   attempt, interrupted_reason, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, '[]', 1, '', ?, ?)`,
		request.ScopeID, request.DispatchID, request.RequestDigest, request.JobID,
		string(DispatchPending), now.UnixMilli(), now.UnixMilli(),
	)
	if err != nil {
		return DispatchState{}, fmt.Errorf("claim reviewed feedback dispatch: %w", err)
	}
	insertedRows, err := inserted.RowsAffected()
	if err != nil {
		return DispatchState{}, fmt.Errorf("claim reviewed feedback dispatch: %w", err)
	}
	state, err := s.get(ctx, request.ScopeID, request.DispatchID)
	if err != nil {
		return DispatchState{}, err
	}
	state.Claimed = insertedRows == 1
	if state.RequestDigest != request.RequestDigest {
		return state, nil
	}
	if state.Status == DispatchInterrupted {
		resumed, err := s.db.ExecContext(ctx, `
			UPDATE reviewed_feedback_dispatches
			SET status=?, attempt=attempt+1, updated_at=?
			WHERE scope_id=? AND dispatch_id=? AND request_digest=? AND status=?`,
			string(DispatchPending), now.UnixMilli(),
			request.ScopeID, request.DispatchID, request.RequestDigest,
			string(DispatchInterrupted),
		)
		if err != nil {
			return DispatchState{}, fmt.Errorf("resume reviewed feedback dispatch: %w", err)
		}
		resumedRows, err := resumed.RowsAffected()
		if err != nil {
			return DispatchState{}, fmt.Errorf("resume reviewed feedback dispatch: %w", err)
		}
		state, err = s.get(ctx, request.ScopeID, request.DispatchID)
		state.Claimed = resumedRows == 1
	}
	return state, err
}

func (s *SQLiteDispatchStore) Complete(
	ctx context.Context,
	scopeID, dispatchID, digest string,
	receipts []appplatform.Receipt,
) error {
	raw, err := json.Marshal(receipts)
	if err != nil {
		return fmt.Errorf("encode reviewed feedback dispatch receipts: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE reviewed_feedback_dispatches
		SET status=?, receipts_json=?, interrupted_reason='', updated_at=?
		WHERE scope_id=? AND dispatch_id=? AND request_digest=? AND status=?`,
		string(DispatchCompleted), string(raw), s.clock.Now().UTC().UnixMilli(),
		scopeID, dispatchID, digest, string(DispatchPending),
	)
	if err != nil {
		return fmt.Errorf("complete reviewed feedback dispatch: %w", err)
	}
	return requireDispatchUpdate(result)
}

func (s *SQLiteDispatchStore) Interrupt(
	ctx context.Context,
	scopeID, dispatchID, digest, reason string,
) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE reviewed_feedback_dispatches
		SET status=?, interrupted_reason=?, updated_at=?
		WHERE scope_id=? AND dispatch_id=? AND request_digest=? AND status=?`,
		string(DispatchInterrupted), boundedReason(reason),
		s.clock.Now().UTC().UnixMilli(), scopeID, dispatchID, digest,
		string(DispatchPending),
	)
	if err != nil {
		return fmt.Errorf("interrupt reviewed feedback dispatch: %w", err)
	}
	return requireDispatchUpdate(result)
}

func (s *SQLiteDispatchStore) InterruptPending(
	ctx context.Context,
	reason string,
) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE reviewed_feedback_dispatches
		SET status=?, interrupted_reason=?, updated_at=?
		WHERE status=?`,
		string(DispatchInterrupted), boundedReason(reason),
		s.clock.Now().UTC().UnixMilli(), string(DispatchPending),
	)
	if err != nil {
		return 0, fmt.Errorf("interrupt pending reviewed feedback dispatches: %w", err)
	}
	return result.RowsAffected()
}

func (s *SQLiteDispatchStore) get(
	ctx context.Context,
	scopeID, dispatchID string,
) (DispatchState, error) {
	var (
		state       DispatchState
		status      string
		receiptsRaw string
		createdMS   int64
		updatedMS   int64
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT scope_id, dispatch_id, request_digest, job_id, status,
		       receipts_json, attempt, interrupted_reason, created_at, updated_at
		FROM reviewed_feedback_dispatches
		WHERE scope_id=? AND dispatch_id=?`,
		scopeID, dispatchID,
	).Scan(
		&state.ScopeID, &state.DispatchID, &state.RequestDigest, &state.JobID,
		&status, &receiptsRaw, &state.Attempt, &state.InterruptedReason,
		&createdMS, &updatedMS,
	)
	if err != nil {
		return DispatchState{}, fmt.Errorf("read reviewed feedback dispatch: %w", err)
	}
	state.Status = DispatchStatus(status)
	state.CreatedAt = time.UnixMilli(createdMS).UTC()
	state.UpdatedAt = time.UnixMilli(updatedMS).UTC()
	if err := json.Unmarshal([]byte(receiptsRaw), &state.Receipts); err != nil {
		return DispatchState{}, fmt.Errorf("decode reviewed feedback dispatch receipts: %w", err)
	}
	return state, nil
}

func requireDispatchUpdate(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("reviewed feedback dispatch state changed concurrently")
	}
	return nil
}

func boundedReason(reason string) string {
	const max = 1024
	return boundedText(reason, max)
}
