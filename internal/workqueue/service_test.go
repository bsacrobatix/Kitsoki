package workqueue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestServiceUsesConfiguredPolicyAndRedactsJobs(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	ids := 0
	store, err := NewSQLiteStore(db, WithClock(func() time.Time { return now }), WithIDGenerator(func() string {
		ids++
		return string(rune('a' + ids))
	}))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(store, "pog-ops", map[string]QueueConfig{
		"triage": {MaxInputBytes: 64, MaxAttempts: 4, RequiredCapabilities: []string{"linux", "linux", "go"}, Priority: 7},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.Enqueue(ctx, "pog-ops", "triage", "feedback-1", json.RawMessage(` { "b": 2, "a": 1 } `))
	if err != nil {
		t.Fatal(err)
	}
	if first.Schema != SubmissionReceiptSchema || first.Ref == "" || first.Replayed || first.InputHash == "" {
		t.Fatalf("first receipt = %#v", first)
	}
	replay, err := svc.Enqueue(ctx, "pog-ops", "triage", "feedback-1", json.RawMessage(`{"a":1,"b":2}`))
	if err != nil || !replay.Replayed || replay.Ref != first.Ref {
		t.Fatalf("replay = %#v, %v", replay, err)
	}
	job, err := store.Get(ctx, first.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if string(job.Payload) != `{"a":1,"b":2}` || job.Priority != 7 || job.MaxAttempts != 4 || len(job.RequiredCapabilities) != 2 {
		t.Fatalf("stored job = %#v", job)
	}
	job.Receipt = &Receipt{ApplicationID: "pog-ops", WorkerID: "private-worker", Outcome: "done"}
	projection := project(job)
	if projection.Receipt.ApplicationID != "" || projection.Receipt.WorkerID != "" {
		t.Fatalf("private fields leaked: %#v", projection)
	}
	if _, err := svc.Get(ctx, "other-app", first.Ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-app get = %v", err)
	}
	if _, err := svc.Enqueue(ctx, "pog-ops", "missing", "k", json.RawMessage(`{}`)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown queue = %v", err)
	}
	if _, err := svc.Enqueue(ctx, "pog-ops", "triage", "too-big", json.RawMessage(`{"value":"this is deliberately much longer than the sixty four byte configured input limit"}`)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize input = %v", err)
	}
}

func TestServiceSnapshotAggregatesAndFailsClosedAtBounds(t *testing.T) {
	ctx := context.Background()
	db, _ := sql.Open("sqlite", ":memory:")
	defer db.Close()
	next := 0
	store, err := NewSQLiteStore(db, WithIDGenerator(func() string { next++; return string(rune('a' + next)) }))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(store, "app", map[string]QueueConfig{"a": {}, "b": {}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Enqueue(ctx, "app", "a", "a", json.RawMessage(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Enqueue(ctx, "app", "b", "b", json.RawMessage(`{"b":2}`)); err != nil {
		t.Fatal(err)
	}
	all, err := svc.Snapshot(ctx, "app", 2, DefaultMaxSnapshotBytes)
	if err != nil || len(all) != 2 || all[0].Queue != "a" || all[1].Queue != "b" {
		t.Fatalf("snapshot = %#v, %v", all, err)
	}
	if _, err := svc.Snapshot(ctx, "app", 1, DefaultMaxSnapshotBytes); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("item limit = %v", err)
	}
	if _, err := svc.Snapshot(ctx, "app", 2, 10); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("byte limit = %v", err)
	}
	if _, err := svc.Snapshot(ctx, "other", 2, DefaultMaxSnapshotBytes); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-app snapshot = %v", err)
	}
}
