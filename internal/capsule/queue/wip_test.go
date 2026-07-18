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
	branch, err := PreserveWIP(context.Background(), dir, time.Now())
	if err != nil || branch != "" {
		t.Fatalf("branch=%q err=%v", branch, err)
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

	branch, err := PreserveWIP(context.Background(), dir, time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(branch, "queue/preserved-wip/") {
		t.Fatalf("branch=%q", branch)
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
	first, err := PreserveWIP(context.Background(), dir, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "two.txt"), []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := PreserveWIP(context.Background(), dir, at) // same stamp on purpose
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
