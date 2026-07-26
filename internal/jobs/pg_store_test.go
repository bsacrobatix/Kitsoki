package jobs_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"kitsoki/internal/app"
	"kitsoki/internal/dbruntime/pgtest"
	"kitsoki/internal/jobs"
)

// openPGJobStore returns a JobStore backed by a fresh per-test Postgres
// database (embedded server, or KITSOKI_PG_DSN passthrough). Skips when no
// server can be started in this environment.
func openPGJobStore(t *testing.T) (*jobs.JobStore, *sql.DB) {
	t.Helper()
	db := pgtest.Open(t)
	js, err := jobs.NewJobStore(db, jobs.WithDialect(jobs.DialectPostgres))
	if err != nil {
		t.Fatalf("NewJobStore (postgres): %v", err)
	}
	return js, db
}

func TestJobStorePostgres_JobCRUD(t *testing.T) {
	js, _ := openPGJobStore(t)
	ctx := context.Background()

	j := makeTestJob("01JPG0000000000000000001A", jobs.JobRunning)
	j.Payload = map[string]any{"target": "make test"}
	if err := js.UpsertJob(ctx, j); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}

	got, err := js.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != jobs.JobRunning || got.Kind != "host.test" || got.Payload["target"] != "make test" {
		t.Fatalf("GetJob = %+v", got)
	}
	if _, err := js.GetJob(ctx, "missing"); err != jobs.ErrJobNotFound {
		t.Fatalf("GetJob(missing) = %v, want ErrJobNotFound", err)
	}

	// Upsert over the same id replaces the row (conflict path).
	j.Status = jobs.JobAwaitingInput
	if err := js.UpsertJob(ctx, j); err != nil {
		t.Fatalf("UpsertJob (replace): %v", err)
	}
	got, err = js.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatalf("GetJob after replace: %v", err)
	}
	if got.Status != jobs.JobAwaitingInput {
		t.Fatalf("status after replace = %q", got.Status)
	}

	byStatus, err := js.ListJobsByStatus(ctx, "sess-test", jobs.JobAwaitingInput)
	if err != nil {
		t.Fatalf("ListJobsByStatus: %v", err)
	}
	if len(byStatus) != 1 || byStatus[0].ID != j.ID {
		t.Fatalf("ListJobsByStatus = %+v", byStatus)
	}

	bySession, err := js.ListBySession(ctx, "sess-test")
	if err != nil {
		t.Fatalf("ListBySession: %v", err)
	}
	if len(bySession) != 1 {
		t.Fatalf("ListBySession = %+v", bySession)
	}

	across, err := js.ListByStatus(ctx, []jobs.JobStatus{jobs.JobAwaitingInput, jobs.JobDone})
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	if len(across) != 1 || across[0].SessionID != "sess-test" {
		t.Fatalf("ListByStatus = %+v", across)
	}

	finished := time.Now()
	if err := js.UpdateJobStatus(ctx, j.ID, jobs.JobDone, "", map[string]any{"ok": true}, &finished); err != nil {
		t.Fatalf("UpdateJobStatus: %v", err)
	}
	got, err = js.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatalf("GetJob after status update: %v", err)
	}
	if got.Status != jobs.JobDone || got.FinishedAt == nil {
		t.Fatalf("terminal job = %+v", got)
	}
}

func TestJobStorePostgres_Clarification(t *testing.T) {
	js, _ := openPGJobStore(t)
	ctx := context.Background()

	j := makeTestJob("01JPG0000000000000000002A", jobs.JobRunning)
	if err := js.UpsertJob(ctx, j); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}

	schema := jobs.ClarificationSchema{Fields: map[string]string{"branch": "string"}, Prompt: "which branch?"}
	if err := js.RequestClarification(ctx, j.ID, schema); err != nil {
		t.Fatalf("RequestClarification: %v", err)
	}
	gotSchema, err := js.GetClarificationSchema(ctx, j.ID)
	if err != nil {
		t.Fatalf("GetClarificationSchema: %v", err)
	}
	if gotSchema == nil || gotSchema.Prompt != "which branch?" {
		t.Fatalf("schema = %+v", gotSchema)
	}

	if err := js.AnswerClarification(ctx, j.ID, map[string]any{"branch": "main"}); err != nil {
		t.Fatalf("AnswerClarification: %v", err)
	}
	raw, err := js.AnswerClarificationRaw(ctx, j.ID)
	if err != nil {
		t.Fatalf("AnswerClarificationRaw: %v", err)
	}
	if raw == "" {
		t.Fatal("expected stored clarification answer")
	}
	got, err := js.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != jobs.JobRunning {
		t.Fatalf("status after answer = %q, want running", got.Status)
	}
}

