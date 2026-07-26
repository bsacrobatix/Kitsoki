package campaign

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS campaign_schedules (
  app_id TEXT NOT NULL,
  campaign_id TEXT NOT NULL,
  title TEXT NOT NULL,
  enabled INTEGER NOT NULL,
  paused INTEGER NOT NULL,
  cadence_nanos INTEGER NOT NULL,
  max_ticks_per_day INTEGER NOT NULL,
  max_concurrency INTEGER NOT NULL,
  action_story TEXT NOT NULL,
  action_intent TEXT NOT NULL,
  action_input_json TEXT NOT NULL,
  source_digest TEXT NOT NULL,
  definition_hash TEXT NOT NULL,
  next_due_at INTEGER NOT NULL,
  last_dispatch_at INTEGER,
  ticks_day TEXT NOT NULL DEFAULT '',
  ticks_today INTEGER NOT NULL DEFAULT 0,
  running INTEGER NOT NULL DEFAULT 0,
  last_job_ref TEXT NOT NULL DEFAULT '',
  last_status TEXT NOT NULL DEFAULT '',
  last_error TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (app_id, campaign_id)
);

CREATE INDEX IF NOT EXISTS campaign_schedules_due
  ON campaign_schedules(app_id, enabled, paused, next_due_at);

CREATE TABLE IF NOT EXISTS campaign_dispatches (
  idempotency_key TEXT PRIMARY KEY,
  app_id TEXT NOT NULL,
  campaign_id TEXT NOT NULL,
  due_at INTEGER NOT NULL,
  job_ref TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  started_at INTEGER NOT NULL,
  finished_at INTEGER
);

CREATE INDEX IF NOT EXISTS campaign_dispatches_app_started
  ON campaign_dispatches(app_id, started_at DESC);
