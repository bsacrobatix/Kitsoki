package gitbackup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/objectstore"
)

// Ad hoc git repos are deliberate here: the behavior under test is the git
// command sequence itself (bundle chains, force-pushes, ref surgery), which
// the hermetic capsule fixtures do not exercise.

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=gitbackup-test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=gitbackup-test", "GIT_COMMITTER_EMAIL=test@example.invalid",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "--quiet", "-b", "main")
	return dir
}

func commitFile(t *testing.T, repo, name, content, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", name)
	gitRun(t, repo, "commit", "--quiet", "-m", message)
}

func forEachRef(t *testing.T, repo string) string {
	t.Helper()
	return gitRun(t, repo, "for-each-ref", "--format=%(objectname) %(refname)", "refs/heads", "refs/tags")
}

func openBacker(t *testing.T, store objectstore.Store, repo string) *Backer {
	t.Helper()
	b, err := Open(store, "repos/demo", repo)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// backupIncr runs BackupIncremental and asserts it produced an entry of the
// wanted kind (the first backup auto-promotes to KindFull).
func backupIncr(t *testing.T, ctx context.Context, b *Backer, wantKind string) Result {
	t.Helper()
	res, err := b.BackupIncremental(ctx)
	return mustBackup(t, res, err, wantKind)
}

func mustBackupFull(t *testing.T, ctx context.Context, b *Backer, wantKind string) Result {
	t.Helper()
	res, err := b.BackupFull(ctx)
	return mustBackup(t, res, err, wantKind)
}

func mustBackup(t *testing.T, res Result, err error, wantKind string) Result {
	t.Helper()
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if res.Skipped {
		t.Fatalf("backup unexpectedly skipped (wanted %s)", wantKind)
	}
	if res.Entry.Kind != wantKind {
		t.Fatalf("backup kind = %q, want %q", res.Entry.Kind, wantKind)
	}
	return res
}

func restoreAndCompare(t *testing.T, store objectstore.Store, source string) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), "restored")
	if _, err := Restore(context.Background(), store, "repos/demo", target); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got, want := forEachRef(t, target), forEachRef(t, source); got != want {
		t.Fatalf("restored refs mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
	gitRun(t, target, "fsck", "--no-progress")
	return target
}

func TestIncrementalChainRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := objectstore.NewFake()
	repo := newRepo(t)
	b := openBacker(t, store, repo)

	commitFile(t, repo, "a.txt", "one", "c1")
	res := backupIncr(t, ctx, b, KindFull)
	if res.Entry.Seq != 1 {
		t.Fatalf("first entry seq = %d, want 1", res.Entry.Seq)
	}

	// New commits, a new branch, and tags (lightweight + annotated).
	commitFile(t, repo, "a.txt", "two", "c2")
	gitRun(t, repo, "branch", "feature")
	gitRun(t, repo, "tag", "light")
	gitRun(t, repo, "tag", "-a", "-m", "annotated", "heavy")
	incr := backupIncr(t, ctx, b, KindIncr)
	if incr.Entry.Seq != 2 {
		t.Fatalf("second entry seq = %d, want 2", incr.Entry.Seq)
	}
	if !strings.Contains(incr.Entry.Key, "-incr-") {
		t.Fatalf("incremental key %q missing -incr-", incr.Entry.Key)
	}

	commitFile(t, repo, "b.txt", "three", "c3")
	backupIncr(t, ctx, b, KindIncr)

	restoreAndCompare(t, store, repo)
}

