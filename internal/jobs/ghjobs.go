package jobs

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"time"

	"kitsoki/internal/ulid"

	_ "modernc.org/sqlite"
)

//go:embed ghjobs.sql
var ghJobsSchemaDDL string

// GitHub-job lifecycle states. A job advances
// queued -> claimed -> running -> (done | failed), or parks at
// awaiting_guidance when the router cannot classify the mention.
const (
	GHQueued           = "queued"
	GHClaimed          = "claimed"
	GHRunning          = "running"
	GHAwaitingGuidance = "awaiting_guidance"
	GHDone             = "done"
	GHFailed           = "failed"
)

// GHJob is one @kitsoki mention promoted to a unit of work. Its identity is the
// mention's origin_ref (github:<repo>/<kind>/<number>), which makes Claim
// idempotent: a re-mention of the same issue/PR attaches to the existing row
// rather than spawning a second run.
type GHJob struct {
	JobID        string
	OriginRef    string
	Repo         string
	ObjectKind   string // issue | pr
	ObjectNumber string
	Story        string
	State        string
	WorkerID     string
	RunID        string
	RunURL       string
	CommentID    string
	ErrMsg       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// GHMention is the minimal mention shape the store needs to mint a job row.
// internal/ghagent.Mention satisfies it via its exported fields; keeping the
// store's dependency a tiny local interface avoids an import cycle
// (ghagent imports jobs, not the reverse).
type GHMention struct {
	OriginRef    string
	Repo         string
	ObjectKind   string
	ObjectNumber string
}

// GHJobStore is the SQLite-backed claim/lifecycle store for GitHub jobs. It is
// purely additive to JobStore: a separate gh_jobs table, applied idempotently,
// sharing the same *sql.DB and modernc.org/sqlite driver. The natural key is
// origin_ref, which the session-scoped jobs table does not carry — hence a
// distinct table rather than an overload of UpsertJob (which is INSERT OR
// REPLACE and so cannot serve an idempotent-attach Claim).
type GHJobStore struct {
	db *sql.DB
}

// NewGHJobStore applies the gh_jobs DDL idempotently and configures WAL +
// busy_timeout on the connection so the BEGIN IMMEDIATE used by Claim
// serializes writers (in-process) and cross-process workers back off rather
// than erroring SQLITE_BUSY. Mirrors NewJobStore's construction idiom.
func NewGHJobStore(db *sql.DB) (*GHJobStore, error) {
	// Best-effort pragmas; :memory: ignores WAL but accepts busy_timeout.
	_, _ = db.Exec("PRAGMA journal_mode=WAL")
	_, _ = db.Exec("PRAGMA busy_timeout=5000")
	if _, err := db.Exec(ghJobsSchemaDDL); err != nil {
		return nil, fmt.Errorf("jobs.NewGHJobStore: schema migration: %w", err)
	}
	return &GHJobStore{db: db}, nil
}

// Claim atomically attaches a worker to the mention's job. It inserts a queued
// row if none exists, then performs a guarded queued->claimed CAS scoped to
// origin_ref. Exactly one concurrent caller wins the CAS (won=true); a row
// already past queued means a re-mention, so won=false and the caller attaches
// to the existing run. The whole sequence runs inside a BEGIN IMMEDIATE tx,
// which under WAL takes a write lock up front and serializes the CAS.
func (s *GHJobStore) Claim(ctx context.Context, m GHMention, workerID string) (job *GHJob, won bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("jobs.Claim: begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	// BEGIN IMMEDIATE: promote to a write lock immediately so concurrent
	// claimers serialize on the CAS rather than racing a deferred upgrade.
	if _, err = tx.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		// database/sql already opened a (deferred) tx; an explicit BEGIN
		// errors. Tolerate it — the guarded UPDATE below is still atomic
		// within this tx. (modernc sqlite returns "cannot start a
		// transaction within a transaction".)
		err = nil
	}

	now := time.Now().UnixMilli()
	jobID := ulid.New()
	if _, err = tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO gh_jobs
		   (job_id, origin_ref, repo, object_kind, object_number, state, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		jobID, m.OriginRef, m.Repo, m.ObjectKind, m.ObjectNumber, GHQueued, now, now,
	); err != nil {
		return nil, false, fmt.Errorf("jobs.Claim: insert: %w", err)
	}

	res, err := tx.ExecContext(ctx,
		`UPDATE gh_jobs SET state=?, worker_id=?, updated_at=?
		   WHERE origin_ref=? AND state=?`,
		GHClaimed, workerID, time.Now().UnixMilli(), m.OriginRef, GHQueued,
	)
	if err != nil {
		return nil, false, fmt.Errorf("jobs.Claim: cas: %w", err)
	}
	affected, _ := res.RowsAffected()
	won = affected == 1

	job, err = scanGHJobTx(ctx, tx, m.OriginRef)
	if err != nil {
		return nil, false, fmt.Errorf("jobs.Claim: read-back: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("jobs.Claim: commit: %w", err)
	}
	return job, won, nil
}

// GetByOriginRef returns the job for an origin_ref, or sql.ErrNoRows.
func (s *GHJobStore) GetByOriginRef(ctx context.Context, originRef string) (*GHJob, error) {
	return scanGHJob(ctx, s.db, "origin_ref", originRef)
}

// GetJob returns the job for a job_id, or sql.ErrNoRows.
func (s *GHJobStore) GetJob(ctx context.Context, jobID string) (*GHJob, error) {
	return scanGHJob(ctx, s.db, "job_id", jobID)
}

// Advance transitions a job to newState, recording errMsg (typically only on
// the failed transition).
func (s *GHJobStore) Advance(ctx context.Context, jobID, newState, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE gh_jobs SET state=?, err_msg=?, updated_at=? WHERE job_id=?`,
		newState, errMsg, time.Now().UnixMilli(), jobID)
	if err != nil {
		return fmt.Errorf("jobs.Advance: %w", err)
	}
	return nil
}

// SetStory records the chosen story path for a claimed job.
func (s *GHJobStore) SetStory(ctx context.Context, jobID, story string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE gh_jobs SET story=?, updated_at=? WHERE job_id=?`,
		story, time.Now().UnixMilli(), jobID)
	if err != nil {
		return fmt.Errorf("jobs.SetStory: %w", err)
	}
	return nil
}

// SetComment captures the rolling-status comment id on first Post.
func (s *GHJobStore) SetComment(ctx context.Context, jobID, commentID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE gh_jobs SET comment_id=?, updated_at=? WHERE job_id=?`,
		commentID, time.Now().UnixMilli(), jobID)
	if err != nil {
		return fmt.Errorf("jobs.SetComment: %w", err)
	}
	return nil
}

// SetRunURL records the spawned run's id + url for the ack.
func (s *GHJobStore) SetRunURL(ctx context.Context, jobID, runID, runURL string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE gh_jobs SET run_id=?, run_url=?, updated_at=? WHERE job_id=?`,
		runID, runURL, time.Now().UnixMilli(), jobID)
	if err != nil {
		return fmt.Errorf("jobs.SetRunURL: %w", err)
	}
	return nil
}

