package queue

// This file closes under-covered edges in WIP preservation (wip.go) and the
// operator verbs (ops.go). Every top-level identifier declared here is
// prefixed with "woe" (tests keep the required "Test" prefix as "TestWoe...")
// to avoid redeclaration collisions with other files added to this package
// concurrently.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Shared fixtures
// ---------------------------------------------------------------------------

// woeInterceptGit installs a fake "git" on PATH that fails with failMsg for
// exactly the invocation whose argv equals failArgs, and execs the real git
// binary unmodified for every other invocation. It is restored automatically
// via t.Setenv/t.Cleanup. This lets a test fault-inject a single git
// subcommand deep inside a multi-step function (e.g. PreserveWIP) without a
// timing-dependent race and without touching production code.
func woeInterceptGit(t *testing.T, failArgs []string, failMsg string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("locate real git binary: %v", err)
	}
	dir := t.TempDir()
	var cond strings.Builder
	fmt.Fprintf(&cond, "[ \"$#\" -eq %d ]", len(failArgs))
	for i, a := range failArgs {
		fmt.Fprintf(&cond, " && [ \"$%d\" = %q ]", i+1, a)
	}
	script := fmt.Sprintf("#!/bin/sh\nif %s; then\n  echo %q >&2\n  exit 1\nfi\nexec %q \"$@\"\n", cond.String(), failMsg, realGit)
	scriptPath := filepath.Join(dir, "git")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// woeInterceptGitOnSecondStatusCall installs a fake git on PATH that, the
// instant it sees the second invocation whose first argument is "status",
// writes raceContent to raceFile (an absolute path) before handing off to
// the real git — deterministically simulating a concurrent edit landing
// exactly between PreserveWIP's initial dirty-tree capture and its
// byte-completeness proof re-capture (both go through porcelainStatus's
// `git status` call, and no other call in a successful capture path issues
// `status`). This lets the race PreserveWIP's proof exists to catch be
// tested without any timing dependency.
func woeInterceptGitOnSecondStatusCall(t *testing.T, raceFile, raceContent string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("locate real git binary: %v", err)
	}
	binDir := t.TempDir()
	counter := filepath.Join(t.TempDir(), "status-count")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = \"status\" ]; then\n  n=$(cat %q 2>/dev/null || echo 0)\n  n=$((n+1))\n  echo \"$n\" > %q\n  if [ \"$n\" = \"2\" ]; then\n    printf '%%s' %q > %q\n  fi\nfi\nexec %q \"$@\"\n",
		counter, counter, raceContent, raceFile, realGit)
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// woeCreateSiblingRefs creates count+1 refs (base, base-1, ..., base-count)
// all pointing at the repo's current HEAD, to deterministically exhaust
// preservedBranchName's collision-numbering loop.
func woeCreateSiblingRefs(t *testing.T, dir, base string, count int) {
	t.Helper()
	sha := git(t, dir, "rev-parse", "HEAD")
	for i := 0; i <= count; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		git(t, dir, "update-ref", "refs/heads/"+name, sha)
	}
}

// woeHashObject writes content as a git blob and returns its SHA.
func woeHashObject(t *testing.T, dir, content string) string {
	t.Helper()
	cmd := exec.Command("git", "hash-object", "-w", "--stdin")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(content)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git hash-object: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// woeMoveRefToNewTree commits newContent for path as a new commit on top of
// the repo's current HEAD, entirely through plumbing (hash-object, mktree,
// commit-tree, update-ref) so the ref moves without ever touching the index
// or the working tree. This reproduces the exact precondition
// syncProtectedCheckout is designed for: the just-CAS'd target ref has
// already advanced, but the protected worktree still holds the pre-CAS
// content until a caller (that has preserved WIP first) explicitly syncs it.
func woeMoveRefToNewTree(t *testing.T, dir, path, newContent, message string) string {
	t.Helper()
	blob := woeHashObject(t, dir, newContent)
	oldSHA := git(t, dir, "rev-parse", "HEAD")
	cmd := exec.Command("git", "mktree")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(fmt.Sprintf("100644 blob %s\t%s\n", blob, path))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git mktree: %v\n%s", err, out)
	}
	newTree := strings.TrimSpace(string(out))
	newSHA := git(t, dir, "commit-tree", newTree, "-p", oldSHA, "-m", message)
	git(t, dir, "update-ref", "refs/heads/main", newSHA)
	return newSHA
}

// ---------------------------------------------------------------------------
// WIP preservation: PreserveWIP
// ---------------------------------------------------------------------------

