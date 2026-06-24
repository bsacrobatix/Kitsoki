package jobs

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func newTestGHStore(t *testing.T) *GHJobStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := NewGHJobStore(db)
	if err != nil {
		t.Fatalf("NewGHJobStore: %v", err)
	}
	return s
}

// TestClaim_WinThenAttach proves Claim is idempotent on origin_ref: the first
// mention wins a fresh queued->claimed CAS, a re-mention attaches to the SAME
// row (won=false), and exactly one row exists.
func TestClaim_WinThenAttach(t *testing.T) {
	ctx := context.Background()
	s := newTestGHStore(t)
	m := GHMention{OriginRef: "github:o/r/issue/42", Repo: "o/r", ObjectKind: "issue", ObjectNumber: "42"}

	job1, won1, err := s.Claim(ctx, m, "w1")
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if !won1 {
		t.Fatalf("first claim should win")
	}
	if job1.State != GHClaimed {
		t.Errorf("state = %q, want claimed", job1.State)
	}
	if job1.WorkerID != "w1" {
		t.Errorf("worker = %q, want w1", job1.WorkerID)
	}

	job2, won2, err := s.Claim(ctx, m, "w2")
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if won2 {
		t.Errorf("re-mention should NOT win the claim")
	}
	if job2.JobID != job1.JobID {
		t.Errorf("re-mention minted a new job: %q vs %q", job2.JobID, job1.JobID)
	}
	if job2.WorkerID != "w1" {
		t.Errorf("re-mention stole the claim: worker = %q, want w1", job2.WorkerID)
	}

	// Exactly one row.
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gh_jobs`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("gh_jobs row count = %d, want 1", count)
	}
}

func TestAdvanceAndSetters(t *testing.T) {
	ctx := context.Background()
	s := newTestGHStore(t)
	m := GHMention{OriginRef: "github:o/r/issue/7", Repo: "o/r", ObjectKind: "issue", ObjectNumber: "7"}
	job, _, err := s.Claim(ctx, m, "w1")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := s.SetStory(ctx, job.JobID, "stories/bugfix"); err != nil {
		t.Fatal(err)
	}
	if err := s.Advance(ctx, job.JobID, GHRunning, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetComment(ctx, job.JobID, "c-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRunURL(ctx, job.JobID, "run-1", "kitsoki://run/run-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Advance(ctx, job.JobID, GHDone, ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetJob(ctx, job.JobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Story != "stories/bugfix" || got.State != GHDone || got.CommentID != "c-1" || got.RunURL != "kitsoki://run/run-1" {
		t.Errorf("unexpected job after lifecycle: %+v", got)
	}
}
