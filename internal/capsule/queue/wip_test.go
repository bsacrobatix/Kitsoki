package queue

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func wipRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-b", "main")
	git(t, dir, "config", "user.email", "queue@test")
	git(t, dir, "config", "user.name", "queue")
	commit(t, dir, "tracked.txt", "v1", "initial")
	return dir
}

func TestPreserveWIPCleanCheckoutIsNoOp(t *testing.T) {
	dir := wipRepo(t)
	branch, skipped, err := PreserveWIP(context.Background(), dir, time.Now())
	if err != nil || branch != "" || len(skipped) != 0 {
		t.Fatalf("branch=%q skipped=%v err=%v", branch, skipped, err)
	}
}

func TestPreserveWIPCapturesEveryByteThenCleans(t *testing.T) {
	dir := wipRepo(t)
	// staged, unstaged, untracked, and a deletion — all at once.
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("v2-unstaged"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "staged.txt"), []byte("staged"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("untracked"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "nested/deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested/deep/file.txt"), []byte("deep"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Queue control state must survive untouched.
	if err := os.MkdirAll(filepath.Join(dir, ".capsules/queue"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".capsules/queue/state.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	branch, skipped, err := PreserveWIP(context.Background(), dir, time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(branch, "queue/preserved-wip/") {
		t.Fatalf("branch=%q", branch)
	}
	if len(skipped) != 0 {
		t.Fatalf("skipped=%v, want none", skipped)
	}
	// Checkout is clean again and control state survived.
	if status := run(t, dir, "git", "status", "--porcelain", "--untracked-files=all", "--", ".", ":(exclude).capsules"); strings.TrimSpace(status) != "" {
		t.Fatalf("checkout not clean: %q", status)
	}
	if _, err := os.Stat(filepath.Join(dir, ".capsules/queue/state.json")); err != nil {
		t.Fatalf("queue control state was destroyed: %v", err)
	}
	// Every byte is on the preserved branch.
	for path, want := range map[string]string{"tracked.txt": "v2-unstaged", "staged.txt": "staged", "untracked.txt": "untracked", "nested/deep/file.txt": "deep"} {
		got := run(t, dir, "git", "show", branch+":"+path)
		if strings.TrimSpace(got) != want {
			t.Fatalf("%s on %s = %q want %q", path, branch, got, want)
		}
	}
	// The preserved commit parents on the HEAD it was captured from.
	parent := strings.TrimSpace(run(t, dir, "git", "rev-parse", branch+"^"))
	head := strings.TrimSpace(run(t, dir, "git", "rev-parse", "HEAD"))
	if parent != head {
		t.Fatalf("preserved commit parent %s != HEAD %s", parent, head)
	}
}

func TestPreserveWIPSecondCaptureGetsFreshBranch(t *testing.T) {
	dir := wipRepo(t)
	at := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	if err := os.WriteFile(filepath.Join(dir, "one.txt"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, _, err := PreserveWIP(context.Background(), dir, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "two.txt"), []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, _, err := PreserveWIP(context.Background(), dir, at) // same stamp on purpose
	if err != nil {
		t.Fatal(err)
	}
	if first == second || second == "" {
		t.Fatalf("first=%q second=%q", first, second)
	}
	if got := strings.TrimSpace(run(t, dir, "git", "show", first+":one.txt")); got != "one" {
		t.Fatalf("first capture lost: %q", got)
	}
	if got := strings.TrimSpace(run(t, dir, "git", "show", second+":two.txt")); got != "two" {
		t.Fatalf("second capture lost: %q", got)
	}
}

// TestPorcelainPathsHandlesLeadingSpaceOnFirstLine guards a real regression:
// gitOutput's strings.TrimSpace on the whole multi-line status blob strips
// the leading space off ONLY the first line (porcelain v1's status column
// can legitimately start with a space, e.g. " M base.txt" for a
// modified-but-not-staged file), shifting the fixed-column parse by one
// byte for that entry alone — "base.txt" parsed as "ase.txt", silently
// skipping the real file. porcelainPaths must be fed raw, untrimmed status
// text (see porcelainStatus) for this to parse correctly regardless of
// which line happens to come first.
func TestPorcelainPathsHandlesLeadingSpaceOnFirstLine(t *testing.T) {
	status := " M base.txt\n?? untracked.txt\nD  deleted.txt\n"
	got := porcelainPaths(status)
	want := []string{"base.txt", "untracked.txt", "deleted.txt"}
	if len(got) != len(want) {
		t.Fatalf("porcelainPaths(%q) = %v, want %v", status, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("porcelainPaths(%q) = %v, want %v", status, got, want)
		}
	}
}

// TestPreserveWIPToleratesUnreadablePathAndCapturesTheRest guards the fix
// this file exists for: `git add -A` makes zero progress on ANY path when
// even one path in the batch is unreadable (permission-denied, an
// unreadable embedded repo, ...), so a single such path used to abort
// capture — and therefore the whole finalization — of every other dirty
// path in the checkout too. The unreadable path must be skipped, evidenced,
// and left completely untouched (never reset, never cleaned — its content
// was never backed up), while every other dirty path is still captured and
// cleaned normally.
func TestPreserveWIPToleratesUnreadablePathAndCapturesTheRest(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions; this test needs a non-root process")
	}
	dir := wipRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "capturable.txt"), []byte("capture me"), 0o644); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(dir, "locked.txt")
	if err := os.WriteFile(locked, []byte("original locked content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o644) })

	branch, skipped, err := PreserveWIP(context.Background(), dir, time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("PreserveWIP should tolerate an unreadable path, got err=%v", err)
	}
	if !strings.HasPrefix(branch, "queue/preserved-wip/") {
		t.Fatalf("branch=%q, want a preserved branch (capturable.txt was dirty)", branch)
	}
	if len(skipped) != 1 || skipped[0] != "locked.txt" {
		t.Fatalf("skipped=%v, want exactly [locked.txt]", skipped)
	}
	// The capturable file made it onto the preserved branch and the
	// checkout was cleaned for it.
	if got := strings.TrimSpace(run(t, dir, "git", "show", branch+":capturable.txt")); got != "capture me" {
		t.Fatalf("capturable.txt on %s = %q, want %q", branch, got, "capture me")
	}
	if _, err := os.Stat(filepath.Join(dir, "capturable.txt")); !os.IsNotExist(err) {
		t.Fatalf("capturable.txt should have been cleaned from the checkout, stat err=%v", err)
	}
	// The unreadable file is completely untouched: same permissions, same
	// content, never captured (it cannot appear on the preserved branch —
	// git could not read it to back it up).
	info, err := os.Lstat(locked)
	if err != nil {
		t.Fatalf("locked.txt should still exist untouched: %v", err)
	}
	if info.Mode().Perm() != 0o000 {
		t.Fatalf("locked.txt permissions changed: %v", info.Mode().Perm())
	}
	if _, err := gitOutput(context.Background(), dir, "cat-file", "-e", branch+":locked.txt"); err == nil {
		t.Fatal("locked.txt must not appear on the preserved branch; its content was never read")
	}
	if err := os.Chmod(locked, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(locked)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original locked content" {
		t.Fatalf("locked.txt content changed: %q", got)
	}
}

// TestPreserveWIPLeavesCheckoutUntouchedWhenOnlyUnreadablePathIsDirty covers
// the other branch of the same fix: when the ENTIRE dirty set is
// uncapturable, PreserveWIP must not fabricate an empty preservation and
// must not attempt any cleanup — there is nothing safe to clean.
func TestPreserveWIPLeavesCheckoutUntouchedWhenOnlyUnreadablePathIsDirty(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions; this test needs a non-root process")
	}
	dir := wipRepo(t)
	locked := filepath.Join(dir, "locked.txt")
	if err := os.WriteFile(locked, []byte("only locked content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o644) })

	branch, skipped, err := PreserveWIP(context.Background(), dir, time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("PreserveWIP should not error when the only dirtiness is unreadable, got err=%v", err)
	}
	if branch != "" {
		t.Fatalf("branch=%q, want none: nothing capturable was dirty", branch)
	}
	if len(skipped) != 1 || skipped[0] != "locked.txt" {
		t.Fatalf("skipped=%v, want exactly [locked.txt]", skipped)
	}
	if err := os.Chmod(locked, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(locked)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "only locked content" {
		t.Fatalf("locked.txt content changed: %q", got)
	}
}