`

// SQLiteStore stores schedule control state beside daemon sessions and
// artifact jobs.
type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(db *sql.DB) (*SQLiteStore, error) {
	if db == nil {
		return nil, fmt.Errorf("campaign.NewSQLiteStore: nil db")
	}
	if _, err := db.Exec(sqliteSchema); err != nil {
		return nil, fmt.Errorf("campaign.NewSQLiteStore: schema migration: %w", err)
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Reconcile(ctx context.Context, appID string, defs []Definition, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("campaign.Reconcile: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	seen := make(map[string]bool, len(defs))
	for _, def := range defs {
		if def.AppID != appID {
			return fmt.Errorf("campaign: definition %q belongs to app %q, not %q", def.ID, def.AppID, appID)
		}
		seen[def.ID] = true
		inputJSON, err := json.Marshal(def.Action.Input)
		if err != nil {
			return fmt.Errorf("campaign.Reconcile: encode %q action input: %w", def.ID, err)
		}
		nowMillis := timeMillis(now)
		_, err = tx.ExecContext(ctx, `
			INSERT INTO campaign_schedules (
			  app_id, campaign_id, title, enabled, paused, cadence_nanos,
			  max_ticks_per_day, max_concurrency, action_story, action_intent,
			  action_input_json, source_digest, definition_hash, next_due_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(app_id, campaign_id) DO UPDATE SET
			  title=excluded.title,
			  enabled=excluded.enabled,
			  paused=excluded.paused,
			  cadence_nanos=excluded.cadence_nanos,
			  max_ticks_per_day=excluded.max_ticks_per_day,
			  max_concurrency=excluded.max_concurrency,
			  action_story=excluded.action_story,
			  action_intent=excluded.action_intent,
			  action_input_json=excluded.action_input_json,
			  source_digest=excluded.source_digest,
			  next_due_at=CASE
			    WHEN campaign_schedules.definition_hash <> excluded.definition_hash
			      AND campaign_schedules.last_dispatch_at IS NOT NULL
			    THEN MAX(excluded.next_due_at, campaign_schedules.last_dispatch_at + excluded.cadence_nanos / 1000000)
			    ELSE campaign_schedules.next_due_at
			  END,
			  definition_hash=excluded.definition_hash,
			  updated_at=excluded.updated_at`,
			appID, def.ID, def.Title, boolInt(def.Enabled), boolInt(def.Paused), int64(def.Cadence),
			def.Budget.MaxTicksPerDay, def.Budget.MaxConcurrency, def.Action.Story, def.Action.Intent,
			string(inputJSON), def.SourceDigest, def.DefinitionHash, nowMillis, nowMillis,
		)
		if err != nil {
			return fmt.Errorf("campaign.Reconcile: upsert %q: %w", def.ID, err)
		}
	}

	rows, err := tx.QueryContext(ctx, `SELECT campaign_id FROM campaign_schedules WHERE app_id=?`, appID)
	if err != nil {
		return fmt.Errorf("campaign.Reconcile: list stale schedules: %w", err)
	}
	var stale []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return fmt.Errorf("campaign.Reconcile: scan stale schedule: %w", err)
		}
		if !seen[id] {
			stale = append(stale, id)
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("campaign.Reconcile: close stale rows: %w", err)
	}
	for _, id := range stale {
		if _, err := tx.ExecContext(ctx, `DELETE FROM campaign_schedules WHERE app_id=? AND campaign_id=?`, appID, id); err != nil {
			return fmt.Errorf("campaign.Reconcile: delete stale %q: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("campaign.Reconcile: commit: %w", err)
	}
	return nil
}

func (s *SQLiteStore) ClaimDue(ctx context.Context, appID string, now time.Time, limit int) ([]Claim, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("campaign: claim limit must be positive")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("campaign.ClaimDue: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
		SELECT campaign_id, title, enabled, paused, cadence_nanos,
		       max_ticks_per_day, max_concurrency, action_story, action_intent,
		       action_input_json, source_digest, definition_hash, next_due_at,
		       ticks_day, ticks_today, running
		FROM campaign_schedules
		WHERE app_id=? AND enabled=1 AND paused=0 AND next_due_at <= ?
		ORDER BY next_due_at, campaign_id
		LIMIT ?`, appID, timeMillis(now), limit)
	if err != nil {
		return nil, fmt.Errorf("campaign.ClaimDue: query: %w", err)
	}
	var candidates []claimCandidate
	for rows.Next() {
		var candidate claimCandidate
		candidate.def.AppID = appID
		if err := rows.Scan(
			&candidate.def.ID, &candidate.def.Title, &candidate.enabled, &candidate.paused,
			&candidate.cadenceNanos, &candidate.def.Budget.MaxTicksPerDay,
			&candidate.def.Budget.MaxConcurrency, &candidate.def.Action.Story,
			&candidate.def.Action.Intent, &candidate.actionInputJSON,
			&candidate.def.SourceDigest, &candidate.def.DefinitionHash,
			&candidate.nextDueMillis, &candidate.ticksDay, &candidate.ticksToday,
			&candidate.running,
		); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("campaign.ClaimDue: scan: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("campaign.ClaimDue: close rows: %w", err)
	}

	now = now.UTC()
	day := now.Format("2006-01-02")
	claims := make([]Claim, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.ticksDay != day {
			candidate.ticksDay = day
			candidate.ticksToday = 0
		}
		if candidate.ticksToday >= candidate.def.Budget.MaxTicksPerDay ||
			candidate.running >= candidate.def.Budget.MaxConcurrency {
			if _, err := tx.ExecContext(ctx, `
				UPDATE campaign_schedules SET ticks_day=?, ticks_today=?, updated_at=?
				WHERE app_id=? AND campaign_id=?`,
				candidate.ticksDay, candidate.ticksToday, timeMillis(now), appID, candidate.def.ID,
			); err != nil {
				return nil, fmt.Errorf("campaign.ClaimDue: persist budget state %q: %w", candidate.def.ID, err)
			}
			continue
		}
		if err := json.Unmarshal([]byte(candidate.actionInputJSON), &candidate.def.Action.Input); err != nil {
			return nil, fmt.Errorf("campaign.ClaimDue: decode %q action input: %w", candidate.def.ID, err)
		}
		candidate.def.Enabled = candidate.enabled != 0
		candidate.def.Paused = candidate.paused != 0
		candidate.def.Cadence = time.Duration(candidate.cadenceNanos)
		dueAt := millisTime(candidate.nextDueMillis)
		key := tickKey(appID, candidate.def.ID, dueAt)
		res, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO campaign_dispatches
			  (idempotency_key, app_id, campaign_id, due_at, status, started_at)
			VALUES (?, ?, ?, ?, 'running', ?)`,
			key, appID, candidate.def.ID, candidate.nextDueMillis, timeMillis(now),
		)
		if err != nil {
			return nil, fmt.Errorf("campaign.ClaimDue: reserve %q: %w", candidate.def.ID, err)
		}
		inserted, err := res.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("campaign.ClaimDue: reserve result %q: %w", candidate.def.ID, err)
		}
		nextDue := dueAt.Add(candidate.def.Cadence)
		if inserted == 0 {
			if _, err := tx.ExecContext(ctx, `
				UPDATE campaign_schedules SET next_due_at=?, updated_at=?
				WHERE app_id=? AND campaign_id=?`,
				timeMillis(nextDue), timeMillis(now), appID, candidate.def.ID,
			); err != nil {
				return nil, fmt.Errorf("campaign.ClaimDue: advance replayed %q: %w", candidate.def.ID, err)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE campaign_schedules
			SET ticks_day=?, ticks_today=?, running=running+1,
			    next_due_at=?, last_status='running', last_error='', updated_at=?
			WHERE app_id=? AND campaign_id=?`,
			day, candidate.ticksToday+1, timeMillis(nextDue), timeMillis(now), appID, candidate.def.ID,
		); err != nil {
			return nil, fmt.Errorf("campaign.ClaimDue: update %q: %w", candidate.def.ID, err)
		}
		claims = append(claims, Claim{
			Definition: candidate.def, IdempotencyKey: key, DueAt: dueAt,
		})
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("campaign.ClaimDue: commit: %w", err)
	}
	return claims, nil
}

