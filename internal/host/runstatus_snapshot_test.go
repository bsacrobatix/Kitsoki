package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"kitsoki/internal/app"
	"kitsoki/internal/artifactjob"
	"kitsoki/internal/effect"
	"kitsoki/internal/host/opschema"
)

func TestRunstatusSnapshotIsScopedPrivateBoundedAndDeterministic(t *testing.T) {
	store := artifactjob.NewMemoryStore()
	now := time.Date(2026, 7, 26, 3, 4, 5, 6, time.UTC)
	store.SetClock(func() time.Time { return now })
	registerSnapshotJob(t, store, artifactjob.RegisterRequest{
		ID: artifactjob.JobID("job-b"), SessionID: app.SessionID("session-a"),
		AppID: "review-app", Story: "/private/story.yaml",
		Status: artifactjob.StatusRunning, Origin: artifactjob.Origin{
			Kind: "secret-origin", Ref: "secret-ref", URL: "https://secret.invalid",
		},
		RunURL: "/secret/run", TracePath: "/secret/trace",
		Summary: "secret summary", Phase: "secret phase", Owner: "secret owner",
	})
	registerSnapshotJob(t, store, artifactjob.RegisterRequest{
		ID: artifactjob.JobID("job-a"), SessionID: app.SessionID("session-a"),
		AppID: "review-app", Story: "/private/other.yaml",
		Status:              artifactjob.StatusAwaitingInput,
		WorkspaceInstanceID: artifactjob.InstanceID("workspace-a"),
	})
	secretInterruption := "secret interruption"
	if _, err := store.Update(context.Background(), "job-a", artifactjob.Update{
		InterruptedReason: &secretInterruption,
	}); err != nil {
		t.Fatalf("update private interruption reason: %v", err)
	}
	registerSnapshotJob(t, store, artifactjob.RegisterRequest{
		ID: artifactjob.JobID("job-c"), SessionID: app.SessionID("session-b"),
		AppID: "other-app", Status: artifactjob.StatusFailed,
	})

	handler := NewRunstatusSnapshotHandler(store, "review-app")
	args := map[string]any{"max_jobs": 2, "max_bytes": 16 * 1024}
	first, err := handler(context.Background(), args)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	second, err := handler(context.Background(), args)
	if err != nil {
		t.Fatalf("snapshot second call: %v", err)
	}
	firstJSON, err := json.Marshal(first.Data)
	if err != nil {
		t.Fatalf("marshal first snapshot: %v", err)
	}
	secondJSON, err := json.Marshal(second.Data)
	if err != nil {
		t.Fatalf("marshal second snapshot: %v", err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("snapshot changed without a store change:\nfirst:  %s\nsecond: %s", firstJSON, secondJSON)
	}

	for _, secret := range []string{
		"/private/story.yaml", "/private/other.yaml", "secret-origin",
		"secret-ref", "secret.invalid", "/secret/run", "/secret/trace",
		"secret summary", "secret phase", "secret owner", "secret interruption",
		"other-app", "job-c",
	} {
		if strings.Contains(string(firstJSON), secret) {
			t.Fatalf("snapshot leaked %q: %s", secret, firstJSON)
		}
	}

	snapshot := first.Data["snapshot"].(map[string]any)
	if got := snapshot["schema"]; got != runstatusSnapshotSchema {
		t.Fatalf("schema = %v", got)
	}
	jobs := snapshot["jobs"].([]any)
	if len(jobs) != 2 ||
		jobs[0].(map[string]any)["job_ref"] != "job-a" ||
		jobs[1].(map[string]any)["job_ref"] != "job-b" {
		t.Fatalf("jobs = %#v", jobs)
	}
	sessions := snapshot["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("sessions = %#v", sessions)
	}
	session := sessions[0].(map[string]any)
	if session["session_ref"] != "session-a" ||
		session["job_count"] != 2 ||
		session["attention_count"] != 1 ||
		session["workspace_count"] != 1 {
		t.Fatalf("session summary = %#v", session)
	}
	attention := snapshot["attention"].(map[string]any)
	if attention["count"] != 1 {
		t.Fatalf("attention = %#v", attention)
	}
	ref := attention["refs"].([]any)[0].(map[string]any)
	if ref["kind"] != "operator_input_required" || ref["job_ref"] != "job-a" {
		t.Fatalf("attention ref = %#v", ref)
	}
}

func TestRunstatusSnapshotRefusesToTruncateRowsOrBytes(t *testing.T) {
	store := artifactjob.NewMemoryStore()
	store.SetClock(func() time.Time { return time.Unix(1, 0).UTC() })
	for _, id := range []string{"job-a", "job-b"} {
		registerSnapshotJob(t, store, artifactjob.RegisterRequest{
			ID: artifactjob.JobID(id), AppID: "app", Status: artifactjob.StatusRunning,
		})
	}
	handler := NewRunstatusSnapshotHandler(store, "app")

	_, err := handler(context.Background(), map[string]any{
		"max_jobs": 1, "max_bytes": 16 * 1024,
	})
	if err == nil || !strings.Contains(err.Error(), "refusing to truncate") {
		t.Fatalf("row overflow error = %v", err)
	}

	_, err = handler(context.Background(), map[string]any{
		"max_jobs": 2, "max_bytes": 32,
	})
	if err == nil || !strings.Contains(err.Error(), "refusing to truncate") {
		t.Fatalf("byte overflow error = %v", err)
	}
}

func TestRunstatusSnapshotRejectsInvalidBoundsAndUnknownStatus(t *testing.T) {
	store := artifactjob.NewMemoryStore()
	handler := NewRunstatusSnapshotHandler(store, "app")
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "missing jobs", args: map[string]any{"max_bytes": 1000}, want: "max_jobs"},
		{name: "fractional", args: map[string]any{"max_jobs": 1.5, "max_bytes": 1000}, want: "integer"},
		{name: "jobs ceiling", args: map[string]any{"max_jobs": 201, "max_bytes": 1000}, want: "between"},
		{name: "bytes ceiling", args: map[string]any{"max_jobs": 1, "max_bytes": 262145}, want: "between"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := handler(context.Background(), test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}

	registerSnapshotJob(t, store, artifactjob.RegisterRequest{
		ID: "job-unknown", AppID: "app", Status: artifactjob.Status("mystery"),
	})
	_, err := handler(context.Background(), map[string]any{
		"max_jobs": 1, "max_bytes": 1000,
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported status") {
		t.Fatalf("unknown status error = %v", err)
	}
}

func TestRunstatusSnapshotRegistrationSchemaAndEffect(t *testing.T) {
	registry := NewRegistry()
	RegisterBuiltins(registry)
	result, err := registry.Invoke(context.Background(), "host.runstatus.snapshot", map[string]any{
		"max_jobs": 1, "max_bytes": 1024,
	})
	if err != nil {
		t.Fatalf("invoke builtin: %v", err)
	}
	if !strings.Contains(result.Error, "unavailable outside daemon mode") {
		t.Fatalf("builtin error = %q", result.Error)
	}

	class, deterministic := ClassifyDispatchedCall(
		"host.runstatus",
		map[string]any{"op": "snapshot"},
	)
	if class != effect.Read || !deterministic {
		t.Fatalf("classification = (%q, %v)", class, deterministic)
	}
	class, deterministic = ClassifyDispatchedCall("host.runstatus.snapshot", nil)
	if class != effect.Read || !deterministic {
		t.Fatalf("leaf classification = (%q, %v)", class, deterministic)
	}
	spec, ok := opschema.Builtins().Lookup("host.runstatus", "snapshot")
	if !ok ||
		spec.Input["max_jobs"].Type != "int" ||
		spec.Input["max_bytes"].Type != "int" ||
		spec.Output["snapshot"].Type != "object" {
		t.Fatalf("opschema = %#v, %v", spec, ok)
	}
}

func registerSnapshotJob(t *testing.T, store *artifactjob.MemoryStore, req artifactjob.RegisterRequest) {
	t.Helper()
	if _, err := store.Register(context.Background(), req); err != nil {
		t.Fatalf("register %q: %v", req.ID, err)
	}
}
