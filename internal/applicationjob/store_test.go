package applicationjob

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"kitsoki/internal/store"
)

func TestSQLiteStorePersistsPrivateMappingAndArtifacts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sessions.db")
	sessionStore, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	jobStore, err := NewSQLiteStore(sessionStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 26, 1, 2, 3, 0, time.UTC)
	jobStore.SetClock(func() time.Time { return now })
	record, err := jobStore.Register(ctx, Record{
		JobRef:              "aj_0123456789abcdef0123456789abcdef",
		CallerApplicationID: "caller", TemplateID: "publish",
		TargetApplicationID: "producer", TargetEvent: "producer.publish",
		ArtifactOutputs: []string{"report_ref", "bundle_ref"}, PrimaryOutput: "report_ref",
		MaxInputBytes: 4096, MaxRuntimeSeconds: 60, InputDigest: "sha256:input",
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.TargetRouteID != "" || !record.CreatedAt.Equal(now) {
		t.Fatalf("reserved record = %#v", record)
	}
	record, err = jobStore.BindChild(ctx, record.JobRef, DispatchResult{
		RouteID: "private-route", SessionID: "private-session", ChildID: "private-child",
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err = jobStore.Complete(
		ctx,
		record.JobRef,
		[]string{"report:one", "bundle:two"},
		"report:one",
		"", "", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	if record.TargetRouteID != "private-route" || record.TargetSessionID != "private-session" ||
		record.ChildJobID != "private-child" ||
		!reflect.DeepEqual(record.Artifacts, []string{"report:one", "bundle:two"}) ||
		record.PrimaryHandle != "report:one" {
		t.Fatalf("completed record = %#v", record)
	}
	if err := sessionStore.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedJobs, err := NewSQLiteStore(reopened.DB())
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopenedJobs.Get(ctx, record.JobRef)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.ArtifactOutputs, record.ArtifactOutputs) ||
		!reflect.DeepEqual(got.Artifacts, record.Artifacts) ||
		got.TargetApplicationID != "producer" || got.TargetEvent != "producer.publish" {
		t.Fatalf("reopened record = %#v", got)
	}
}

func TestSQLiteStoreMissingMapping(t *testing.T) {
	sessionStore, err := store.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sessionStore.Close()
	jobStore, err := NewSQLiteStore(sessionStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jobStore.Get(context.Background(), "aj_missing"); err != ErrNotFound {
		t.Fatalf("Get missing = %v, want ErrNotFound", err)
	}
}