func (s *SQLiteStore) Complete(ctx context.Context, claim Claim, jobRef string, dispatchErr error, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("campaign.Complete: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	status := "done"
	errorText := ""
	if dispatchErr != nil {
		status = "failed"
		errorText = dispatchErr.Error()
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE campaign_dispatches
		SET job_ref=?, status=?, error=?, finished_at=?
		WHERE idempotency_key=? AND status='running'`,
		jobRef, status, errorText, timeMillis(now), claim.IdempotencyKey,
	)
	if err != nil {
		return fmt.Errorf("campaign.Complete: update dispatch: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("campaign.Complete: dispatch result: %w", err)
	}
	if affected == 0 {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE campaign_schedules
		SET running=CASE WHEN running > 0 THEN running - 1 ELSE 0 END,
		    last_dispatch_at=?, last_job_ref=?, last_status=?, last_error=?, updated_at=?
		WHERE app_id=? AND campaign_id=?`,
		timeMillis(now), jobRef, status, errorText, timeMillis(now), claim.AppID, claim.ID,
	); err != nil {
		return fmt.Errorf("campaign.Complete: update schedule: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("campaign.Complete: commit: %w", err)
	}
	return nil
}

func (s *SQLiteStore) InterruptRunning(ctx context.Context, reason string, now time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("campaign.InterruptRunning: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
		UPDATE campaign_dispatches
		SET status='interrupted', error=?, finished_at=?
		WHERE status='running'`, reason, timeMillis(now))
	if err != nil {
		return 0, fmt.Errorf("campaign.InterruptRunning: update dispatches: %w", err)
	}
	count, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("campaign.InterruptRunning: result: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE campaign_schedules
		SET running=0,
		    last_status=CASE WHEN running > 0 THEN 'interrupted' ELSE last_status END,
		    last_error=CASE WHEN running > 0 THEN ? ELSE last_error END,
		    updated_at=CASE WHEN running > 0 THEN ? ELSE updated_at END
		WHERE running > 0`, reason, timeMillis(now)); err != nil {
		return 0, fmt.Errorf("campaign.InterruptRunning: update schedules: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("campaign.InterruptRunning: commit: %w", err)
	}
	return count, nil
}

func (s *SQLiteStore) List(ctx context.Context, appID string, limit int) ([]Schedule, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("campaign: list limit must be positive")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT campaign_id, title, enabled, paused, cadence_nanos,
		       max_ticks_per_day, max_concurrency, action_story, action_intent,
		       action_input_json, source_digest, definition_hash, next_due_at,
		       last_dispatch_at, ticks_day, ticks_today, running, last_job_ref,
		       last_status, last_error, updated_at
		FROM campaign_schedules
		WHERE app_id=?
		ORDER BY campaign_id
		LIMIT ?`, appID, limit)
	if err != nil {
		return nil, fmt.Errorf("campaign.List: query: %w", err)
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		var schedule Schedule
		var enabled, paused int
		var cadenceNanos, nextDueMillis, updatedMillis int64
		var lastDispatchMillis sql.NullInt64
		var actionInputJSON string
		schedule.AppID = appID
		if err := rows.Scan(
			&schedule.ID, &schedule.Title, &enabled, &paused, &cadenceNanos,
			&schedule.Budget.MaxTicksPerDay, &schedule.Budget.MaxConcurrency,
			&schedule.Action.Story, &schedule.Action.Intent, &actionInputJSON,
			&schedule.SourceDigest, &schedule.DefinitionHash, &nextDueMillis,
			&lastDispatchMillis, &schedule.TicksDay, &schedule.TicksToday,
			&schedule.Running, &schedule.LastJobRef, &schedule.LastStatus,
			&schedule.LastError, &updatedMillis,
		); err != nil {
			return nil, fmt.Errorf("campaign.List: scan: %w", err)
		}
		if err := json.Unmarshal([]byte(actionInputJSON), &schedule.Action.Input); err != nil {
			return nil, fmt.Errorf("campaign.List: decode %q action input: %w", schedule.ID, err)
		}
		schedule.Enabled = enabled != 0
		schedule.Paused = paused != 0
		schedule.Cadence = time.Duration(cadenceNanos)
		schedule.NextDueAt = millisTime(nextDueMillis)
		schedule.UpdatedAt = millisTime(updatedMillis)
		if lastDispatchMillis.Valid {
			value := millisTime(lastDispatchMillis.Int64)
			schedule.LastDispatchAt = &value
		}
		out = append(out, schedule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("campaign.List: rows: %w", err)
	}
	return out, nil
}

type claimCandidate struct {
	def             Definition
	enabled         int
	paused          int
	cadenceNanos    int64
	actionInputJSON string
	nextDueMillis   int64
	ticksDay        string
	ticksToday      int
	running         int
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func timeMillis(value time.Time) int64 { return value.UTC().UnixMilli() }
func millisTime(value int64) time.Time { return time.UnixMilli(value).UTC() }