// TestJobStorePostgres_SweepStaleJobs pins the Postgres sweep policy: the
// database is shared across hosts, so owner_pid values are foreign PIDs and
// local liveness probing is meaningless. The sweep must touch NOTHING on this
// dialect — not even ownerless rows or rows whose recorded PID is dead on
// this host — until a host-independent lease/heartbeat exists.
func TestJobStorePostgres_SweepStaleJobs(t *testing.T) {
	js, db := openPGJobStore(t)
	ctx := context.Background()

	for _, j := range []*jobs.Job{
		makeTestJob("01JPG0000000000000000003R", jobs.JobRunning),
		makeTestJob("01JPG0000000000000000003W", jobs.JobAwaitingInput),
		makeTestJob("01JPG0000000000000000003D", jobs.JobDone),
		makeTestJob("01JPG0000000000000000003L", jobs.JobRunning),
	} {
		if err := js.UpsertJob(ctx, j); err != nil {
			t.Fatalf("UpsertJob: %v", err)
		}
	}
	// A row whose owner was never recorded: on SQLite this is an orphan; on
	// Postgres it could belong to any host and must be left alone.
	if _, err := db.Exec(`UPDATE jobs.jobs SET owner_pid = NULL WHERE id = '01JPG0000000000000000003L'`); err != nil {
		t.Fatalf("clear owner_pid: %v", err)
	}

	n, err := js.SweepStaleJobs(ctx)
	if err != nil {
		t.Fatalf("SweepStaleJobs: %v", err)
	}
	if n != 0 {
		t.Fatalf("swept %d rows on postgres dialect, want 0", n)
	}

	// Even with every PID reading as dead locally, nothing is swept: the PIDs
	// may be live on the host that recorded them.
	restore := jobs.SetSweepProcessAliveForTest(func(int) bool { return false })
	defer restore()
	n, err = js.SweepStaleJobs(ctx)
	if err != nil {
		t.Fatalf("SweepStaleJobs (dead local PIDs): %v", err)
	}
	if n != 0 {
		t.Fatalf("swept %d rows with dead local PIDs, want 0", n)
	}
	for id, want := range map[string]jobs.JobStatus{
		"01JPG0000000000000000003R": jobs.JobRunning,
		"01JPG0000000000000000003W": jobs.JobAwaitingInput,
		"01JPG0000000000000000003D": jobs.JobDone,
		"01JPG0000000000000000003L": jobs.JobRunning,
	} {
		got, err := js.GetJob(ctx, id)
		if err != nil {
			t.Fatalf("GetJob(%s): %v", id, err)
		}
		if got.Status != want {
			t.Errorf("%s: status = %q, want %q", id, got.Status, want)
		}
	}
}

func TestJobStorePostgres_ReconcileProcessBoundJobsDefersCrossHostOwners(t *testing.T) {
	js, _ := openPGJobStore(t)
	ctx := context.Background()
	job := makeTestJob("01JPG0000000000000000005R", jobs.JobRunning)
	job.SessionID = "app-session"
	if err := js.UpsertJob(ctx, job); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	result, err := js.ReconcileProcessBoundJobs(ctx, []app.SessionID{"app-session"}, 10)
	if err != nil {
		t.Fatalf("ReconcileProcessBoundJobs: %v", err)
	}
	if result.Examined != 1 || result.Interrupted != 0 || result.Deferred != 1 ||
		result.RestartTruth != "cross_host_lease_required" {
		t.Fatalf("result = %#v", result)
	}
	got, err := js.GetJob(ctx, job.ID)
	if err != nil || got.Status != jobs.JobRunning {
		t.Fatalf("job after deferred reconciliation = %#v, %v", got, err)
	}
}