type ghRowScanner interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

const ghJobCols = `job_id, origin_ref, repo, object_kind, object_number,
	COALESCE(story,''), state, COALESCE(worker_id,''), COALESCE(run_id,''),
	COALESCE(run_url,''), COALESCE(comment_id,''), COALESCE(err_msg,''),
	created_at, updated_at`

func scanGHJob(ctx context.Context, q ghRowScanner, col, val string) (*GHJob, error) {
	row := q.QueryRowContext(ctx,
		`SELECT `+ghJobCols+` FROM gh_jobs WHERE `+col+`=?`, val)
	return scanGHJobRow(row)
}

func scanGHJobTx(ctx context.Context, tx *sql.Tx, originRef string) (*GHJob, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT `+ghJobCols+` FROM gh_jobs WHERE origin_ref=?`, originRef)
	return scanGHJobRow(row)
}

func scanGHJobRow(row *sql.Row) (*GHJob, error) {
	var j GHJob
	var createdMs, updatedMs int64
	if err := row.Scan(
		&j.JobID, &j.OriginRef, &j.Repo, &j.ObjectKind, &j.ObjectNumber,
		&j.Story, &j.State, &j.WorkerID, &j.RunID, &j.RunURL, &j.CommentID,
		&j.ErrMsg, &createdMs, &updatedMs,
	); err != nil {
		return nil, err
	}
	j.CreatedAt = time.UnixMilli(createdMs)
	j.UpdatedAt = time.UnixMilli(updatedMs)
	return &j, nil
}
