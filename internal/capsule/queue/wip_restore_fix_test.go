package queue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// restoreCapturedPaths: file -> non-empty-directory type collision.
//
// Before this fix, `git checkout -q HEAD -- path` for a path whose HEAD
// content is a tracked file, but whose worktree now holds a non-empty
// directory, silently recreated the tracked file by recursively removing
// that directory — destroying any untracked content inside it with no
// warning. The only symptom was an incidental ENOTDIR surfacing later, from
// unrelated cleanup of a sibling porcelain entry that no longer existed in
// directory form. See TestWoeRestoreCapturedPathsFailsWhenTrackedPathBecame-
// NonEmptyDirectory in wip_ops_edge_test.go, which still passes unmodified:
// it only asserts that restoreCapturedPaths returns a non-nil error for that
// direct, capture-less call, which remains true — the fix now returns that
// error *before* touching the checkout instead of incidentally afterward.
// ---------------------------------------------------------------------------

// TestWrfPreserveWIPRefusesDestructiveRestoreOnFileToDirectoryCollision runs
// the real preserve/restore flow (PreserveWIP, not the lower-level
// restoreCapturedPaths directly) over the exact collision this bug report
// describes: tracked.txt is captured correctly (its directory contents fold
// into the preserved commit, since captureTree's `git add -A` follows the
// worktree's current type), but restoring it back to a plain HEAD file would
// require destroying the untracked nested file first. The fix refuses that
// destructive restore instead of doing it silently: PreserveWIP must fail
// loudly with a typed, actionable error, and the untracked bytes must be
// provably intact — both still on disk (the collision was never touched)
// and on the preserved branch (capture already ran before the restore step
// was ever reached).
func TestWrfPreserveWIPRefusesDestructiveRestoreOnFileToDirectoryCollision(t *testing.T) {
	dir := wipRepo(t) // tracked.txt @ "v1" committed on HEAD.
	if err := os.Remove(filepath.Join(dir, "tracked.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "tracked.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	innerPath := filepath.Join(dir, "tracked.txt", "inner.txt")
	if err := os.WriteFile(innerPath, []byte("untracked nested content"), 0o644); err != nil {
		t.Fatal(err)
	}

	branch, skipped, err := PreserveWIP(context.Background(), dir, time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("expected PreserveWIP to fail loudly on a file-to-directory type collision, not restore silently")
	}
	var collision *RestoreTypeCollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("expected err to wrap *RestoreTypeCollisionError, got: %v", err)
	}
	if collision.Path != "tracked.txt" {
		t.Fatalf("collision.Path = %q, want %q", collision.Path, "tracked.txt")
	}
	if collision.Entries != 1 {
		t.Fatalf("collision.Entries = %d, want 1", collision.Entries)
	}
	if len(skipped) != 0 {
		t.Fatalf("skipped=%v, want none: this path was fully capturable, just not safely restorable", skipped)
	}
	if !strings.HasPrefix(branch, "queue/preserved-wip/") {
		t.Fatalf("branch=%q, want a preserved branch to still be reported so the collision can be inspected", branch)
	}

	// Provable survival #1: the untracked bytes are still on disk, exactly
	// as found — the collision was left completely untouched.
	got, err := os.ReadFile(innerPath)
	if err != nil {
		t.Fatalf("untracked nested file was destroyed: %v", err)
	}
	if string(got) != "untracked nested content" {
		t.Fatalf("untracked nested file content changed: %q", got)
	}
	if info, err := os.Stat(filepath.Join(dir, "tracked.txt")); err != nil || !info.IsDir() {
		t.Fatalf("tracked.txt should still be the untouched directory, stat=%v err=%v", info, err)
	}

	// Provable survival #2: the same bytes are also already on the
	// preserved branch — capture ran (and folded the directory's contents
	// into the tree) before the refused restore step was ever reached.
	onBranch := strings.TrimSpace(run(t, dir, "git", "show", branch+":tracked.txt/inner.txt"))
	if onBranch != "untracked nested content" {
		t.Fatalf("preserved branch %s tracked.txt/inner.txt = %q, want %q", branch, onBranch, "untracked nested content")
	}
}

// TestWrfPreserveWIPPlainFileToFileRestoreStillWorks is the control: an
// ordinary modified tracked file, with no directory involved at all, must
// still capture and restore exactly as before the fix.
func TestWrfPreserveWIPPlainFileToFileRestoreStillWorks(t *testing.T) {
	dir := wipRepo(t) // tracked.txt @ "v1" committed on HEAD.
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}

	branch, skipped, err := PreserveWIP(context.Background(), dir, time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("plain file-to-file restore should still succeed: %v", err)
	}
	if len(skipped) != 0 {
		t.Fatalf("skipped=%v, want none", skipped)
	}
	if !strings.HasPrefix(branch, "queue/preserved-wip/") {
		t.Fatalf("branch=%q", branch)
	}
	if status := run(t, dir, "git", "status", "--porcelain", "--untracked-files=all", "--", ".", ":(exclude).capsules"); status != "" {
		t.Fatalf("checkout not clean: %q", status)
	}
	got, err := os.ReadFile(filepath.Join(dir, "tracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v1" {
		t.Fatalf("tracked.txt = %q, want restored to v1", got)
	}
	if onBranch := strings.TrimSpace(run(t, dir, "git", "show", branch+":tracked.txt")); onBranch != "v2" {
		t.Fatalf("preserved branch tracked.txt = %q, want v2", onBranch)
	}
}

// TestWrfPreserveWIPDirectoryWithOnlyCapturedContentRestoresCleanly covers a
// directory that is NOT a type collision: sub/ was already a directory in
// HEAD (holding a tracked file), a tracked file inside it is modified, and a
// brand-new untracked file is added alongside it in the same directory. No
// path here ever flips between blob and tree, so the new collision check
// must not fire, and the whole capture+restore must still complete with no
// error and a fully clean checkout.
func TestWrfPreserveWIPDirectoryWithOnlyCapturedContentRestoresCleanly(t *testing.T) {
	dir := wipRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	commit(t, dir, "sub/nested.txt", "v1", "add nested")
	if err := os.WriteFile(filepath.Join(dir, "sub", "nested.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "extra.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	branch, skipped, err := PreserveWIP(context.Background(), dir, time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("directory restore with only captured content should succeed cleanly: %v", err)
	}
	if len(skipped) != 0 {
		t.Fatalf("skipped=%v, want none", skipped)
	}
	if status := run(t, dir, "git", "status", "--porcelain", "--untracked-files=all", "--", ".", ":(exclude).capsules"); status != "" {
		t.Fatalf("checkout not clean: %q", status)
	}
	got, err := os.ReadFile(filepath.Join(dir, "sub", "nested.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v1" {
		t.Fatalf("sub/nested.txt = %q, want restored to v1", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "extra.txt")); !os.IsNotExist(err) {
		t.Fatalf("sub/extra.txt should have been cleaned from the checkout, stat err=%v", err)
	}
	if onBranch := strings.TrimSpace(run(t, dir, "git", "show", branch+":sub/extra.txt")); onBranch != "new" {
		t.Fatalf("preserved branch sub/extra.txt = %q, want %q", onBranch, "new")
	}
}
