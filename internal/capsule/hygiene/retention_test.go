package hygiene

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPurgeClosedWorkspaceRequiresReceiptAndProviderGuard(t *testing.T) {
	root := t.TempDir()
	initLegacyProject(t, root)
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	workspace := writeLegacyWorkspace(t, root, "closed-shipped-20260725", now.Add(-48*time.Hour), false, false)
	writePurgeProviderStub(t, root)
	head := strings.TrimSpace(runHygieneCommand(t, workspace, "git", "rev-parse", "HEAD"))
	recoveryRef := "refs/kitsoki/workspace-teardown-recovery/" + head
	runHygieneGit(t, root, "update-ref", recoveryRef, head)
	receipt := validRetentionReceipt(root, filepath.Base(workspace), head, recoveryRef, now)
	receipt.Project = filepath.Join(root, "releases", "retired")
	receipt.ProjectStateRoot = filepath.Join(root, ".capsules")

	var providerCalls int
	purgingRel := filepath.ToSlash(filepath.Join(".capsules", "workspaces", "closed-purging-shipped-20260725"))
	result, err := PurgeClosedWorkspace(context.Background(), PurgeOptions{
		ProjectRoot:           root,
		Receipt:               receipt,
		KeepWorkspaces:        -1,
		MinAge:                24 * time.Hour,
		CurrentPath:           root,
		Now:                   func() time.Time { return now },
		ReadWorkspaceActivity: inactiveRetentionActivity,
		ReadDiskUsage:         stableRetentionDiskUsage,
		CloseWorkspace: func(_ context.Context, project string, candidate Candidate) error {
			providerCalls++
			if project != resultProjectRoot(t, root) || candidate.Path != purgingRel || candidate.Head != head || !candidate.Legacy {
				t.Fatalf("provider received unbound candidate: project=%s candidate=%+v", project, candidate)
			}
			return os.RemoveAll(filepath.Join(project, filepath.FromSlash(candidate.Path)))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.AlreadyAbsent || result.WorkspaceID != receipt.WorkspaceID || result.Head != head || providerCalls != 1 {
		t.Fatalf("result=%+v provider_calls=%d", result, providerCalls)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("workspace remains after provider purge: %v", err)
	}

	result, err = PurgeClosedWorkspace(context.Background(), PurgeOptions{
		ProjectRoot:   root,
		Receipt:       receipt,
		MinAge:        24 * time.Hour,
		Now:           func() time.Time { return now },
		ReadDiskUsage: stableRetentionDiskUsage,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.AlreadyAbsent || providerCalls != 1 {
		t.Fatalf("idempotent repeat = %+v provider_calls=%d", result, providerCalls)
	}
}

func TestPurgeClosedWorkspaceFailsClosedOnRetentionGuards(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		dirty      bool
		unmerged   bool
		pinned     bool
		active     bool
		tooYoung   bool
		maxBytes   int64
		mutateHead bool
		removeRef  bool
		want       string
	}{
		{name: "dirty", dirty: true, want: "uncommitted changes"},
		{name: "non-contained", unmerged: true, want: "not contained"},
		{name: "pinned", pinned: true, want: "pinned"},
		{name: "active process", active: true, want: "in use by process"},
		{name: "too young", tooYoung: true, want: "too young"},
		{name: "byte limit", maxBytes: 1, want: "exceeds per-command limit"},
		{name: "changed tip", mutateHead: true, want: "not contained"},
		{name: "missing recovery ref", removeRef: true, want: "recovery ref is missing or changed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			initLegacyProject(t, root)
			workspace := writeLegacyWorkspace(t, root, "closed-case-"+strings.ReplaceAll(tt.name, " ", "-"), now.Add(-48*time.Hour), tt.dirty, tt.unmerged)
			writePurgeProviderStub(t, root)
			head := strings.TrimSpace(runHygieneCommand(t, workspace, "git", "rev-parse", "HEAD"))
			recoveryRef := "refs/kitsoki/workspace-teardown-recovery/" + head
			if !tt.unmerged {
				runHygieneGit(t, root, "update-ref", recoveryRef, head)
			}
			receipt := validRetentionReceipt(root, filepath.Base(workspace), head, recoveryRef, now)
			if tt.tooYoung {
				receipt.IssuedAt = now.Add(-time.Hour)
				receipt.EligibleAfter = receipt.IssuedAt.Add(24 * time.Hour)
				receipt.ProcessSnapshot.CapturedAt = receipt.IssuedAt.Add(-time.Minute)
				receipt.ActivityProbe.CapturedAt = receipt.IssuedAt.Add(-2 * time.Minute)
			}
			if tt.mutateHead {
				if err := os.WriteFile(filepath.Join(workspace, "changed.txt"), []byte("changed\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				runHygieneGit(t, workspace, "add", "changed.txt")
				runHygieneGit(t, workspace, "commit", "--signoff", "-m", "changed after receipt")
			}
			if tt.removeRef {
				runHygieneGit(t, root, "update-ref", "-d", recoveryRef)
			}
			activity := inactiveRetentionActivity
			if tt.active {
				activity = func(context.Context, []string) (WorkspaceActivity, error) {
					return WorkspaceActivity{Known: true, PIDsByPath: map[string][]int{workspace: {4242}}}, nil
				}
			}
			pinned := []string(nil)
			if tt.pinned {
				pinned = []string{filepath.Base(workspace)}
			}
			providerCalled := false
			result, err := PurgeClosedWorkspace(context.Background(), PurgeOptions{
				ProjectRoot:           root,
				Receipt:               receipt,
				KeepWorkspaces:        -1,
				MinAge:                24 * time.Hour,
				MaxBytes:              tt.maxBytes,
				CurrentPath:           root,
				PinnedWorkspaceIDs:    pinned,
				Now:                   func() time.Time { return now },
				ReadWorkspaceActivity: activity,
				ReadDiskUsage:         stableRetentionDiskUsage,
				CloseWorkspace: func(context.Context, string, Candidate) error {
					providerCalled = true
					return nil
				},
			})
			if tt.tooYoung {
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("err=%v, want %q", err, tt.want)
				}
			} else {
				if err != nil || result.Status != "skipped" || !strings.Contains(result.Reason, tt.want) || result.Candidate == nil {
					t.Fatalf("result=%+v err=%v, want typed skip containing %q", result, err, tt.want)
				}
			}
			if providerCalled {
				t.Fatal("unsafe receipt reached destructive provider")
			}
			if _, statErr := os.Stat(workspace); statErr != nil {
				t.Fatalf("unsafe workspace was removed: %v", statErr)
			}
		})
	}
}

func TestPurgeClosedWorkspaceIsArchiveFreeAndResumesInterruptedIsolation(t *testing.T) {
	root := t.TempDir()
	initLegacyProject(t, root)
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	workspace := writeLegacyWorkspace(t, root, "closed-interrupted", now.Add(-48*time.Hour), false, false)
	writePurgeProviderStub(t, root)
	head := strings.TrimSpace(runHygieneCommand(t, workspace, "git", "rev-parse", "HEAD"))
	recoveryRef := "refs/kitsoki/workspace-teardown-recovery/" + head
	runHygieneGit(t, root, "update-ref", recoveryRef, head)
	receipt := validRetentionReceipt(root, filepath.Base(workspace), head, recoveryRef, now)

	_, err := PurgeClosedWorkspace(context.Background(), PurgeOptions{
		ProjectRoot:           root,
		Receipt:               receipt,
		KeepWorkspaces:        -1,
		MinAge:                24 * time.Hour,
		CurrentPath:           root,
		Now:                   func() time.Time { return now },
		ReadWorkspaceActivity: inactiveRetentionActivity,
		ReadDiskUsage:         stableRetentionDiskUsage,
		CloseWorkspace: func(context.Context, string, Candidate) error {
			return context.Canceled
		},
	})
	if err == nil || !strings.Contains(err.Error(), "archive-free remover") {
		t.Fatalf("interruption err=%v", err)
	}
	purging := filepath.Join(root, ".capsules", "workspaces", "closed-purging-interrupted")
	if _, err := os.Stat(purging); err != nil {
		t.Fatalf("interrupted isolated quarantine is not visible: %v", err)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("original path still exists after atomic isolation: %v", err)
	}

	result, err := PurgeClosedWorkspace(context.Background(), PurgeOptions{
		ProjectRoot:           root,
		Receipt:               receipt,
		KeepWorkspaces:        -1,
		MinAge:                24 * time.Hour,
		CurrentPath:           root,
		Now:                   func() time.Time { return now.Add(time.Minute) },
		ReadWorkspaceActivity: inactiveRetentionActivity,
		ReadDiskUsage:         stableRetentionDiskUsage,
	})
	if err != nil || !result.OK || result.Status != "purged" {
		t.Fatalf("resumed result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(purging); !os.IsNotExist(err) {
		t.Fatalf("resumed purge left isolated quarantine: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".artifacts", "workspace-close")); !os.IsNotExist(err) {
		t.Fatalf("archive-free purge created workspace-close archives: %v", err)
	}
}

func TestPurgeClosedWorkspaceEnforcesMonotonicDiskReceipts(t *testing.T) {
	for _, tt := range []struct {
		name       string
		free       []int64
		wantStatus string
		wantCode   string
		wantExists bool
	}{
		{
			name:       "intent write regression stops before isolation",
			free:       []int64{100 << 20, 98 << 20},
			wantStatus: "skipped",
			wantCode:   "intent_not_monotonic",
			wantExists: true,
		},
		{
			name:       "post purge regression remains typed",
			free:       []int64{100 << 20, 100 << 20, 98 << 20},
			wantStatus: "purged",
			wantCode:   "post_purge_space_regressed",
			wantExists: false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			initLegacyProject(t, root)
			now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
			workspace := writeLegacyWorkspace(t, root, "closed-monotonic", now.Add(-48*time.Hour), false, false)
			writePurgeProviderStub(t, root)
			head := strings.TrimSpace(runHygieneCommand(t, workspace, "git", "rev-parse", "HEAD"))
			recoveryRef := "refs/kitsoki/workspace-teardown-recovery/" + head
			runHygieneGit(t, root, "update-ref", recoveryRef, head)
			receipt := validRetentionReceipt(root, filepath.Base(workspace), head, recoveryRef, now)
			call := 0
			result, err := PurgeClosedWorkspace(context.Background(), PurgeOptions{
				ProjectRoot:           root,
				Receipt:               receipt,
				KeepWorkspaces:        -1,
				MinAge:                24 * time.Hour,
				CurrentPath:           root,
				Now:                   func() time.Time { return now },
				ReadWorkspaceActivity: inactiveRetentionActivity,
				ReadDiskUsage: func(string) (DiskUsage, error) {
					index := call
					call++
					if index >= len(tt.free) {
						index = len(tt.free) - 1
					}
					return DiskUsage{Known: true, FreeBytes: tt.free[index]}, nil
				},
			})
			if err != nil || result.Status != tt.wantStatus || result.ReasonCode != tt.wantCode {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			_, statErr := os.Stat(workspace)
			if tt.wantExists && statErr != nil {
				t.Fatalf("workspace should remain visible: %v", statErr)
			}
			if !tt.wantExists && !os.IsNotExist(statErr) {
				t.Fatalf("workspace should be removed: %v", statErr)
			}
			if _, err := os.Stat(filepath.Join(root, ".artifacts", "workspace-close")); !os.IsNotExist(err) {
				t.Fatalf("monotonic purge created archives: %v", err)
			}
		})
	}
}

func TestPurgeClosedWorkspaceRejectsMalformedOrSingleProbeReceipt(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	receipt := validRetentionReceipt(root, "closed-safe", strings.Repeat("a", 40), "refs/kitsoki/workspace-teardown-recovery/"+strings.Repeat("a", 40), now)
	receipt.ActivityProbe.Kind = receipt.ProcessSnapshot.Kind
	if _, err := PurgeClosedWorkspace(context.Background(), PurgeOptions{
		ProjectRoot: root,
		Receipt:     receipt,
		Now:         func() time.Time { return now },
	}); err == nil || !strings.Contains(err.Error(), "two distinct safe") {
		t.Fatalf("err=%v", err)
	}
}

func validRetentionReceipt(root, id, head, recoveryRef string, now time.Time) RetentionReceipt {
	issued := now.Add(-25 * time.Hour)
	return RetentionReceipt{
		Schema:        RetentionReceiptSchema,
		Project:       root,
		WorkspaceID:   id,
		WorkspacePath: filepath.ToSlash(filepath.Join(".capsules", "workspaces", id)),
		Head:          head,
		RecoveryRef:   recoveryRef,
		IssuedAt:      issued,
		EligibleAfter: issued.Add(24 * time.Hour),
		ProcessSnapshot: RetentionProbe{
			Kind:       "process-table",
			CapturedAt: issued.Add(-time.Minute),
			Safe:       true,
		},
		ActivityProbe: RetentionProbe{
			Kind:       "open-file-scan",
			CapturedAt: issued.Add(-2 * time.Minute),
			Safe:       true,
		},
	}
}

func stableRetentionDiskUsage(string) (DiskUsage, error) {
	return DiskUsage{Known: true, FreeBytes: 1 << 40}, nil
}

func inactiveRetentionActivity(context.Context, []string) (WorkspaceActivity, error) {
	return WorkspaceActivity{Known: true, PIDsByPath: map[string][]int{}}, nil
}

func writePurgeProviderStub(t *testing.T, root string) {
	t.Helper()
	script := filepath.Join(root, "scripts", "dev-workspace.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func resultProjectRoot(t *testing.T, root string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
