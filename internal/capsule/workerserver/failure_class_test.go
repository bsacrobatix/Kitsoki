package workerserver

import (
	"context"
	"errors"
	"testing"
	"time"

	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
)

func newFailureClassTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(Config{
		Root: t.TempDir(),
		Runner: func(context.Context, string, executor.Prepared, string) (executor.Result, error) {
			return executor.Result{}, errors.New("unused in this test")
		},
		Environment: EnvironmentVerifierFunc(func(context.Context, string, environment.Lock) error { return nil }),
		Now:         func() time.Time { return time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestServerFailAssignsTheRequestedClass pins fail()'s contract: it stamps
// RunRecord.FailureClass with exactly the class its caller (each
// executeRegistered call site) passed, for every class this package assigns.
func TestServerFailAssignsTheRequestedClass(t *testing.T) {
	s := newFailureClassTestServer(t)
	prepared := executor.Prepared{ID: "exec-1", Envelope: executor.Envelope{Digest: "sha256:envelope"}}
	for _, class := range executor.KnownFailureClasses() {
		record := RunRecord{Schema: RunRecordSchema, ExecutionID: "exec-1", EnvelopeDigest: "sha256:envelope"}
		rec, _, ferr := s.fail(context.Background(), record, prepared, "some_stage", class, errors.New("boom"))
		if ferr == nil {
			t.Fatalf("class %s: expected the original error back", class)
		}
		if rec.Status != "failed" || rec.Stage != "some_stage" {
			t.Fatalf("class %s: record = %+v", class, rec)
		}
		if rec.FailureClass != class {
			t.Fatalf("FailureClass = %q, want %q", rec.FailureClass, class)
		}
	}
}

// TestServerFailNeverClassifiesACancellation ensures a cancelled run — not a
// fault — never carries a failure class, regardless of what the call site
// passed (defensive: a future call site cannot accidentally leak a class
// onto a cancellation just by not special-casing context.Canceled itself).
func TestServerFailNeverClassifiesACancellation(t *testing.T) {
	s := newFailureClassTestServer(t)
	prepared := executor.Prepared{ID: "exec-2", Envelope: executor.Envelope{Digest: "sha256:envelope"}}
	record := RunRecord{Schema: RunRecordSchema, ExecutionID: "exec-2", EnvelopeDigest: "sha256:envelope"}
	rec, _, _ := s.fail(context.Background(), record, prepared, "running_story", executor.FailureClassAgentAuth, context.Canceled)
	if rec.Status != "cancelled" {
		t.Fatalf("status = %q, want cancelled", rec.Status)
	}
	if rec.FailureClass != "" {
		t.Fatalf("a cancelled run must not carry a failure class, got %q", rec.FailureClass)
	}
}
