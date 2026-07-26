package reviewedfeedback

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"kitsoki/internal/clock"
)

const reconcileSchema = `
CREATE TABLE IF NOT EXISTS feedback_reconciliations (
  operation          TEXT NOT NULL,
  app_id             TEXT NOT NULL,
  item_id            TEXT NOT NULL,
  item_digest        TEXT NOT NULL,
  status             TEXT NOT NULL,
  result_json        TEXT NOT NULL DEFAULT '{}',
  attempt            INTEGER NOT NULL DEFAULT 1,
  interrupted_reason TEXT NOT NULL DEFAULT '',
  created_at         INTEGER NOT NULL,
  updated_at         INTEGER NOT NULL,
  PRIMARY KEY(operation, app_id, item_id)
) STRICT;

CREATE UNIQUE INDEX IF NOT EXISTS feedback_reconciliations_pending
ON feedback_reconciliations(operation, app_id) WHERE status = 'pending';
`

const postgresReconcileSchema = `
CREATE SCHEMA IF NOT EXISTS reviewedfeedback;

CREATE TABLE IF NOT EXISTS reviewedfeedback.feedback_reconciliations (
  operation          TEXT NOT NULL,
  app_id             TEXT NOT NULL,
  item_id            TEXT NOT NULL,
  item_digest        TEXT NOT NULL,
  status             TEXT NOT NULL,
  result_json        TEXT NOT NULL DEFAULT '{}',
  attempt            INTEGER NOT NULL DEFAULT 1,
  interrupted_reason TEXT NOT NULL DEFAULT '',
  created_at         BIGINT NOT NULL,
  updated_at         BIGINT NOT NULL,
  PRIMARY KEY(operation, app_id, item_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS feedback_reconciliations_pending
ON reviewedfeedback.feedback_reconciliations(operation, app_id)
WHERE status = 'pending';
`

type SQLiteReconcileStore struct {
	db      *sql.DB
	clock   clock.Clock
	dialect sqlDialect
}

func NewSQLiteReconcileStore(db *sql.DB, clk clock.Clock) (*SQLiteReconcileStore, error) {
	if db == nil {
		return nil, fmt.Errorf("feedback reconcile store requires a database")
	}
	if clk == nil {
		clk = clock.Real()
	}
	if _, err := db.Exec(reconcileSchema); err != nil {
		return nil, fmt.Errorf("initialize feedback reconcile store: %w", err)
	}
	return &SQLiteReconcileStore{db: db, clock: clk}, nil
}

func NewPostgresReconcileStore(db *sql.DB, clk clock.Clock) (*SQLiteReconcileStore, error) {
	if db == nil {
		return nil, fmt.Errorf("feedback reconcile store requires a database")
	}
	if clk == nil {
		clk = clock.Real()
	}
	if _, err := db.Exec(postgresReconcileSchema); err != nil {
		return nil, fmt.Errorf("initialize feedback Postgres reconcile store: %w", err)
	}
	return &SQLiteReconcileStore{
		db: db, clock: clk, dialect: sqlDialectPostgres,
	}, nil
}

func (s *SQLiteReconcileStore) q(query string) string {
	return feedbackSQL(s.dialect, query)
}

