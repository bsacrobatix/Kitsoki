package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/gitbackup"
	"kitsoki/internal/objectstore"
)

// runRepo executes the repo command tree in isolation with the given args,
// returning combined stdout/stderr (same pattern as runVmp).
func runRepo(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := repoCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// repoWithFakeStore points repoNewObjectStore at a shared in-memory fake for
// the duration of the test, regardless of bucket URL or credentials.
func repoWithFakeStore(t *testing.T) *objectstore.Fake {
	t.Helper()
	fake := objectstore.NewFake()
	orig := repoNewObjectStore
	repoNewObjectStore = func(bucketURL, keyEnv, secretEnv string) (objectstore.Store, error) {
		return fake, nil
	}
	t.Cleanup(func() { repoNewObjectStore = orig })
	return fake
}

// repoGit runs git in dir with deterministic identity, failing the test on
// error. These ad hoc t.TempDir repos are the subject matter here (the git
// command sequence over HTTP is what is under test), not a reusable fixture.
func repoGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=repo-test", "GIT_AUTHOR_EMAIL=repo@test.invalid",
		"GIT_COMMITTER_NAME=repo-test", "GIT_COMMITTER_EMAIL=repo@test.invalid",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
	return string(out)
}

// repoCommit writes content to a file and commits it.
func repoCommit(t *testing.T, dir, file, content, message string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644))
	repoGit(t, dir, "add", file)
	repoGit(t, dir, "commit", "-m", message)
}

// repoRefs returns the for-each-ref snapshot used for restore comparisons.
func repoRefs(t *testing.T, dir string) string {
	t.Helper()
	return repoGit(t, dir, "for-each-ref", "--format=%(refname) %(objectname)")
}

const repoTestBucketURL = "https://bucket.region.example.invalid"

func TestRepoInitCreatesBareRepo(t *testing.T) {
	root := t.TempDir()
	out, err := runRepo(t, "init", "--root", root, "--name", "team/project")
	require.NoError(t, err)
	dir := strings.TrimSpace(out)
	require.Equal(t, filepath.Join(root, "team", "project.git"), dir)
	require.Equal(t, "true\n", repoGit(t, dir, "rev-parse", "--is-bare-repository"))

	// Refuses overwrite and unsafe names.
	_, err = runRepo(t, "init", "--root", root, "--name", "team/project")
	require.ErrorContains(t, err, "already exists")
	_, err = runRepo(t, "init", "--root", root, "--name", "../escape")
	require.Error(t, err)
}

func TestRepoBackupRestoreRoundTrip(t *testing.T) {
	repoWithFakeStore(t)
	work := t.TempDir()
	repoGit(t, work, "init", "-b", "main", ".")
	repoCommit(t, work, "a.txt", "one", "first")

	out, err := runRepo(t, "backup", "--repo", work, "--prefix", "chains/work",
		"--bucket-url", repoTestBucketURL)
	require.NoError(t, err)
	var first map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &first))
	require.Equal(t, gitbackup.KindFull, first["kind"], "first backup is an automatic full")

	repoCommit(t, work, "a.txt", "two", "second")
	repoGit(t, work, "tag", "v1")
	out, err = runRepo(t, "backup", "--repo", work, "--prefix", "chains/work",
		"--bucket-url", repoTestBucketURL)
	require.NoError(t, err)
	var second map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &second))
	require.Equal(t, gitbackup.KindIncr, second["kind"])
	require.Equal(t, float64(2), second["entries"])

	// Unchanged repo: skipped, no new entry.
	out, err = runRepo(t, "backup", "--repo", work, "--prefix", "chains/work",
		"--bucket-url", repoTestBucketURL)
	require.NoError(t, err)
	var third map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &third))
	require.Equal(t, true, third["skipped"])

	target := filepath.Join(t.TempDir(), "restored")
	out, err = runRepo(t, "restore", "--prefix", "chains/work", "--target", target,
		"--bucket-url", repoTestBucketURL)
	require.NoError(t, err)
	var restored map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &restored))
	require.Equal(t, true, restored["ok"])
	require.Equal(t, repoRefs(t, work), repoRefs(t, target))
}

func TestRepoBackupFullFlagForcesCompactionPoint(t *testing.T) {
	repoWithFakeStore(t)
	work := t.TempDir()
	repoGit(t, work, "init", "-b", "main", ".")
	repoCommit(t, work, "a.txt", "one", "first")

	_, err := runRepo(t, "backup", "--repo", work, "--prefix", "p",
		"--bucket-url", repoTestBucketURL)
	require.NoError(t, err)
	repoCommit(t, work, "a.txt", "two", "second")
	out, err := runRepo(t, "backup", "--repo", work, "--prefix", "p", "--full",
		"--bucket-url", repoTestBucketURL)
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.Equal(t, gitbackup.KindFull, result["kind"])
}

