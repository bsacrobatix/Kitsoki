package artifactjob

import (
	"context"
	"errors"
	"testing"
	"time"

	"kitsoki/internal/dbruntime/pgtest"
)

// openPostgresStore returns a store backed by a fresh per-test Postgres
// database (embedded server, or KITSOKI_PG_DSN passthrough). Skips when no
// server can be started in this environment.
func openPostgresStore(t *testing.T) *SQLStore {
	t.Helper()
	db := pgtest.Open(t)
	store, err := NewPostgresStore(db)
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	return store
}

func TestPostgresStoreRegisterBindListArchive(t *testing.T) {
	ctx := context.Background()
	store := openPostgresStore(t)
	now := time.Date(2026, 7, 8, 1, 2, 3, 0, time.UTC)
	store.SetClock(func() time.Time { return now })

	job, err := store.Register(ctx, RegisterRequest{
		ID:        "job-1",
		AppID:     "dev-story",
		Story:     "stories/dev-story",
		Origin:    Origin{Kind: "dev-story", Ref: "design:artifact-driven-stories", URL: "https://example.test/origin"},
		Summary:   "draft design",
		Phase:     "design_brief",
		SessionID: "session-a",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if job.Status != StatusRunning || job.Visibility != VisibilityLocal {
		t.Fatalf("defaults = status %q visibility %q", job.Status, job.Visibility)
	}
	if !job.CreatedAt.Equal(now) || !job.UpdatedAt.Equal(now) {
		t.Fatalf("timestamps = created %v updated %v, want %v", job.CreatedAt, job.UpdatedAt, now)
	}

	got, err := store.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Origin != job.Origin || got.Summary != "draft design" {
		t.Fatalf("Get round-trip = %+v", got)
	}

	job, err = store.BindRun(ctx, job.ID, "session-b", RunURL("http://127.0.0.1:7331", job.ID), "/tmp/trace.jsonl")
	if err != nil {
		t.Fatalf("BindRun: %v", err)
	}
	if job.SessionID != "session-b" || job.RunURL != "http://127.0.0.1:7331/run/job-1" || job.TracePath == "" {
		t.Fatalf("bound job = %+v", job)
	}

	// Attach rebinds only the session — the external-attach path used when a
	// live session picks up an existing job record.
	job, err = store.Attach(ctx, job.ID, "session-c")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if job.SessionID != "session-c" {
		t.Fatalf("attached session = %q", job.SessionID)
	}
	if _, err := store.Attach(ctx, "missing", "session-x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Attach missing = %v, want ErrNotFound", err)
	}

	rows, err := store.List(ctx, ListFilter{AppID: "dev-story"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != job.ID {
		t.Fatalf("List rows = %+v", rows)
	}
	rows, err = store.List(ctx, ListFilter{SessionID: "session-c", Status: []Status{StatusRunning, StatusAwaitingInput}})
	if err != nil {
		t.Fatalf("List by session+status: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("List by session+status rows = %+v", rows)
	}

	// Terminal update with finished_at, then archive and filter visibility.
	done := StatusDone
	finished := now.Add(time.Minute)
	job, err = store.Update(ctx, job.ID, Update{Status: &done, FinishedAt: &finished})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if job.Status != StatusDone || job.FinishedAt == nil || !job.FinishedAt.Equal(finished) {
		t.Fatalf("updated job = %+v", job)
	}

	if _, err := store.Archive(ctx, job.ID); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	rows, err = store.List(ctx, ListFilter{AppID: "dev-story"})
	if err != nil {
		t.Fatalf("List after archive: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("archived job still listed: %+v", rows)
	}
	rows, err = store.List(ctx, ListFilter{AppID: "dev-story", IncludeArchived: true})
	if err != nil {
		t.Fatalf("List include-archived: %v", err)
	}
	if len(rows) != 1 || rows[0].Status != StatusArchived {
		t.Fatalf("include-archived rows = %+v", rows)
	}
}

func TestPostgresStoreSweepInterrupted(t *testing.T) {
	ctx := context.Background()
	store := openPostgresStore(t)

	seed := []RegisterRequest{
		{ID: "run-1", Status: StatusRunning},
		{ID: "wait-1", Status: StatusAwaitingInput},
		{ID: "done-1", Status: StatusDone},
	}
	for _, req := range seed {
		if _, err := store.Register(ctx, req); err != nil {
			t.Fatalf("Register %s: %v", req.ID, err)
		}
	}

	n, err := store.SweepInterrupted(ctx, "daemon restart")
	if err != nil {
		t.Fatalf("SweepInterrupted: %v", err)
	}
	if n != 2 {
		t.Fatalf("swept %d rows, want 2", n)
	}
	for _, id := range []JobID{"run-1", "wait-1"} {
		j, err := store.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		if j.Status != StatusInterrupted || j.InterruptedReason != "daemon restart" {
			t.Fatalf("%s = status %q reason %q", id, j.Status, j.InterruptedReason)
		}
	}
	if j, err := store.Get(ctx, "done-1"); err != nil || j.Status != StatusDone {
		t.Fatalf("done-1 = %+v err %v", j, err)
	}
}

func TestPostgresRunIndex(t *testing.T) {
	ctx := context.Background()
	store := openPostgresStore(t)

	job, err := store.Register(ctx, RegisterRequest{ID: "job-run", Story: "stories/demo", SessionID: "session-a"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	started := time.Date(2026, 7, 8, 2, 0, 0, 0, time.UTC)
	run := Run{JobID: job.ID, SessionID: "session-a", Story: "stories/demo", Status: StatusRunning, StartedAt: started, TracePath: "/tmp/trace.jsonl"}
	if err := store.UpsertRun(ctx, run); err != nil {
		t.Fatalf("UpsertRun: %v", err)
	}
	// Second upsert overwrites the same row (conflict path).
	ended := started.Add(time.Minute)
	run.Status = StatusDone
	run.EndedAt = &ended
	run.LastTurn = 4
	if err := store.UpsertRun(ctx, run); err != nil {
		t.Fatalf("UpsertRun (update): %v", err)
	}

	got, err := store.GetRun(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != StatusDone || got.LastTurn != 4 || got.EndedAt == nil || !got.EndedAt.Equal(ended) {
		t.Fatalf("GetRun = %+v", got)
	}

	runs, err := store.ListRuns(ctx, ListFilter{SessionID: "session-a", Status: []Status{StatusDone}})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].JobID != job.ID {
		t.Fatalf("ListRuns = %+v", runs)
	}

	a := Artifact{JobID: job.ID, Handle: "deck", Kind: "slidey", MIME: "application/json", Label: "Deck", Path: "/tmp/deck.slidey.json", SizeBytes: 42, CreatedAt: started}
	if err := store.UpsertArtifact(ctx, a); err != nil {
		t.Fatalf("UpsertArtifact: %v", err)
	}
	a.SizeBytes = 99
	if err := store.UpsertArtifact(ctx, a); err != nil {
		t.Fatalf("UpsertArtifact (update): %v", err)
	}

	all, err := store.Artifacts(ctx, job.ID)
	if err != nil {
		t.Fatalf("Artifacts: %v", err)
	}
	if len(all) != 1 || all[0].SizeBytes != 99 {
		t.Fatalf("Artifacts = %+v", all)
	}

	resolved, err := store.ResolveArtifact(ctx, job.ID, "deck")
	if err != nil || resolved.Path != "/tmp/deck.slidey.json" {
		t.Fatalf("ResolveArtifact = %+v err %v", resolved, err)
	}
	if _, err := store.ResolveArtifact(ctx, job.ID, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ResolveArtifact missing = %v, want ErrNotFound", err)
	}
}