func (s *SQLiteReconcileStore) ClaimNext(
	ctx context.Context,
	operation, appID string,
	items []ReconcileItem,
) (ReconcileClaim, error) {
	if operation == "" || appID == "" {
		return ReconcileClaim{}, fmt.Errorf("claim feedback reconciliation: incomplete scope")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReconcileClaim{}, fmt.Errorf("claim feedback reconciliation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var replay ReconcileClaim
	now := s.clock.Now().UTC().UnixMilli()
	for _, item := range items {
		if item.ID == "" || item.Digest == "" {
			return ReconcileClaim{}, fmt.Errorf("claim feedback reconciliation: incomplete item identity")
		}
		state, found, err := readReconcileClaim(
			ctx, tx, s.q, operation, appID, item.ID,
		)
		if err != nil {
			return ReconcileClaim{}, err
		}
		if found {
			if state.Digest != item.Digest {
				return ReconcileClaim{}, fmt.Errorf("feedback reconciliation item identity was reused with different content")
			}
			switch state.Status {
			case ReconcileCompleted:
				if replay.ItemID == "" {
					state.Replayed = true
					replay = state
				}
				continue
			case ReconcilePending:
				if err := tx.Commit(); err != nil {
					return ReconcileClaim{}, fmt.Errorf("claim feedback reconciliation: %w", err)
				}
				return state, nil
			case ReconcileInterrupted:
				result, err := tx.ExecContext(ctx, s.q(`
					UPDATE feedback_reconciliations
					SET status=?, attempt=attempt+1, interrupted_reason='', updated_at=?
					WHERE operation=? AND app_id=? AND item_id=? AND item_digest=? AND status=?`),
					string(ReconcilePending), now, operation, appID, item.ID, item.Digest,
					string(ReconcileInterrupted),
				)
				if err != nil {
					return ReconcileClaim{}, fmt.Errorf("resume feedback reconciliation: %w", err)
				}
				affected, _ := result.RowsAffected()
				if affected == 1 {
					state.Status = ReconcilePending
					state.Attempt++
					state.Claimed = true
					if err := tx.Commit(); err != nil {
						return ReconcileClaim{}, fmt.Errorf("claim feedback reconciliation: %w", err)
					}
					return state, nil
				}
			default:
				return ReconcileClaim{}, fmt.Errorf("feedback reconciliation has invalid durable status")
			}
		}
		result, err := tx.ExecContext(ctx, s.claimInsertSQL(),
			operation, appID, item.ID, item.Digest, string(ReconcilePending), now, now,
		)
		if err != nil {
			return ReconcileClaim{}, fmt.Errorf("claim feedback reconciliation: %w", err)
		}
		affected, _ := result.RowsAffected()
		if affected == 1 {
			claim := ReconcileClaim{
				Operation: operation, AppID: appID, ItemID: item.ID, Digest: item.Digest,
				Status: ReconcilePending, Claimed: true, Attempt: 1,
			}
			if err := tx.Commit(); err != nil {
				return ReconcileClaim{}, fmt.Errorf("claim feedback reconciliation: %w", err)
			}
			return claim, nil
		}
		// Another process may own the operation/app partial-unique pending slot.
		pending, ok, err := readPendingReconcileClaim(
			ctx, tx, s.q, operation, appID,
		)
		if err != nil {
			return ReconcileClaim{}, err
		}
		if ok {
			if err := tx.Commit(); err != nil {
				return ReconcileClaim{}, fmt.Errorf("claim feedback reconciliation: %w", err)
			}
			return pending, nil
		}
	}
	if err := tx.Commit(); err != nil {
		return ReconcileClaim{}, fmt.Errorf("claim feedback reconciliation: %w", err)
	}
	return replay, nil
}

func (s *SQLiteReconcileStore) Complete(
	ctx context.Context,
	claim ReconcileClaim,
	result ReconcileResult,
) error {
	if err := validateReconcileResult(claim, result); err != nil {
		return err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode feedback reconciliation result: %w", err)
	}
	updated, err := s.db.ExecContext(ctx, s.q(`
		UPDATE feedback_reconciliations
		SET status=?, result_json=?, interrupted_reason='', updated_at=?
		WHERE operation=? AND app_id=? AND item_id=? AND item_digest=? AND status=?`),
		string(ReconcileCompleted), string(raw), s.clock.Now().UTC().UnixMilli(),
		claim.Operation, claim.AppID, claim.ItemID, claim.Digest, string(ReconcilePending),
	)
	if err != nil {
		return fmt.Errorf("complete feedback reconciliation: %w", err)
	}
	return requireReconcileUpdate(updated)
}

func (s *SQLiteReconcileStore) Interrupt(
	ctx context.Context,
	claim ReconcileClaim,
	reason string,
) error {
	updated, err := s.db.ExecContext(ctx, s.q(`
		UPDATE feedback_reconciliations
		SET status=?, interrupted_reason=?, updated_at=?
		WHERE operation=? AND app_id=? AND item_id=? AND item_digest=? AND status=?`),
		string(ReconcileInterrupted), boundedReason(reason), s.clock.Now().UTC().UnixMilli(),
		claim.Operation, claim.AppID, claim.ItemID, claim.Digest, string(ReconcilePending),
	)
	if err != nil {
		return fmt.Errorf("interrupt feedback reconciliation: %w", err)
	}
	return requireReconcileUpdate(updated)
}

func (s *SQLiteReconcileStore) InterruptPending(ctx context.Context, reason string) (int64, error) {
	updated, err := s.db.ExecContext(ctx, s.q(`
		UPDATE feedback_reconciliations
		SET status=?, interrupted_reason=?, updated_at=?
		WHERE status=?`),
		string(ReconcileInterrupted), boundedReason(reason),
		s.clock.Now().UTC().UnixMilli(), string(ReconcilePending),
	)
	if err != nil {
		return 0, fmt.Errorf("interrupt pending feedback reconciliations: %w", err)
	}
	return updated.RowsAffected()
}

type reconcileQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readReconcileClaim(
	ctx context.Context,
	q reconcileQuery,
	query func(string) string,
	operation, appID, itemID string,
) (ReconcileClaim, bool, error) {
	var state ReconcileClaim
	var status, resultJSON string
	err := q.QueryRowContext(ctx, query(`
		SELECT operation, app_id, item_id, item_digest, status, result_json, attempt
		FROM feedback_reconciliations
		WHERE operation=? AND app_id=? AND item_id=?`),
		operation, appID, itemID,
	).Scan(
		&state.Operation, &state.AppID, &state.ItemID, &state.Digest,
		&status, &resultJSON, &state.Attempt,
	)
	if err == sql.ErrNoRows {
		return ReconcileClaim{}, false, nil
	}
	if err != nil {
		return ReconcileClaim{}, false, fmt.Errorf("read feedback reconciliation: %w", err)
	}
	state.Status = ReconcileStatus(status)
	if status == string(ReconcileCompleted) {
		if err := json.Unmarshal([]byte(resultJSON), &state.Result); err != nil {
			return ReconcileClaim{}, false, fmt.Errorf("decode feedback reconciliation result: %w", err)
		}
	}
	return state, true, nil
}

func readPendingReconcileClaim(
	ctx context.Context,
	q reconcileQuery,
	query func(string) string,
	operation, appID string,
) (ReconcileClaim, bool, error) {
	var itemID string
	err := q.QueryRowContext(ctx, query(`
		SELECT item_id FROM feedback_reconciliations
		WHERE operation=? AND app_id=? AND status=?`),
		operation, appID, string(ReconcilePending),
	).Scan(&itemID)
	if err == sql.ErrNoRows {
		return ReconcileClaim{}, false, nil
	}
	if err != nil {
		return ReconcileClaim{}, false, fmt.Errorf("read pending feedback reconciliation: %w", err)
	}
	return readReconcileClaim(ctx, q, query, operation, appID, itemID)
}

func (s *SQLiteReconcileStore) claimInsertSQL() string {
	if s.dialect == sqlDialectPostgres {
		return s.q(`
			INSERT INTO feedback_reconciliations
			  (operation, app_id, item_id, item_digest, status, result_json,
			   attempt, interrupted_reason, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, '{}', 1, '', ?, ?)
			ON CONFLICT DO NOTHING`)
	}
	return `
		INSERT OR IGNORE INTO feedback_reconciliations
		  (operation, app_id, item_id, item_digest, status, result_json,
		   attempt, interrupted_reason, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, '{}', 1, '', ?, ?)`
}

func requireReconcileUpdate(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("feedback reconciliation state changed concurrently")
	}
	return nil
}