func TestJobStorePostgres_Notifications(t *testing.T) {
	js, _ := openPGJobStore(t)
	ctx := context.Background()

	n := &jobs.Notification{
		SessionID:     "sess-test",
		Severity:      jobs.SeverityActionRequired,
		Title:         "job needs input",
		Body:          "answer the clarification",
		TeleportState: "terminal",
		TeleportSlots: map[string]any{"job": "01JPG0000000000000000004A"},
		TeleportJobID: "01JPG0000000000000000004A",
		OriginKind:    "job",
		OriginRef:     "job:01JPG0000000000000000004A",
	}
	if err := js.InsertNotification(ctx, n); err != nil {
		t.Fatalf("InsertNotification: %v", err)
	}
	if n.ID == "" {
		t.Fatal("InsertNotification did not assign an ID")
	}

	listed, err := js.ListNotifications(ctx, "sess-test", 10)
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	if len(listed) != 1 || listed[0].Title != "job needs input" || listed[0].TeleportSlots["job"] != "01JPG0000000000000000004A" {
		t.Fatalf("ListNotifications = %+v", listed)
	}

	counts, err := js.UnreadCount(ctx, "sess-test")
	if err != nil {
		t.Fatalf("UnreadCount: %v", err)
	}
	if counts[jobs.SeverityActionRequired] != 1 {
		t.Fatalf("UnreadCount = %+v", counts)
	}

	if err := js.MarkNotificationRead(ctx, n.ID); err != nil {
		t.Fatalf("MarkNotificationRead: %v", err)
	}
	counts, err = js.UnreadCount(ctx, "sess-test")
	if err != nil {
		t.Fatalf("UnreadCount after read: %v", err)
	}
	if counts[jobs.SeverityActionRequired] != 0 {
		t.Fatalf("UnreadCount after read = %+v", counts)
	}

	got, err := js.GetNotification(ctx, n.ID)
	if err != nil || got == nil || got.ReadAt == nil {
		t.Fatalf("GetNotification = %+v err %v", got, err)
	}

	// External dedup: same session + origin_kind + origin_ref inserts once.
	ext := &jobs.Notification{
		SessionID:     "sess-test",
		Severity:      jobs.SeverityInfo,
		Title:         "PR review requested",
		TeleportState: "terminal",
		OriginKind:    "external",
		OriginRef:     "github:pr/123",
	}
	inserted, err := js.InsertExternalNotificationOnce(ctx, ext)
	if err != nil || !inserted {
		t.Fatalf("InsertExternalNotificationOnce = %v inserted=%v", err, inserted)
	}
	dup := &jobs.Notification{
		SessionID:     "sess-test",
		Severity:      jobs.SeverityInfo,
		Title:         "PR review requested (again)",
		TeleportState: "terminal",
		OriginKind:    "external",
		OriginRef:     "github:pr/123",
	}
	inserted, err = js.InsertExternalNotificationOnce(ctx, dup)
	if err != nil || inserted {
		t.Fatalf("duplicate InsertExternalNotificationOnce = %v inserted=%v", err, inserted)
	}
	if dup.ID != ext.ID {
		t.Fatalf("duplicate correlated id = %q, want %q", dup.ID, ext.ID)
	}

	if err := js.DismissNotification(ctx, ext.ID); err != nil {
		t.Fatalf("DismissNotification: %v", err)
	}
	all, err := js.ListNotificationsAll(ctx, 0)
	if err != nil {
		t.Fatalf("ListNotificationsAll: %v", err)
	}
	if len(all) != 1 || all[0].ID != n.ID {
		t.Fatalf("ListNotificationsAll after dismiss = %+v", all)
	}
}
