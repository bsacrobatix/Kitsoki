package hygiene

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"kitsoki/internal/capsule/control"
)

func TestOwnerReconcilePlansAndMarksOnlyProvenOrphanedActiveRecordFailed(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 7, 27, 8, 0, 0, 0, time.UTC)
	workspace := writeManagedWorkspace(t, root, "orphan", control.StateReady, now.Add(-96*time.Hour), false)
	if err := os.WriteFile(filepath.Join(workspace, workspaceSentinel), []byte("orphan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".kitsoki-owner"), []byte("owner-orphan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := OwnerReconcileOptions{ProjectRoot: root, MinAge: 72 * time.Hour, Now: func() time.Time { return now }, ReadWorkspaceActivity: inactiveOwnerReconcileActivity, ReadWorkspaceCommands: inactiveOwnerReconcileActivity}
	plan, err := BuildOwnerReconcilePlan(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Schema != OwnerReconcileSchema || !plan.DryRun || len(plan.Candidates) != 1 {
		t.Fatalf("plan=%#v", plan)
	}
	if !plan.Candidates[0].Eligible || plan.Candidates[0].Action != "mark_failed_cleanup_eligible" {
		t.Fatalf("candidate=%#v", plan.Candidates[0])
	}
	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("dry-run removed workspace: %v", err)
	}
	result, err := ApplyOwnerReconcilePlan(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Reconciled) != 1 {
		t.Fatalf("result=%#v", result)
	}
	in, err := (control.FileInstanceStore{Root: filepath.Join(root, ".capsules", "workspaces")}).Get(context.Background(), "orphan")
	if err != nil || in.State != control.StateFailed {
		t.Fatalf("instance=%#v err=%v", in, err)
	}
	if _, err := os.Stat(workspace); err != nil {
		t.Fatalf("reconcile removed workspace: %v", err)
	}
}

func TestOwnerReconcileFailsClosedForOwnerOrProcessEvidence(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 7, 27, 8, 0, 0, 0, time.UTC)
	workspace := writeManagedWorkspace(t, root, "live", control.StateMaterializing, now.Add(-96*time.Hour), false)
	if err := os.WriteFile(filepath.Join(workspace, workspaceSentinel), []byte("live\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".kitsoki-owner"), []byte("owner-live\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A matching owner marker does not outrank a live command probe.
	plan, err := BuildOwnerReconcilePlan(context.Background(), OwnerReconcileOptions{ProjectRoot: root, MinAge: 72 * time.Hour, Now: func() time.Time { return now }, ReadWorkspaceActivity: inactiveOwnerReconcileActivity, ReadWorkspaceCommands: func(_ context.Context, paths []string) (WorkspaceActivity, error) {
		return WorkspaceActivity{Known: true, PIDsByPath: map[string][]int{paths[0]: {73}}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 || plan.Candidates[0].Eligible {
		t.Fatalf("plan=%#v", plan)
	}
	if plan.Candidates[0].Reason != "workspace is live in process-command probe: [73]" {
		t.Fatalf("candidate=%#v", plan.Candidates[0])
	}
}

func TestOwnerReconcileRejectsUnexpiredLease(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC().Add(time.Second)
	workspace := writeManagedWorkspace(t, root, "leased", control.StateReady, now.Add(-96*time.Hour), false)
	if err := os.WriteFile(filepath.Join(workspace, workspaceSentinel), []byte("leased\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".kitsoki-owner"), []byte("owner-leased\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := control.FileInstanceStore{Root: filepath.Join(root, ".capsules", "workspaces")}
	in, err := store.Get(context.Background(), "leased")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompareAndSwap(context.Background(), in.ID, in.Generation, func(cur *control.Instance) error { cur.Lease.ExpiresAt = now.Add(time.Hour); return nil }); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildOwnerReconcilePlan(context.Background(), OwnerReconcileOptions{ProjectRoot: root, MinAge: time.Nanosecond, Now: func() time.Time { return now }, ReadWorkspaceActivity: inactiveOwnerReconcileActivity, ReadWorkspaceCommands: inactiveOwnerReconcileActivity})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 || plan.Candidates[0].Eligible || plan.Candidates[0].Reason != "workspace lease is still unexpired" {
		t.Fatalf("plan=%#v", plan)
	}
}

func inactiveOwnerReconcileActivity(context.Context, []string) (WorkspaceActivity, error) {
	return WorkspaceActivity{Known: true, PIDsByPath: map[string][]int{}}, nil
}
