package applicationjob

import (
	"context"
	"reflect"
	"testing"

	"kitsoki/internal/dbruntime/pgtest"
)

func TestPostgresStorePrivateMappingParity(t *testing.T) {
	db := pgtest.Open(t)
	jobStore, err := NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	record, err := jobStore.Register(context.Background(), Record{
		JobRef:              "aj_0123456789abcdef0123456789abcdef",
		CallerApplicationID: "caller", TemplateID: "publish",
		TargetApplicationID: "producer", TargetEvent: "producer.publish",
		ArtifactOutputs: []string{"report_ref"}, PrimaryOutput: "report_ref",
		MaxInputBytes: 1024, MaxRuntimeSeconds: 30, InputDigest: "sha256:input",
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err = jobStore.BindChild(context.Background(), record.JobRef, DispatchResult{
		RouteID: "route", SessionID: "session", ChildID: "child",
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err = jobStore.Complete(
		context.Background(),
		record.JobRef,
		[]string{"report:complete"},
		"report:complete",
		"", "", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := jobStore.Get(context.Background(), record.JobRef)
	if err != nil {
		t.Fatal(err)
	}
	if got.TargetRouteID != "route" || got.TargetSessionID != "session" ||
		got.ChildJobID != "child" ||
		!reflect.DeepEqual(got.Artifacts, []string{"report:complete"}) {
		t.Fatalf("Postgres mapping = %#v", got)
	}
}