func TestNoChangeIsNoOp(t *testing.T) {
	ctx := context.Background()
	store := objectstore.NewFake()
	repo := newRepo(t)
	commitFile(t, repo, "a.txt", "one", "c1")
	b := openBacker(t, store, repo)
	first := backupIncr(t, ctx, b, KindFull)

	res, err := b.BackupIncremental(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skipped {
		t.Fatal("second backup with no changes should be skipped")
	}
	if res.Manifest.Generation != first.Manifest.Generation {
		t.Fatal("skipped backup must not advance the manifest generation")
	}
}

func TestForcePushDivergentHistory(t *testing.T) {
	ctx := context.Background()
	store := objectstore.NewFake()
	repo := newRepo(t)
	commitFile(t, repo, "a.txt", "one", "c1")
	commitFile(t, repo, "a.txt", "two", "c2")
	b := openBacker(t, store, repo)
	backupIncr(t, ctx, b, KindFull)

	// Non-fast-forward rewrite: drop c2, commit divergent c2'.
	gitRun(t, repo, "reset", "--quiet", "--hard", "HEAD~1")
	commitFile(t, repo, "a.txt", "two-rewritten", "c2'")
	backupIncr(t, ctx, b, KindIncr)

	restoreAndCompare(t, store, repo)
}

func TestForcePushRewindToAncestorRecordsRefsEntry(t *testing.T) {
	ctx := context.Background()
	store := objectstore.NewFake()
	repo := newRepo(t)
	commitFile(t, repo, "a.txt", "one", "c1")
	commitFile(t, repo, "a.txt", "two", "c2")
	b := openBacker(t, store, repo)
	backupIncr(t, ctx, b, KindFull)

	// Rewind main to c1: no new objects exist, so the increment must be a
	// bundle-less refs entry.
	gitRun(t, repo, "reset", "--quiet", "--hard", "HEAD~1")
	res := backupIncr(t, ctx, b, KindRefs)
	if res.Entry.Key != "" || res.Entry.Size != 0 {
		t.Fatalf("refs entry must not carry a bundle: %+v", res.Entry)
	}

	restoreAndCompare(t, store, repo)
}

func TestRefAndTagDeletion(t *testing.T) {
	ctx := context.Background()
	store := objectstore.NewFake()
	repo := newRepo(t)
	commitFile(t, repo, "a.txt", "one", "c1")
	gitRun(t, repo, "branch", "doomed")
	gitRun(t, repo, "tag", "gone")
	b := openBacker(t, store, repo)
	backupIncr(t, ctx, b, KindFull)

	gitRun(t, repo, "branch", "-D", "doomed")
	gitRun(t, repo, "tag", "-d", "gone")
	backupIncr(t, ctx, b, KindRefs)

	target := restoreAndCompare(t, store, repo)
	if refs := forEachRef(t, target); strings.Contains(refs, "doomed") || strings.Contains(refs, "gone") {
		t.Fatalf("deleted refs survived restore:\n%s", refs)
	}
}

func TestPrunedBasisFallsBackToFull(t *testing.T) {
	ctx := context.Background()
	store := objectstore.NewFake()
	repo := newRepo(t)
	commitFile(t, repo, "a.txt", "one", "c1")
	b := openBacker(t, store, repo)
	backupIncr(t, ctx, b, KindFull)

	// Replace main with an unrelated root and prune the old history so the
	// recorded basis commit no longer exists locally.
	gitRun(t, repo, "checkout", "--quiet", "--orphan", "rebooted")
	commitFile(t, repo, "b.txt", "new world", "r1")
	gitRun(t, repo, "branch", "-M", "rebooted", "main")
	gitRun(t, repo, "reflog", "expire", "--expire=now", "--all")
	gitRun(t, repo, "gc", "--quiet", "--prune=now", "--aggressive")

	res := backupIncr(t, ctx, b, KindFull)
	if res.Entry.Seq != 2 {
		t.Fatalf("fallback full seq = %d, want 2", res.Entry.Seq)
	}
	restoreAndCompare(t, store, repo)
}

func TestFullBackupIsCompactionPoint(t *testing.T) {
	ctx := context.Background()
	store := objectstore.NewFake()
	repo := newRepo(t)
	b := openBacker(t, store, repo)

	commitFile(t, repo, "a.txt", "one", "c1")
	first := backupIncr(t, ctx, b, KindFull)
	commitFile(t, repo, "a.txt", "two", "c2")
	second := backupIncr(t, ctx, b, KindIncr)
	commitFile(t, repo, "a.txt", "three", "c3")
	full := mustBackupFull(t, ctx, b, KindFull)
	commitFile(t, repo, "a.txt", "four", "c4")
	backupIncr(t, ctx, b, KindIncr)

	// Everything before the newest full backup must be irrelevant to
	// restore: delete those bundle objects outright.
	for _, key := range []string{first.Entry.Key, second.Entry.Key} {
		if err := store.Delete(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	if full.Entry.Seq != 3 {
		t.Fatalf("full entry seq = %d, want 3", full.Entry.Seq)
	}
	restoreAndCompare(t, store, repo)
}

func TestEmptyRepo(t *testing.T) {
	ctx := context.Background()
	store := objectstore.NewFake()
	repo := newRepo(t)
	b := openBacker(t, store, repo)

	res, err := b.BackupIncremental(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skipped {
		t.Fatal("backup of an empty repository should be skipped")
	}
	if res, err := b.BackupFull(ctx); err != nil || !res.Skipped {
		t.Fatalf("full backup of empty repository: res=%+v err=%v", res, err)
	}
	if _, err := Restore(ctx, store, "repos/demo", filepath.Join(t.TempDir(), "restored")); !errors.Is(err, ErrNoBackup) {
		t.Fatalf("restore without backups: err = %v, want ErrNoBackup", err)
	}
}

func TestRestoreRefusesNonEmptyTarget(t *testing.T) {
	ctx := context.Background()
	store := objectstore.NewFake()
	repo := newRepo(t)
	commitFile(t, repo, "a.txt", "one", "c1")
	b := openBacker(t, store, repo)
	backupIncr(t, ctx, b, KindFull)

	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "precious.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(ctx, store, "repos/demo", target); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("restore onto non-empty dir: err = %v, want refusal", err)
	}
}

func TestTamperedManifestRefusesRestore(t *testing.T) {
	ctx := context.Background()
	store := objectstore.NewFake()
	repo := newRepo(t)
	commitFile(t, repo, "a.txt", "one", "c1")
	b := openBacker(t, store, repo)
	backupIncr(t, ctx, b, KindFull)

	tamper := func(t *testing.T, mutate func(*Entry)) error {
		t.Helper()
		body, _, err := store.Get(ctx, "repos/demo/manifest.json")
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(body)
		body.Close()
		if err != nil {
			t.Fatal(err)
		}
		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		mutate(&m.Entries[len(m.Entries)-1])
		out, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Put(ctx, "repos/demo/manifest.json", bytes.NewReader(out), int64(len(out)), objectstore.PutOptions{}); err != nil {
			t.Fatal(err)
		}
		_, restoreErr := Restore(ctx, store, "repos/demo", filepath.Join(t.TempDir(), "restored"))
		return restoreErr
	}

	if err := tamper(t, func(e *Entry) { e.Size++ }); err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("tampered size: err = %v, want size mismatch refusal", err)
	}
	if err := tamper(t, func(e *Entry) { e.Size--; e.SHA256 = strings.Repeat("0", 64) }); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("tampered sha256: err = %v, want digest refusal", err)
	}
}

func TestConcurrentManifestWriteDetected(t *testing.T) {
	ctx := context.Background()
	store := objectstore.NewFake()
	repo := newRepo(t)
	commitFile(t, repo, "a.txt", "one", "c1")
	b := openBacker(t, store, repo)
	backupIncr(t, ctx, b, KindFull)
	commitFile(t, repo, "a.txt", "two", "c2")

	// Simulate a racing writer advancing the generation between plan and
	// write.
	if _, err := putManifest(ctx, store, "repos/demo", 1, Entry{Seq: 2, Kind: KindRefs, Refs: map[string]string{}}, b.now()); err != nil {
		t.Fatal(err)
	}
	manifest, err := loadManifest(ctx, store, "repos/demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := putManifest(ctx, store, "repos/demo", manifest.Generation-1, Entry{Seq: 3, Kind: KindRefs, Refs: map[string]string{}}, b.now()); !errors.Is(err, ErrConcurrentUpdate) {
		t.Fatalf("stale-generation write: err = %v, want ErrConcurrentUpdate", err)
	}
}

func TestOpenRejectsNonRepo(t *testing.T) {
	if _, err := Open(objectstore.NewFake(), "repos/demo", t.TempDir()); err == nil {
		t.Fatal("Open on a non-repository directory should fail")
	}
	if _, err := Open(objectstore.NewFake(), "", newRepo(t)); err == nil {
		t.Fatal("Open with empty prefix should fail")
	}
}

func TestManifestChainValidation(t *testing.T) {
	m := Manifest{Schema: ManifestSchema, Entries: []Entry{{Seq: 1, Kind: KindIncr, Key: "k", Size: 1, SHA256: "x"}}}
	if err := validateManifest(m); err == nil || !strings.Contains(err.Error(), "start with a full") {
		t.Fatalf("chain starting with incr: err = %v", err)
	}
	m = Manifest{Schema: ManifestSchema, Entries: []Entry{{Seq: 2, Kind: KindFull, Key: "k", Size: 1, SHA256: "x"}}}
	if err := validateManifest(m); err == nil || !strings.Contains(err.Error(), "chain broken") {
		t.Fatalf("non-contiguous seq: err = %v", err)
	}
	if err := validateManifest(Manifest{Schema: "bogus/v9"}); err == nil {
		t.Fatal("unknown schema must be rejected")
	}
}

func TestRestoreSetsHEADToRestoredBranch(t *testing.T) {
	ctx := context.Background()
	store := objectstore.NewFake()
	repo := newRepo(t)
	commitFile(t, repo, "a.txt", "one", "c1")
	b := openBacker(t, store, repo)
	backupIncr(t, ctx, b, KindFull)

	target := restoreAndCompare(t, store, repo)
	head := strings.TrimSpace(gitRun(t, target, "symbolic-ref", "HEAD"))
	if head != "refs/heads/main" {
		t.Fatalf("restored HEAD = %q, want refs/heads/main", head)
	}
}
