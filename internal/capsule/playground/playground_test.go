package playground

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStartProducesPortalCompatibleLeasedRecord(t *testing.T) {
	project, workspace := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, ".kitsoki-capsule"), []byte("test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	r, err := Start(context.Background(), StartRequest{Project: project, ID: "virtual-pr-7", Workspace: workspace, SourceSHA: "abc1234", Ref: "feature/demo", Command: []string{"sh", "-c", "sleep 30"}, Testing: Testing{Instructions: []string{"Open the URL", "Exercise sign-in"}, ScenarioIDs: []string{"persona-admin"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer Stop(project, r.ID)
	if r.State != "ready" || r.URL == "" || r.SourceSHA != "abc1234" || r.Lease.IdleTimeoutSeconds != 720 || len(r.Testing.Instructions) != 2 || len(r.Testing.ScenarioIDs) != 1 {
		t.Fatalf("unexpected record: %#v", r)
	}
	if r.Port < 46000 || r.Port >= 47000 {
		t.Fatalf("uncontrolled port: %d", r.Port)
	}
	before := r.Lease.LastActivityAt
	time.Sleep(time.Millisecond)
	r, err = Touch(project, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Lease.LastActivityAt.After(before) {
		t.Fatal("touch did not refresh lease")
	}
}
func TestReapStopsExpiredPlayground(t *testing.T) {
	project, workspace := t.TempDir(), t.TempDir()
	_ = os.WriteFile(filepath.Join(workspace, ".kitsoki-capsule"), []byte("test"), 0644)
	r, err := Start(context.Background(), StartRequest{Project: project, ID: "idle", Workspace: workspace, SourceSHA: "abc1234", Command: []string{"sh", "-c", "sleep 30"}, IdleTimeout: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	r.Lease.LastActivityAt = time.Now().UTC().Add(-11 * time.Minute)
	if err := write(project, r); err != nil {
		t.Fatal(err)
	}
	r, err = Reap(project, r.ID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "stopped" {
		t.Fatalf("state=%q", r.State)
	}
}
