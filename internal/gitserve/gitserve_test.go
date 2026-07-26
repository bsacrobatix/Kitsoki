package gitserve

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// runGit runs a git command for test setup with a scrubbed environment so the
// developer's global/system git config (credential helpers, hooks, prompts)
// cannot influence the test. These ad hoc repos are the subject matter here —
// the behavior under test is the git smart-HTTP command sequence itself.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	argv := append([]string{
		"-c", "user.name=gitserve-test",
		"-c", "user.email=gitserve-test@example.invalid",
		"-c", "protocol.version=2",
	}, args...)
	cmd := exec.Command("git", argv...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"HOME="+t.TempDir(),
		"XDG_CONFIG_HOME="+t.TempDir(),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// runGitErr is runGit for commands that are expected to fail.
func runGitErr(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"HOME="+t.TempDir(),
		"XDG_CONFIG_HOME="+t.TempDir(),
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// seedBareRepo creates root/<name> as a bare repo containing one commit on
// main and returns the commit SHA.
func seedBareRepo(t *testing.T, root, name string) string {
	t.Helper()
	bare, err := InitBare(root, name)
	if err != nil {
		t.Fatalf("InitBare: %v", err)
	}
	work := t.TempDir()
	runGit(t, work, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "README.md")
	runGit(t, work, "commit", "-m", "seed")
	runGit(t, work, "push", bare, "main:main")
	return runGit(t, work, "rev-parse", "HEAD")
}

type hookRecorder struct {
	mu    sync.Mutex
	calls []hookCall
}

type hookCall struct {
	repo    string
	updates []RefUpdate
}

func (h *hookRecorder) OnRefsChanged(repo string, updates []RefUpdate) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, hookCall{repo: repo, updates: updates})
}

func (h *hookRecorder) snapshot() []hookCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]hookCall(nil), h.calls...)
}