func TestRepoBackupRequiresBucketURL(t *testing.T) {
	_, err := runRepo(t, "backup", "--repo", t.TempDir(), "--prefix", "p")
	require.ErrorContains(t, err, "bucket-url")
}

func TestRepoServeFlagValidation(t *testing.T) {
	root := t.TempDir()
	_, err := runRepo(t, "serve", "--root", root, "--backup")
	require.ErrorContains(t, err, "--backup requires --bucket-url")

	repoWithFakeStore(t)
	_, err = runRepo(t, "serve", "--root", root, "--backup", "--read-only",
		"--bucket-url", repoTestBucketURL)
	require.ErrorContains(t, err, "read-only")
}

// TestRepoServeBackupEndToEnd is the acceptance test: serve a root with
// backup enabled through the real CLI command, push over HTTP twice, watch
// the incremental backups land in the fake object store, then restore into a
// fresh directory and require an identical for-each-ref snapshot.
func TestRepoServeBackupEndToEnd(t *testing.T) {
	fake := repoWithFakeStore(t)
	root := t.TempDir()
	_, err := runRepo(t, "init", "--root", root, "--name", "team/project")
	require.NoError(t, err)
	bare := filepath.Join(root, "team", "project.git")

	ready := make(chan string, 1)
	origReady := repoServeOnReady
	repoServeOnReady = func(addr string) { ready <- addr }
	t.Cleanup(func() { repoServeOnReady = origReady })

	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	cmd := repoCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"serve", "--root", root, "--addr", "127.0.0.1:0",
		"--backup", "--backup-prefix", "repos/", "--bucket-url", repoTestBucketURL})
	go func() { serveDone <- cmd.ExecuteContext(ctx) }()
	var addr string
	select {
	case addr = <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not become ready")
	}

	manifestEntries := func() int {
		body, _, err := fake.Get(context.Background(), "repos/team/project.git/manifest.json")
		if err != nil {
			return 0
		}
		defer body.Close()
		var m gitbackup.Manifest
		if json.NewDecoder(body).Decode(&m) != nil {
			return 0
		}
		return len(m.Entries)
	}
	waitEntries := func(n int) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for manifestEntries() < n {
			if time.Now().After(deadline) {
				t.Fatalf("backup chain never reached %d entries (have %d)", n, manifestEntries())
			}
			time.Sleep(25 * time.Millisecond)
		}
	}

	work := t.TempDir()
	repoGit(t, work, "init", "-b", "main", ".")
	repoCommit(t, work, "a.txt", "one", "first")
	remote := fmt.Sprintf("http://%s/team/project.git", addr)
	repoGit(t, work, "push", remote, "main")
	waitEntries(1) // first push: automatic full backup

	repoCommit(t, work, "a.txt", "two", "second")
	repoGit(t, work, "tag", "v1")
	repoGit(t, work, "push", remote, "main", "v1")
	waitEntries(2) // second push: incremental on top of the full

	cancel()
	select {
	case err := <-serveDone:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not shut down")
	}

	target := filepath.Join(t.TempDir(), "restored")
	_, err = runRepo(t, "restore", "--prefix", "repos/team/project.git",
		"--target", target, "--bucket-url", repoTestBucketURL)
	require.NoError(t, err)
	require.Equal(t, repoRefs(t, bare), repoRefs(t, target))
	require.NotEmpty(t, repoRefs(t, target))
}

func TestRepoBackupQueueSerializesAndCoalesces(t *testing.T) {
	var mu sync.Mutex
	runs := map[string]int{}
	inFlight := map[string]int{}
	release := make(chan struct{})
	queue := newRepoBackupQueue(func(ctx context.Context, repo string) error {
		mu.Lock()
		runs[repo]++
		inFlight[repo]++
		require.Equal(t, 1, inFlight[repo], "backups for one repo must be serialized")
		mu.Unlock()
		<-release
		mu.Lock()
		inFlight[repo]--
		mu.Unlock()
		if repo == "b.git" {
			return fmt.Errorf("synthetic failure") // must be logged, not fatal
		}
		return nil
	}, func(string, ...any) {})

	queue.Notify("a.git")
	// Burst while a.git's first run blocks: coalesces to one follow-up.
	queue.Notify("a.git")
	queue.Notify("a.git")
	queue.Notify("a.git")
	queue.Notify("b.git")
	close(release)
	queue.Wait()

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 2, runs["a.git"], "burst of 3 extra notifies coalesces into one follow-up run")
	require.Equal(t, 1, runs["b.git"])
}
