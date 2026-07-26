package applicationjob

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/store"
)

type fakeBackend struct {
	dispatches int
	cancels    int
	dispatched DispatchRequest
	snapshot   ChildSnapshot
	found      bool
}

func (f *fakeBackend) Dispatch(_ context.Context, request DispatchRequest) (DispatchResult, error) {
	f.dispatches++
	f.dispatched = request
	return DispatchResult{RouteID: "private-route", SessionID: "private-session", ChildID: "private-child"}, nil
}

func (f *fakeBackend) Status(_ context.Context, _ Record) (ChildSnapshot, bool, error) {
	return f.snapshot, f.found, nil
}

func (f *fakeBackend) Cancel(_ context.Context, _ Record) error {
	f.cancels++
	return nil
}

func TestServiceSubmitReplayStatusAndDurableArtifacts(t *testing.T) {
	service, backend, jobs, closeStore := newTestService(t)
	defer closeStore()
	ctx := context.Background()
	first, err := service.Submit(
		ctx,
		"caller",
		"publish",
		json.RawMessage(`{"node_id":"n1","options":{"b":2,"a":1}}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^aj_[0-9a-f]{32}$`).MatchString(first.JobRef) ||
		first.Status != "running" || first.Receipt.Schema != ReceiptSchema ||
		!regexp.MustCompile(`^ajr_[0-9a-f]{32}$`).MatchString(first.Receipt.ID) {
		t.Fatalf("submit = %#v", first)
	}
	internal, err := jobs.Get(ctx, artifactjob.JobID(first.JobRef))
	if err != nil {
		t.Fatal(err)
	}
	if internal.AppID != "caller" || internal.Story != "" || internal.SessionID != "" ||
		internal.RunURL != "" || internal.Origin.Kind != "application-job" {
		t.Fatalf("public artifactjob leaked private routing = %#v", internal)
	}
	replay, err := service.Submit(
		ctx,
		"caller",
		"publish",
		json.RawMessage(`{"options":{"a":1,"b":2},"node_id":"n1"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if replay.JobRef != first.JobRef || !replay.Receipt.Replayed || backend.dispatches != 1 {
		t.Fatalf("replay = %#v dispatches=%d", replay, backend.dispatches)
	}

	backend.found = true
	backend.snapshot = ChildSnapshot{
		Status: "done",
		Output: map[string]any{"report_ref": "report:complete", "ignored": "path:/tmp/private"},
	}
	done, err := service.Status(ctx, "caller", first.JobRef)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "done" || done.Primary != "report:complete" ||
		len(done.Artifacts) != 1 || done.Artifacts[0] != "report:complete" {
		t.Fatalf("done = %#v", done)
	}
	raw, _ := json.Marshal(done)
	if strings.Contains(string(raw), "private-route") || strings.Contains(string(raw), "private-session") ||
		strings.Contains(string(raw), "private-child") || strings.Contains(string(raw), "/tmp/private") {
		t.Fatalf("public result leaked private state: %s", raw)
	}
	backend.found = false
	reopened, err := service.Status(ctx, "caller", first.JobRef)
	if err != nil || reopened.Primary != "report:complete" {
		t.Fatalf("durable terminal status = %#v err=%v", reopened, err)
	}
}

func TestServiceRestartInterruptsAndReplaysWithoutRedispatch(t *testing.T) {
	service, backend, jobs, closeStore := newTestService(t)
	defer closeStore()
	first, err := service.Submit(
		context.Background(),
		"caller",
		"publish",
		json.RawMessage(`{"node_id":"restart"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.SweepInterrupted(context.Background(), "daemon_restarted"); err != nil {
		t.Fatal(err)
	}
	replay, err := service.Submit(
		context.Background(),
		"caller",
		"publish",
		json.RawMessage(`{"node_id":"restart"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if replay.JobRef != first.JobRef || replay.Status != "interrupted" ||
		replay.Reason != "daemon_restarted" || !replay.Receipt.Replayed ||
		backend.dispatches != 1 {
		t.Fatalf("restart replay = %#v dispatches=%d", replay, backend.dispatches)
	}
}

func TestServiceRuntimeBoundCancelsExistingChild(t *testing.T) {
	service, backend, jobs, closeStore := newTestService(t)
	defer closeStore()
	var deadline func()
	service.ScheduleAfter = func(_ time.Duration, run func()) { deadline = run }
	submitted, err := service.Submit(
		context.Background(),
		"caller",
		"publish",
		json.RawMessage(`{"node_id":"bounded"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	deadline()
	job, err := jobs.Get(context.Background(), artifactjob.JobID(submitted.JobRef))
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != artifactjob.StatusFailed ||
		job.InterruptedReason != "runtime_bound_exceeded" || backend.cancels != 1 {
		t.Fatalf("bounded job = %#v cancels=%d", job, backend.cancels)
	}
}

func TestServiceRejectsAuthorityKeysAtEveryDepth(t *testing.T) {
	keys := []string{
		"session_id", "child_job_id", "artifact_path", "callback_url",
		"shell_command", "model_provider", "request_actor", "target_transport",
		"application_id", "event", "callbackURL", "targetTransport", "storyPath",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			service, backend, _, closeStore := newTestService(t)
			defer closeStore()
			raw, _ := json.Marshal(map[string]any{
				"node": map[string]any{"nested": []any{map[string]any{key: "private"}}},
			})
			_, err := service.Submit(context.Background(), "caller", "publish", raw)
			if err == nil || !strings.Contains(err.Error(), "reserved key") {
				t.Fatalf("Submit error = %v", err)
			}
			if backend.dispatches != 0 {
				t.Fatal("reserved input reached the dispatcher")
			}
		})
	}
}

func TestServiceRejectsNonOpaqueConfiguredOutput(t *testing.T) {
	service, backend, _, closeStore := newTestService(t)
	defer closeStore()
	submitted, err := service.Submit(
		context.Background(),
		"caller",
		"publish",
		json.RawMessage(`{"node_id":"bad-output"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	backend.found = true
	backend.snapshot = ChildSnapshot{
		Status: "done", Output: map[string]any{"report_ref": "file:///tmp/report"},
	}
	status, err := service.Status(context.Background(), "caller", submitted.JobRef)
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "failed" || status.Reason != "invalid_artifact_output" {
		t.Fatalf("status = %#v", status)
	}
}

func newTestService(
	t *testing.T,
) (*Service, *fakeBackend, *artifactjob.MemoryStore, func()) {
	t.Helper()
	sessionStore, err := store.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	records, err := NewSQLiteStore(sessionStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 26, 2, 0, 0, 0, time.UTC)
	records.SetClock(func() time.Time { return now })
	jobs := artifactjob.NewMemoryStore()
	jobs.SetClock(func() time.Time { return now })
	backend := &fakeBackend{found: true, snapshot: ChildSnapshot{Status: "running"}}
	service := &Service{
		Records: records, Jobs: jobs, Backend: backend,
		Templates: map[string]map[string]Template{
			"caller": {
				"publish": {
					ApplicationID: "producer", Event: "producer.publish",
					ArtifactOutputs: []string{"report_ref"}, PrimaryOutput: "report_ref",
					Bounds: Bounds{MaxInputBytes: 4096, MaxRuntimeSeconds: 60},
				},
			},
		},
		Now:           func() time.Time { return now },
		ScheduleAfter: func(time.Duration, func()) {},
	}
	return service, backend, jobs, func() { _ = sessionStore.Close() }
}