func newServer(t *testing.T, root string, opts ...Option) *httptest.Server {
	t.Helper()
	handler, err := New(root, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestCloneOverHTTP(t *testing.T) {
	root := t.TempDir()
	head := seedBareRepo(t, root, "r.git")
	srv := newServer(t, root)

	dst := filepath.Join(t.TempDir(), "clone")
	runGit(t, t.TempDir(), "clone", srv.URL+"/r.git", dst)
	if got := runGit(t, dst, "rev-parse", "HEAD"); got != head {
		t.Fatalf("cloned HEAD = %s, want %s", got, head)
	}
}

func TestPushUpdatesRefsAndFiresRefHook(t *testing.T) {
	root := t.TempDir()
	head := seedBareRepo(t, root, "r.git")
	hook := &hookRecorder{}
	srv := newServer(t, root, WithRefHook(hook))

	dst := filepath.Join(t.TempDir(), "clone")
	runGit(t, t.TempDir(), "clone", srv.URL+"/r.git", dst)
	if err := os.WriteFile(filepath.Join(dst, "next.txt"), []byte("next\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dst, "add", "next.txt")
	runGit(t, dst, "commit", "-m", "next")
	newHead := runGit(t, dst, "rev-parse", "HEAD")
	runGit(t, dst, "push", "origin", "main")

	if got := runGit(t, filepath.Join(root, "r.git"), "rev-parse", "refs/heads/main"); got != newHead {
		t.Fatalf("bare main = %s, want %s", got, newHead)
	}
	calls := hook.snapshot()
	if len(calls) != 1 {
		t.Fatalf("hook calls = %d, want 1: %+v", len(calls), calls)
	}
	if calls[0].repo != "r.git" {
		t.Fatalf("hook repo = %q, want r.git", calls[0].repo)
	}
	want := RefUpdate{Ref: "refs/heads/main", Old: head, New: newHead}
	if len(calls[0].updates) != 1 || calls[0].updates[0] != want {
		t.Fatalf("hook updates = %+v, want [%+v]", calls[0].updates, want)
	}

	// Pushing a brand-new branch reports the all-zero old OID.
	runGit(t, dst, "push", "origin", "main:refs/heads/feature")
	calls = hook.snapshot()
	if len(calls) != 2 {
		t.Fatalf("hook calls after branch push = %d, want 2: %+v", len(calls), calls)
	}
	wantBranch := RefUpdate{Ref: "refs/heads/feature", Old: zeroOID(len(newHead)), New: newHead}
	if len(calls[1].updates) != 1 || calls[1].updates[0] != wantBranch {
		t.Fatalf("branch hook updates = %+v, want [%+v]", calls[1].updates, wantBranch)
	}
}

// TestLargePushUsesChunkedEncoding proves pushes bigger than git's
// http.postBuffer (1 MiB default) succeed. git switches to
// Transfer-Encoding: chunked for such bodies, which net/http/cgi rejects with
// HTTP 400 unless the handler de-chunks first (regression: deChunkBody).
func TestLargePushUsesChunkedEncoding(t *testing.T) {
	root := t.TempDir()
	seedBareRepo(t, root, "r.git")
	hook := &hookRecorder{}
	srv := newServer(t, root, WithRefHook(hook))

	dst := filepath.Join(t.TempDir(), "clone")
	runGit(t, t.TempDir(), "clone", srv.URL+"/r.git", dst)
	// Incompressible payload so the pack (and thus the POST body) stays well
	// above http.postBuffer and forces chunked transfer encoding.
	payload := make([]byte, 4<<20)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "blob.bin"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dst, "add", "blob.bin")
	runGit(t, dst, "commit", "-m", "large")
	newHead := runGit(t, dst, "rev-parse", "HEAD")
	runGit(t, dst, "push", "origin", "main")

	if got := runGit(t, filepath.Join(root, "r.git"), "rev-parse", "refs/heads/main"); got != newHead {
		t.Fatalf("bare main = %s, want %s (large push did not land)", got, newHead)
	}
	if calls := hook.snapshot(); len(calls) != 1 {
		t.Fatalf("hook calls = %d, want 1: %+v", len(calls), calls)
	}
}

func TestReadOnlyRejectsPush(t *testing.T) {
	root := t.TempDir()
	head := seedBareRepo(t, root, "r.git")
	hook := &hookRecorder{}
	srv := newServer(t, root, WithReadOnly(), WithRefHook(hook))

	dst := filepath.Join(t.TempDir(), "clone")
	runGit(t, t.TempDir(), "clone", srv.URL+"/r.git", dst)
	if err := os.WriteFile(filepath.Join(dst, "blocked.txt"), []byte("no\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dst, "add", "blocked.txt")
	runGit(t, dst, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-m", "blocked")

	out, err := runGitErr(t, dst, "push", "origin", "main")
	if err == nil {
		t.Fatalf("push succeeded in read-only mode:\n%s", out)
	}
	if !strings.Contains(out, "read-only") {
		t.Fatalf("push error does not mention read-only:\n%s", out)
	}
	if got := runGit(t, filepath.Join(root, "r.git"), "rev-parse", "refs/heads/main"); got != head {
		t.Fatalf("bare main moved to %s in read-only mode, want %s", got, head)
	}
	if calls := hook.snapshot(); len(calls) != 0 {
		t.Fatalf("hook fired in read-only mode: %+v", calls)
	}
}

func TestPathValidation(t *testing.T) {
	root := t.TempDir()
	seedBareRepo(t, root, "r.git")
	handler, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Plant an escape target outside the serving root to prove traversal
	// cannot reach it.
	outside := filepath.Dir(root)
	if _, err := InitBare(outside, "escape.git"); err != nil {
		t.Fatalf("InitBare escape: %v", err)
	}

	cases := []string{
		"/../escape.git/info/refs?service=git-upload-pack",
		"/..%2Fescape.git/info/refs?service=git-upload-pack",
		"/r.git/../../escape.git/info/refs?service=git-upload-pack",
		"/.hidden.git/info/refs?service=git-upload-pack",
		"/missing.git/info/refs?service=git-upload-pack",
		"/r/info/refs?service=git-upload-pack", // no .git suffix, not a repo
		"/r.git",                               // no endpoint
		"/r.git/objects/info/packs",            // dumb protocol not served
	}
	for _, target := range cases {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/r.git/info/refs?service=git-upload-pack", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid info/refs = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// TestSymlinkEscapeBlocked proves a symlink planted under the serving root
// cannot expose a repository outside it, while a symlink resolving inside the
// root still serves.
func TestSymlinkEscapeBlocked(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	seedBareRepo(t, root, "r.git")
	outside := t.TempDir()
	secret, err := InitBare(outside, "secret.git")
	if err != nil {
		t.Fatalf("InitBare secret: %v", err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "link.git")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "r.git"), filepath.Join(root, "alias.git")); err != nil {
		t.Fatal(err)
	}
	handler, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/link.git/info/refs?service=git-upload-pack", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("symlink escape served: GET /link.git/info/refs = %d, want 404", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/alias.git/info/refs?service=git-upload-pack", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("in-root symlink refused: GET /alias.git/info/refs = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestAuthSeam(t *testing.T) {
	root := t.TempDir()
	seedBareRepo(t, root, "r.git")

	t.Run("deny", func(t *testing.T) {
		srv := newServer(t, root, WithAuth(func(*http.Request) (string, bool) { return "", false }))
		out, err := runGitErr(t, t.TempDir(), "clone", srv.URL+"/r.git", filepath.Join(t.TempDir(), "clone"))
		if err == nil {
			t.Fatalf("clone succeeded despite auth denial:\n%s", out)
		}
	})

	t.Run("allow with actor", func(t *testing.T) {
		var mu sync.Mutex
		var actors []string
		srv := newServer(t, root, WithAuth(func(r *http.Request) (string, bool) {
			mu.Lock()
			defer mu.Unlock()
			actors = append(actors, "alice")
			return "alice", true
		}))
		runGit(t, t.TempDir(), "clone", srv.URL+"/r.git", filepath.Join(t.TempDir(), "clone"))
		mu.Lock()
		defer mu.Unlock()
		if len(actors) == 0 {
			t.Fatal("auth seam was never consulted")
		}
	})
}

func TestConcurrentClones(t *testing.T) {
	root := t.TempDir()
	head := seedBareRepo(t, root, "r.git")
	srv := newServer(t, root)

	const clones = 4
	scratch := t.TempDir()
	errs := make(chan error, clones)
	var wg sync.WaitGroup
	for i := 0; i < clones; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dst := filepath.Join(scratch, fmt.Sprintf("clone-%d", i))
			out, err := runGitErr(t, scratch, "clone", srv.URL+"/r.git", dst)
			if err != nil {
				errs <- fmt.Errorf("clone %d: %v\n%s", i, err, out)
				return
			}
			got, err := runGitErr(t, dst, "rev-parse", "HEAD")
			if err != nil {
				errs <- fmt.Errorf("rev-parse %d: %v", i, err)
				return
			}
			if strings.TrimSpace(got) != head {
				errs <- fmt.Errorf("clone %d HEAD = %s, want %s", i, strings.TrimSpace(got), head)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestInitBareAndListRepos(t *testing.T) {
	root := t.TempDir()
	if _, err := InitBare(root, "alpha"); err != nil { // .git appended
		t.Fatalf("InitBare alpha: %v", err)
	}
	if _, err := InitBare(root, "team/beta.git"); err != nil {
		t.Fatalf("InitBare team/beta.git: %v", err)
	}
	if _, err := InitBare(root, "alpha.git"); err == nil {
		t.Fatal("InitBare allowed overwriting an existing repository")
	}
	for _, bad := range []string{"", "..", "a/../b", ".hidden", "/abs"} {
		if _, err := InitBare(root, bad); err == nil {
			t.Errorf("InitBare(%q) succeeded, want error", bad)
		}
	}
	// A plain directory must not be listed as a repository.
	if err := os.MkdirAll(filepath.Join(root, "not-a-repo.git"), 0o755); err != nil {
		t.Fatal(err)
	}
	repos, err := ListRepos(root)
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	want := []string{"alpha.git", "team/beta.git"}
	if len(repos) != len(want) {
		t.Fatalf("ListRepos = %v, want %v", repos, want)
	}
	for i := range want {
		if repos[i] != want[i] {
			t.Fatalf("ListRepos = %v, want %v", repos, want)
		}
	}
}