// TestWoePreserveWIPCapturesDeletionAlongsideModifiedAndUntracked pins the
// combined case the recent regression fix was really about: a tracked
// deletion, a tracked modification, and a brand-new untracked file must all
// be captured and cleaned together, not just each in isolation.
func TestWoePreserveWIPCapturesDeletionAlongsideModifiedAndUntracked(t *testing.T) {
	dir := wipRepo(t)
	commit(t, dir, "gone.txt", "will be deleted", "add gone")
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fresh.txt"), []byte("fresh"), 0o644); err != nil {
		t.Fatal(err)
	}

	branch, skipped, err := PreserveWIP(context.Background(), dir, time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 0 {
		t.Fatalf("skipped=%v, want none", skipped)
	}
	if _, err := gitOutput(context.Background(), dir, "cat-file", "-e", branch+":gone.txt"); err == nil {
		t.Fatal("gone.txt must not exist on the preserved branch; it was deleted")
	}
	if got := strings.TrimSpace(run(t, dir, "git", "show", branch+":tracked.txt")); got != "modified" {
		t.Fatalf("tracked.txt on %s = %q, want %q", branch, got, "modified")
	}
	if got := strings.TrimSpace(run(t, dir, "git", "show", branch+":fresh.txt")); got != "fresh" {
		t.Fatalf("fresh.txt on %s = %q, want %q", branch, got, "fresh")
	}
	// The checkout is restored to clean HEAD, not left in its dirty state:
	// gone.txt (deleted locally, but still present in HEAD) reappears.
	if got, err := os.ReadFile(filepath.Join(dir, "gone.txt")); err != nil || string(got) != "will be deleted" {
		t.Fatalf("gone.txt not restored to HEAD content: %q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "tracked.txt")); err != nil || string(got) != "v1" {
		t.Fatalf("tracked.txt not restored to HEAD content: %q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "fresh.txt")); !os.IsNotExist(err) {
		t.Fatal("fresh.txt should have been cleaned from the checkout")
	}
}

// TestWoePreserveWIPDefaultsCaptureTimeWhenZero pins PreserveWIP's zero-value
// `at` fallback to time.Now().UTC().
func TestWoePreserveWIPDefaultsCaptureTimeWhenZero(t *testing.T) {
	dir := wipRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC().Add(-time.Second)
	branch, _, err := PreserveWIP(context.Background(), dir, time.Time{})
	after := time.Now().UTC().Add(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(preservedWIPPrefix) + `(\d{8}T\d{6}Z)`)
	m := re.FindStringSubmatch(branch)
	if m == nil {
		t.Fatalf("branch %q does not match the expected stamp format", branch)
	}
	got, err := time.Parse("20060102T150405Z", m[1])
	if err != nil {
		t.Fatalf("branch stamp %q did not parse: %v", m[1], err)
	}
	if got.Before(before) || got.After(after) {
		t.Fatalf("branch stamp %v not within [%v,%v]; zero `at` did not default to now", got, before, after)
	}
}

// TestWoePreserveWIPFailsWithoutGitRepo covers snapshotTree's (and therefore
// PreserveWIP's) first error return: a root that isn't a git checkout at all.
func TestWoePreserveWIPFailsWithoutGitRepo(t *testing.T) {
	dir := t.TempDir()
	branch, skipped, err := PreserveWIP(context.Background(), dir, time.Now())
	if err == nil {
		t.Fatalf("expected an error outside a git repository, got branch=%q skipped=%v", branch, skipped)
	}
	if branch != "" {
		t.Fatalf("branch=%q, want empty on failure", branch)
	}
}

// TestWoePreserveWIPFailsWhenHeadTreeLookupErrors covers PreserveWIP's own
// `rev-parse HEAD^{tree}` error branch, which is otherwise unreachable in
// practice (snapshotTree succeeding already implies HEAD resolves): it is
// fault-injected via a PATH-shadowed git that fails only that exact
// invocation, leaving every other git call (including snapshotTree's) real.
func TestWoePreserveWIPFailsWhenHeadTreeLookupErrors(t *testing.T) {
	dir := wipRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	woeInterceptGit(t, []string{"rev-parse", "HEAD^{tree}"}, "fatal: woe-injected rev-parse failure")

	branch, skipped, err := PreserveWIP(context.Background(), dir, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatalf("expected HEAD^{tree} lookup failure, got branch=%q skipped=%v", branch, skipped)
	}
	if branch != "" {
		t.Fatalf("branch=%q, want empty on failure", branch)
	}
}

// TestWoePreserveWIPFailsWhenCommitTreeIdentityMissing covers the
// commit-tree error branch: git refuses to create any commit (including via
// commit-tree) without a resolvable author/committer identity. The checkout
// must stay exactly as dirty as it was; no branch is produced.
func TestWoePreserveWIPFailsWhenCommitTreeIdentityMissing(t *testing.T) {
	dir := wipRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir()) // no global gitconfig fallback identity
	t.Setenv("GIT_AUTHOR_NAME", "")
	t.Setenv("GIT_AUTHOR_EMAIL", "")
	t.Setenv("GIT_COMMITTER_NAME", "")
	t.Setenv("GIT_COMMITTER_EMAIL", "")

	branch, skipped, err := PreserveWIP(context.Background(), dir, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatalf("expected commit-tree identity failure, got branch=%q skipped=%v", branch, skipped)
	}
	if branch != "" {
		t.Fatalf("branch=%q, want empty on failure", branch)
	}
	got, err := os.ReadFile(filepath.Join(dir, "dirty.txt"))
	if err != nil || string(got) != "dirty" {
		t.Fatalf("checkout mutated after a failed capture: content=%q err=%v", got, err)
	}
}

// TestWoePreserveWIPFailsWhenBranchRefPathCollides covers the `git branch`
// creation error branch: a plain file already occupying part of the target
// ref's directory path makes ref creation impossible.
func TestWoePreserveWIPFailsWhenBranchRefPathCollides(t *testing.T) {
	dir := wipRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	refParent := filepath.Join(dir, ".git", "refs", "heads", "queue")
	if err := os.MkdirAll(refParent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(refParent, "preserved-wip"), []byte("blocking"), 0o644); err != nil {
		t.Fatal(err)
	}

	branch, skipped, err := PreserveWIP(context.Background(), dir, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatalf("expected branch creation to fail on the ref-path collision, got branch=%q skipped=%v", branch, skipped)
	}
	if branch != "" {
		t.Fatalf("branch=%q, want empty on failure", branch)
	}
}

// TestWoePreserveWIPAbortsWhenCheckoutChangesDuringCapture pins the
// byte-completeness proof this file's whole design revolves around: if the
// checkout changes between the initial dirty-tree capture and the
// re-capture used to prove nothing moved, PreserveWIP must abort with the
// checkout left exactly as the race left it (never partially cleaned) while
// keeping the already-created preserved branch (a harmless superset of
// nothing). The race is fault-injected deterministically: a PATH-shadowed
// git modifies the checkout the instant it sees the SECOND `git status`
// invocation, which is exactly the proof re-capture inside snapshotTree.
func TestWoePreserveWIPAbortsWhenCheckoutChangesDuringCapture(t *testing.T) {
	dir := wipRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "original.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	woeInterceptGitOnSecondStatusCall(t, filepath.Join(dir, "race.txt"), "raced content")

	branch, skipped, err := PreserveWIP(context.Background(), dir, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "checkout changed during WIP capture") {
		t.Fatalf("err=%v, branch=%q, skipped=%v", err, branch, skipped)
	}
	if !strings.HasPrefix(branch, preservedWIPPrefix) {
		t.Fatalf("branch=%q, want the preserved branch retained despite the abort", branch)
	}
	got, err := os.ReadFile(filepath.Join(dir, "original.txt"))
	if err != nil || string(got) != "original" {
		t.Fatalf("original.txt mutated after an aborted capture: %q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "race.txt")); err != nil {
		t.Fatalf("the raced-in file should remain in the checkout after abort: %v", err)
	}
}

// ---------------------------------------------------------------------------
// snapshotTree / captureTree / porcelainStatus / restoreCapturedPaths
// ---------------------------------------------------------------------------

func TestWoeSnapshotTreeFailsWithoutGitRepo(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := snapshotTree(context.Background(), dir); err == nil {
		t.Fatal("expected snapshotTree to fail outside a git repository")
	}
}

func TestWoePorcelainStatusFailsWithoutGitRepo(t *testing.T) {
	dir := t.TempDir()
	if _, err := porcelainStatus(context.Background(), dir); err == nil {
		t.Fatal("expected porcelainStatus to fail outside a git repository")
	}
}

func TestWoeCaptureTreeFailsWithoutGitRepo(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := captureTree(context.Background(), dir, nil); err == nil {
		t.Fatal("expected captureTree to fail resolving --git-path outside a git repository")
	}
}

// TestWoeCaptureTreeFailsWhenNoCommitsExist covers captureTree's `read-tree
// HEAD` error branch: a repository that has never committed has no HEAD to
// read, distinct from (and reached before) any dirty-path handling.
func TestWoeCaptureTreeFailsWhenNoCommitsExist(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-b", "main")
	if _, _, err := captureTree(context.Background(), dir, nil); err == nil {
		t.Fatal("expected captureTree to fail before any commit exists")
	}
}

func TestWoeRestoreCapturedPathsFailsWithoutGitRepo(t *testing.T) {
	dir := t.TempDir()
	if err := restoreCapturedPaths(context.Background(), dir, nil); err == nil {
		t.Fatal("expected restoreCapturedPaths to fail outside a git repository")
	}
}

// TestWoeRestoreCapturedPathsFailsWhenTrackedPathBecameNonEmptyDirectory
// covers the os.Remove error branch for an untracked-file cleanup: when a
// captured path's type flips from file to directory, `git checkout HEAD --
// tracked.txt` replaces the directory with the tracked file (git's own
// behavior), which then makes the sibling untracked-file cleanup collide
// with the recreated file (ENOTDIR is not os.IsNotExist), and restoration
// must surface that failure rather than silently swallowing it.
func TestWoeRestoreCapturedPathsFailsWhenTrackedPathBecameNonEmptyDirectory(t *testing.T) {
	dir := wipRepo(t)
	if err := os.Remove(filepath.Join(dir, "tracked.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "tracked.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt", "inner.txt"), []byte("untracked inner"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := restoreCapturedPaths(context.Background(), dir, nil); err == nil {
		t.Fatal("expected restore to fail when a captured path's type changed from file to directory")
	}
}

// TestWoeRestoreCapturedPathsFailsWhenParentDirDeniesWrite covers the `git
// checkout HEAD -- path` error branch for a tracked path: git cannot unlink
// the stale worktree file when its parent directory forbids writes.
func TestWoeRestoreCapturedPathsFailsWhenParentDirDeniesWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions; this test needs a non-root process")
	}
	dir := wipRepo(t)
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	commit(t, dir, "sub/nested.txt", "v1", "add nested")
	if err := os.WriteFile(filepath.Join(sub, "nested.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	if err := restoreCapturedPaths(context.Background(), dir, nil); err == nil {
		t.Fatal("expected restore checkout to fail when the parent directory denies write")
	}
}

// ---------------------------------------------------------------------------
// preservedBranchName
// ---------------------------------------------------------------------------

// TestWoePreservedBranchNameExhaustsCollisionSlots pins the i>100 refusal:
// once a preserved-WIP stamp and all 100 of its numbered siblings are
// occupied, allocation must fail loudly rather than loop forever or silently
// overwrite an existing branch.
func TestWoePreservedBranchNameExhaustsCollisionSlots(t *testing.T) {
	parallelQueueIntegrationTest(t)
	dir := wipRepo(t)
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	base := preservedWIPPrefix + at.UTC().Format("20060102T150405Z")
	woeCreateSiblingRefs(t, dir, base, 100)

	_, err := preservedBranchName(context.Background(), dir, at)
	if err == nil || !strings.Contains(err.Error(), "cannot allocate preserved WIP branch name") {
		t.Fatalf("err=%v, want collision-exhaustion error", err)
	}
}

// ---------------------------------------------------------------------------
// equalPathSets / setDiff / porcelainPaths
// ---------------------------------------------------------------------------

func TestWoeEqualPathSetsDetectsLengthMismatch(t *testing.T) {
	if equalPathSets([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("expected differing lengths to compare unequal")
	}
}

func TestWoeEqualPathSetsIgnoresOrderButNotContent(t *testing.T) {
	if !equalPathSets([]string{"b", "a"}, []string{"a", "b"}) {
		t.Fatal("expected reordered sets to compare equal")
	}
	if equalPathSets([]string{"a", "b"}, []string{"a", "c"}) {
		t.Fatal("expected differing content at equal length to compare unequal")
	}
}

func TestWoeSetDiffReturnsMembersNotInExclude(t *testing.T) {
	got := setDiff([]string{"a", "b", "c"}, []string{"b"})
	want := []string{"a", "c"}
	if len(got) != len(want) {
		t.Fatalf("setDiff=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("setDiff=%v want %v", got, want)
		}
	}
}

func TestWoePorcelainPathsResolvesRenameArrowToDestination(t *testing.T) {
	got := porcelainPaths("R  old.txt -> new.txt\n")
	if len(got) != 1 || got[0] != "new.txt" {
		t.Fatalf("porcelainPaths=%v want [new.txt]", got)
	}
}

// ---------------------------------------------------------------------------
// syncProtectedCheckout
// ---------------------------------------------------------------------------

// TestWoeSyncProtectedCheckoutNoOpWhenCheckedOutBranchDiffers covers the
// early no-op return: a checkout on a branch other than the just-updated
// target must be left completely alone.
func TestWoeSyncProtectedCheckoutNoOpWhenCheckedOutBranchDiffers(t *testing.T) {
	dir := wipRepo(t)
	oldSHA := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "checkout", "-q", "-b", "other")
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("dirty-and-irrelevant"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := syncProtectedCheckout(context.Background(), dir, "refs/heads/main", oldSHA); err != nil {
		t.Fatalf("expected a no-op, got err=%v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "tracked.txt"))
	if err != nil || string(got) != "dirty-and-irrelevant" {
		t.Fatalf("checkout mutated even though a different branch is checked out: %q err=%v", got, err)
	}
}

// TestWoeSyncProtectedCheckoutRefusesWhenWorktreeDivergedFromOldTip covers the
// collision refusal: a local edit on a path the landing ALSO changed must never
// be overwritten. The safety property is unchanged; the refusal is now raised as
// a typed *ProtectedSyncConflictError that names the offending path, instead of
// a bare string mentioning only the pre-finalization tip.
func TestWoeSyncProtectedCheckoutRefusesWhenWorktreeDivergedFromOldTip(t *testing.T) {
	dir := wipRepo(t)
	oldSHA := git(t, dir, "rev-parse", "HEAD")
	woeMoveRefToNewTree(t, dir, "tracked.txt", "v2", "advance")
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("locally-dirty"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := syncProtectedCheckout(context.Background(), dir, "refs/heads/main", oldSHA)
	if err == nil {
		t.Fatal("expected a refusal when the landing and the worktree both changed tracked.txt")
	}
	var conflict *ProtectedSyncConflictError
	if !asProtectedSyncConflict(err, &conflict) {
		t.Fatalf("err=%T %v, want *ProtectedSyncConflictError", err, err)
	}
	if !strings.Contains(err.Error(), "tracked.txt") || !strings.Contains(err.Error(), oldSHA) {
		t.Fatalf("refusal must name both the conflicting path and the pre-finalization tip: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "tracked.txt"))
	if err != nil || string(got) != "locally-dirty" {
		t.Fatalf("checkout mutated despite refusal: %q err=%v", got, err)
	}
}

// TestWoeSyncProtectedCheckoutIgnoresUntrackedFilesOutsideTheLanding replaces
// an earlier assertion that an untracked file ANYWHERE in the checkout must
// abort the whole sync. That blanket refusal was the direct cause of a
// data-loss bug, so it is not a property worth keeping:
//
//   - It protected nothing. The sync's mutation step never removed untracked
//     files (`git reset --hard` does not, and the scoped form does not either),
//     so aborting on their account defended no content.
//   - Aborting was itself the harm. The protected ref has ALREADY moved by the
//     time the sync runs, so refusing left the index and worktree on the
//     pre-landing tree — i.e. a staged reversal of the commit that just landed,
//     which a plain `git commit` would then apply. Because the real protected
//     checkout permanently carries untracked/modified paths under
//     PreserveWIP's excluded roots, that abort fired on essentially every
//     landing.
//
// The genuine property — an untracked file sitting exactly where the landing
// ADDS a file must refuse loudly rather than be overwritten — is a collision on
// a landed path, and is covered by
// TestPsrSyncRefusesWhenTheLandingAddedAFileTheOperatorAlreadyCreated. Here the
// untracked file is on a path the landing never touched, so the sync must
// proceed and leave that file completely alone.
func TestWoeSyncProtectedCheckoutIgnoresUntrackedFilesOutsideTheLanding(t *testing.T) {
	dir := wipRepo(t)
	oldSHA := git(t, dir, "rev-parse", "HEAD")
	woeMoveRefToNewTree(t, dir, "tracked.txt", "v2", "advance")
	if err := os.WriteFile(filepath.Join(dir, "surprise.txt"), []byte("surprise"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := syncProtectedCheckout(context.Background(), dir, "refs/heads/main", oldSHA); err != nil {
		t.Fatalf("an untracked file outside the landing must not block the sync: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "tracked.txt"))
	if err != nil || string(got) != "v2" {
		t.Fatalf("landed path not advanced: %q err=%v", got, err)
	}
	// No staged reversal of the landing is left behind.
	if staged := git(t, dir, "diff", "--cached", "--stat", "HEAD"); strings.TrimSpace(staged) != "" {
		t.Fatalf("sync left a staged reversal of the landing: %s", staged)
	}
	surprise, err := os.ReadFile(filepath.Join(dir, "surprise.txt"))
	if err != nil || string(surprise) != "surprise" {
		t.Fatalf("untracked file outside the landing was disturbed: %q err=%v", surprise, err)
	}
}

// TestWoeSyncProtectedCheckoutToleratesWIPPreservationsOwnSkippedFiles pins
// the fix in bb7c3e378: a path left behind by WIP preservation because it could
// not be read at all (so it was skipped rather than captured) must not make the
// sync guard misfire and refuse the sync finalization needs. The sync must still
// proceed and the skipped file must survive it untouched.
//
// The tolerance is now structural rather than an explicit tolerate-list: a
// scoped sync only consults the paths the landing changed, so a skipped path
// elsewhere is unreachable by construction. A skipped path that the landing DID
// change is a named collision instead — never silently overwritten — because a
// file that could not be read was never captured onto a preserved branch and so
// has no copy to recover from.
func TestWoeSyncProtectedCheckoutToleratesWIPPreservationsOwnSkippedFiles(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions; this test needs a non-root process")
	}
	dir := wipRepo(t)
	oldSHA := git(t, dir, "rev-parse", "HEAD")
	woeMoveRefToNewTree(t, dir, "tracked.txt", "v2", "advance")
	unreadable := filepath.Join(dir, "surprise.txt")
	if err := os.WriteFile(unreadable, []byte("surprise"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Genuinely unreadable, which is what "skipped" means in PreserveWIP.
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o644) })

	if _, err := syncProtectedCheckout(context.Background(), dir, "refs/heads/main", oldSHA); err != nil {
		t.Fatalf("a skipped (unreadable) path outside the landing must permit the sync, got err=%v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "tracked.txt"))
	if err != nil || string(got) != "v2" {
		t.Fatalf("sync did not advance the checkout to the new tip: %q err=%v", got, err)
	}
	info, err := os.Lstat(unreadable)
	if err != nil {
		t.Fatalf("skipped file should survive the sync: %v", err)
	}
	if info.Mode().Perm() != 0o000 {
		t.Fatalf("skipped file mode changed: %#o", info.Mode().Perm())
	}
}

// ---------------------------------------------------------------------------
// Operator verbs: Approve / Unapprove
// ---------------------------------------------------------------------------

func TestWoeApproveRejectsEmptyID(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	if _, err := store.Approve(ApprovalOp{ID: "   "}); err == nil || !strings.Contains(err.Error(), "requires a candidate id") {
		t.Fatalf("err=%v", err)
	}
}

// TestWoeApproveSkipsNonMatchingCandidatesBeforeFindingTarget covers the
// mutate loop's `continue` branch: approving the second of two candidates
// must not stop (or misfire) at the first non-matching one.
func TestWoeApproveSkipsNonMatchingCandidatesBeforeFindingTarget(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	shaA, shaB := strings.Repeat("a", 40), strings.Repeat("b", 40)
	first, err := store.Submit(Submit{Branch: "agent/a", SHA: shaA, Receipt: testReceipt(t, shaA), FinalizationPolicy: StewardReviewFinalization, ManifestDigest: "sha256:manifest-a"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Submit(Submit{Branch: "agent/b", SHA: shaB, Receipt: testReceipt(t, shaB), FinalizationPolicy: StewardReviewFinalization, ManifestDigest: "sha256:manifest-b"})
	if err != nil {
		t.Fatal(err)
	}
	deps := ProcessDeps{Integration: specIntegration(), Gate: passingGate{}, GateVersion: "test-gate"}
	if _, err := store.Process(context.Background(), deps); err != nil {
		t.Fatal(err)
	}
	if mustGet(t, store, first.ID).phase() != AwaitingApproval {
		t.Fatalf("precondition: first candidate not prepared: %s", mustGet(t, store, first.ID).phase())
	}
	prepared := mustGet(t, store, second.ID)
	if prepared.phase() != AwaitingApproval {
		t.Fatalf("precondition: second candidate not prepared: %s", prepared.phase())
	}

	approved, err := store.Approve(ApprovalOp{ID: second.ID, Actor: "brad", ManifestDigest: prepared.ManifestDigest})
	if err != nil {
		t.Fatal(err)
	}
	if approved.ID != second.ID || approved.phase() != ReadyToFinalize {
		t.Fatalf("approve did not reach the second candidate past the first: %#v", approved)
	}
	// The first candidate (skipped over in the loop) must be untouched.
	if mustGet(t, store, first.ID).phase() != AwaitingApproval {
		t.Fatalf("approving the second candidate mutated the first: %#v", mustGet(t, store, first.ID))
	}
}

func TestWoeApproveRejectsNonStewardCandidate(t *testing.T) {
	store, first, _ := queuedPair(t)
	if _, err := store.Approve(ApprovalOp{ID: first.ID, ManifestDigest: "sha256:whatever"}); err == nil || !strings.Contains(err.Error(), "does not require steward approval") {
		t.Fatalf("err=%v", err)
	}
}

func TestWoeApproveRejectsCandidateNotYetAwaitingApproval(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	sha := strings.Repeat("a", 40)
	c, err := store.Submit(Submit{Branch: "agent/a", SHA: sha, Receipt: testReceipt(t, sha), FinalizationPolicy: StewardReviewFinalization, ManifestDigest: "sha256:manifest"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Approve(ApprovalOp{ID: c.ID, ManifestDigest: "sha256:manifest"}); err == nil || !strings.Contains(err.Error(), "requires awaiting_approval") {
		t.Fatalf("err=%v", err)
	}
}

func TestWoeApproveRejectsUnknownCandidate(t *testing.T) {
	store, _, _ := queuedPair(t)
	if _, err := store.Approve(ApprovalOp{ID: "queue-missing", ManifestDigest: "sha256:manifest"}); err == nil || !strings.Contains(err.Error(), "unknown candidate") {
		t.Fatalf("err=%v", err)
	}
}

// TestWoeApproveRejectsTamperedPreparedTuple covers validatePreparedTuple's
// failure surfacing through Approve: a candidate whose durable prepared
// fields were altered after entering awaiting_approval (so its dependency
// fingerprint no longer matches) must never be approved.
func TestWoeApproveRejectsTamperedPreparedTuple(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	sha := strings.Repeat("a", 40)
	c, err := store.Submit(Submit{Branch: "agent/a", SHA: sha, Receipt: testReceipt(t, sha), FinalizationPolicy: StewardReviewFinalization, ManifestDigest: "sha256:manifest"})
	if err != nil {
		t.Fatal(err)
	}
	deps := ProcessDeps{Integration: specIntegration(), Gate: passingGate{}, GateVersion: "test-gate"}
	if _, err := (Worker{Store: store, Deps: deps}).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	prepared := mustGet(t, store, c.ID)
	if prepared.phase() != AwaitingApproval {
		t.Fatalf("precondition: %s", prepared.phase())
	}
	if _, err := store.mutate(func(state *State) (Candidate, error) {
		for i := range state.Candidates {
			if state.Candidates[i].ID == c.ID {
				state.Candidates[i].DependencyFingerprint = "tampered"
				return state.Candidates[i], nil
			}
		}
		return Candidate{}, fmt.Errorf("candidate not found")
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Approve(ApprovalOp{ID: c.ID, ManifestDigest: prepared.ManifestDigest}); err == nil || !strings.Contains(err.Error(), "approve requires green current gate") {
		t.Fatalf("err=%v, want a tampered-tuple rejection", err)
	}
}

func TestWoeUnapproveRejectsNonStewardCandidate(t *testing.T) {
	store, first, _ := queuedPair(t)
	if _, err := store.Unapprove(Op{ID: first.ID, Actor: "brad"}); err == nil || !strings.Contains(err.Error(), "does not require steward approval") {
		t.Fatalf("err=%v", err)
	}
}

func TestWoeUnapproveRejectsBeforeApproval(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	sha := strings.Repeat("a", 40)
	c, err := store.Submit(Submit{Branch: "agent/a", SHA: sha, Receipt: testReceipt(t, sha), FinalizationPolicy: StewardReviewFinalization, ManifestDigest: "sha256:manifest"})
	if err != nil {
		t.Fatal(err)
	}
	deps := ProcessDeps{Integration: specIntegration(), Gate: passingGate{}, GateVersion: "test-gate"}
	if _, err := (Worker{Store: store, Deps: deps}).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if mustGet(t, store, c.ID).phase() != AwaitingApproval {
		t.Fatal("precondition: candidate not prepared")
	}
	if _, err := store.Unapprove(Op{ID: c.ID, Actor: "brad"}); err == nil || !strings.Contains(err.Error(), "requires ready_to_finalize or finalizing") {
		t.Fatalf("err=%v", err)
	}
}

// ---------------------------------------------------------------------------
// Op / ApprovalOp attribution helpers (at/actor)
// ---------------------------------------------------------------------------

// TestWoeOpAtUsesExplicitNowWhenProvided covers Op.at()'s non-zero branch:
// every existing operator-verb test omits Now, so it always defaults.
func TestWoeOpAtUsesExplicitNowWhenProvided(t *testing.T) {
	store, first, _ := queuedPair(t)
	drain(t, store, ProcessDeps{Integration: specIntegration(), Gate: failingGate()})
	fixed := time.Date(2030, 3, 4, 5, 6, 7, 0, time.UTC)

	kicked, err := store.Kick(Op{ID: first.ID, Actor: "brad", Now: fixed})
	if err != nil {
		t.Fatal(err)
	}
	want := "queue:kick by brad at " + fixed.Format(time.RFC3339)
	if !hasEvidence(kicked, want) {
		t.Fatalf("evidence=%v, want an entry containing %q", kicked.Evidence, want)
	}
}

// TestWoeOpActorDefaultsToOperatorWhenBlank covers Op.actor()'s blank-actor
// default branch.
func TestWoeOpActorDefaultsToOperatorWhenBlank(t *testing.T) {
	store, first, _ := queuedPair(t)
	drain(t, store, ProcessDeps{Integration: specIntegration(), Gate: failingGate()})

	kicked, err := store.Kick(Op{ID: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !hasEvidence(kicked, "queue:kick by operator") {
		t.Fatalf("evidence=%v, want the default actor 'operator'", kicked.Evidence)
	}
}

// TestWoeApprovalOpAtUsesExplicitNowWhenProvided covers ApprovalOp.at()'s
// non-zero branch, and durably pins that the recorded Approval.At reflects
// the supplied attribution time rather than wall-clock now.
func TestWoeApprovalOpAtUsesExplicitNowWhenProvided(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	sha := strings.Repeat("a", 40)
	c, err := store.Submit(Submit{Branch: "agent/a", SHA: sha, Receipt: testReceipt(t, sha), FinalizationPolicy: StewardReviewFinalization, ManifestDigest: "sha256:manifest"})
	if err != nil {
		t.Fatal(err)
	}
	deps := ProcessDeps{Integration: specIntegration(), Gate: passingGate{}, GateVersion: "test-gate"}
	if _, err := (Worker{Store: store, Deps: deps}).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	prepared := mustGet(t, store, c.ID)
	fixed := time.Date(2030, 3, 4, 5, 6, 7, 0, time.UTC)

	approved, err := store.Approve(ApprovalOp{ID: c.ID, ManifestDigest: prepared.ManifestDigest, Now: fixed})
	if err != nil {
		t.Fatal(err)
	}
	if approved.Approval == nil || !approved.Approval.At.Equal(fixed.UTC()) {
		t.Fatalf("approval.At=%v, want %v", approved.Approval, fixed)
	}
}

// ---------------------------------------------------------------------------
// Kick / MarkEmergency guard branches
// ---------------------------------------------------------------------------

func TestWoeKickRejectsCandidateNotInRetryWait(t *testing.T) {
	store, first, _ := queuedPair(t)
	if _, err := store.Kick(Op{ID: first.ID, Actor: "brad"}); err == nil || !strings.Contains(err.Error(), "requires retry_wait") {
		t.Fatalf("err=%v", err)
	}
}

func TestWoeMarkEmergencyRejectsTerminalCandidate(t *testing.T) {
	store, first, _ := queuedPair(t)
	if _, err := store.Reject(Op{ID: first.ID, Actor: "brad"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkEmergency(Op{ID: first.ID, Actor: "brad"}); err == nil || !strings.Contains(err.Error(), "cannot mark") {
		t.Fatalf("err=%v", err)
	}
}

// ---------------------------------------------------------------------------
// Get / operate
// ---------------------------------------------------------------------------

func TestWoeGetRejectsUnknownID(t *testing.T) {
	store, _, _ := queuedPair(t)
	if _, err := store.Get("queue-missing"); err == nil || !strings.Contains(err.Error(), "unknown candidate") {
		t.Fatalf("err=%v", err)
	}
}

// TestWoeGetSurfacesCorruptStateFileError covers Get's readState-error
// branch: a durable state file that fails to parse must surface as an error,
// not a false "unknown candidate".
func TestWoeGetSurfacesCorruptStateFileError(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	dir, path, err := store.paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Get("anything"); err == nil {
		t.Fatal("expected Get to surface the corrupt state file parse error")
	}
}

func TestWoeOperateRejectsEmptyCandidateID(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	if _, err := store.Kick(Op{ID: "   ", Actor: "brad"}); err == nil || !strings.Contains(err.Error(), "requires a candidate id") {
		t.Fatalf("err=%v", err)
	}
}
