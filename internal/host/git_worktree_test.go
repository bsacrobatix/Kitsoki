package host_test

import (
	"context"
	"strings"
	"testing"

	"kitsoki/internal/host"
)

const sampleWorktreePorcelain = `worktree /repo
HEAD aaaaaaaa
branch refs/heads/main

worktree /repo/.worktrees/feature-x
HEAD bbbbbbbb
branch refs/heads/feature/x

`

func TestGitWorktree_RegisteredAsBuiltin(t *testing.T) {
	r := host.NewRegistry()
	host.RegisterBuiltins(r)
	for _, n := range []string{
		"host.git_worktree",
		"host.git_worktree.list",
		"host.git_worktree.get",
		"host.git_worktree.create",
		"host.git_worktree.sync",
	} {
		if _, ok := r.Get(n); !ok {
			t.Fatalf("registry: %s missing", n)
		}
	}
}

func TestGitWorktree_MissingOp(t *testing.T) {
	res, err := host.GitWorktreeHandler(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected domain error")
	}
}

func TestGitWorktree_List_Happy(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["git worktree list --porcelain"] = fakeResp{stdout: sampleWorktreePorcelain}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.GitWorktreeHandler(context.Background(), map[string]any{"op": "list"})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	wts, _ := res.Data["workspaces"].([]map[string]any)
	if len(wts) != 2 {
		t.Fatalf("expected 2 worktrees, got %d (%v)", len(wts), wts)
	}
	// Last worktree should be feature-x.
	if wts[1]["id"] != "feature-x" {
		t.Fatalf("id: %v", wts[1]["id"])
	}
	if wts[1]["branch"] != "feature/x" {
		t.Fatalf("branch: %v", wts[1]["branch"])
	}
}

func TestGitWorktree_Get_Happy(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["git worktree list --porcelain"] = fakeResp{stdout: sampleWorktreePorcelain}
	fr.responses["git status --porcelain"] = fakeResp{stdout: " M file.go\n"}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.GitWorktreeHandler(context.Background(), map[string]any{
		"op": "get",
		"id": "feature-x",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	if res.Data["dirty"] != true {
		t.Fatalf("dirty: %v", res.Data["dirty"])
	}
	if res.Data["branch"] != "feature/x" {
		t.Fatalf("branch: %v", res.Data["branch"])
	}
}

func TestGitWorktree_Get_NotFound(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["git worktree list --porcelain"] = fakeResp{stdout: sampleWorktreePorcelain}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.GitWorktreeHandler(context.Background(), map[string]any{
		"op": "get",
		"id": "nope",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected domain error")
	}
}

func TestGitWorktree_Create_Happy(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["git worktree add -b feature/x"] = fakeResp{}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.GitWorktreeHandler(context.Background(), map[string]any{
		"op":   "create",
		"repo": "/repo",
		"name": "feature/x",
		"base": "main",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	path, _ := res.Data["path"].(string)
	if !strings.Contains(path, "/repo/.worktrees/feature-x") {
		t.Fatalf("path: %s", path)
	}
}

// Authors that bind `workspace_id` from world state pass it as `id:` so
// the on-disk dir basename matches what `sync` looks up by. Without
// honouring `id:`, the dir is derived from `name` (slashes flattened)
// and worktreeSync (keyed on workspace_id) can't find what create just
// made — the silent-bounce-to-idle that surfaced in dogfood.
func TestGitWorktree_Create_IDOverridesDir(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["git worktree add -b fix/T1"] = fakeResp{}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.GitWorktreeHandler(context.Background(), map[string]any{
		"op":   "create",
		"repo": "/repo",
		"id":   "bf-T1",
		"name": "fix/T1",
		"base": "main",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	path, _ := res.Data["path"].(string)
	if path != "/repo/.worktrees/bf-T1" {
		t.Fatalf("path: %s (want /repo/.worktrees/bf-T1)", path)
	}
	// Confirm the git invocation used the id-derived path, not the
	// name-derived one.
	var sawAdd bool
	for _, c := range fr.calls {
		if strings.Contains(c, "git worktree add -b fix/T1 /repo/.worktrees/bf-T1 main") {
			sawAdd = true
		}
		if strings.Contains(c, "/repo/.worktrees/fix-T1") {
			t.Fatalf("call used name-derived dir, not id: %s", c)
		}
	}
	if !sawAdd {
		t.Fatalf("expected `git worktree add -b fix/T1 /repo/.worktrees/bf-T1 main`, got %v", fr.calls)
	}
}

// Stale-branch recovery: a previous run left `fix/X` behind but the
// worktree dir was removed. A naive `git worktree add -b` fails with
// "a branch named ... already exists"; we retry without `-b` to
// reattach the existing branch. Without this, the operator hits a
// permanently-failing create that `on_error: idle` silently swallows.
func TestGitWorktree_Create_ReattachStaleBranch(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["git worktree add -b fix/T2 /repo/.worktrees/bf-T2 main"] = fakeResp{
		stderr: "fatal: a branch named 'fix/T2' already exists",
		code:   128,
	}
	fr.responses["git worktree add /repo/.worktrees/bf-T2 fix/T2"] = fakeResp{}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.GitWorktreeHandler(context.Background(), map[string]any{
		"op":   "create",
		"repo": "/repo",
		"id":   "bf-T2",
		"name": "fix/T2",
		"base": "main",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	if res.Data["reused"] != true {
		t.Fatalf("expected reused=true, got %#v", res.Data)
	}
	if res.Data["path"] != "/repo/.worktrees/bf-T2" {
		t.Fatalf("path: %v", res.Data["path"])
	}
}

// Idempotency: a worktree already registered at our target path with
// our target branch is treated as success. Lets bf.idle re-enter
// (post-restart, post-restart_from) without re-running create against
// a workspace that's already on disk.
func TestGitWorktree_Create_IdempotentExistingWorktree(t *testing.T) {
	porcelain := "worktree /repo\nHEAD aaaa\nbranch refs/heads/main\n\n" +
		"worktree /repo/.worktrees/bf-T3\nHEAD bbbb\nbranch refs/heads/fix/T3\n\n"
	fr := newFakeRunner()
	fr.responses["git worktree list --porcelain"] = fakeResp{stdout: porcelain}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.GitWorktreeHandler(context.Background(), map[string]any{
		"op":   "create",
		"repo": "/repo",
		"id":   "bf-T3",
		"name": "fix/T3",
		"base": "main",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	if res.Data["path"] != "/repo/.worktrees/bf-T3" {
		t.Fatalf("path: %v", res.Data["path"])
	}
	// No `worktree add` should have been issued — we found the
	// existing one and short-circuited.
	for _, c := range fr.calls {
		if strings.Contains(c, "worktree add") {
			t.Fatalf("unexpected `worktree add`: %s", c)
		}
	}
}

// Same dir, wrong branch: report rather than silently overwrite. The
// operator likely has a parallel session or a misconfigured workspace.
func TestGitWorktree_Create_PathHeldByOtherBranch(t *testing.T) {
	porcelain := "worktree /repo\nHEAD aaaa\nbranch refs/heads/main\n\n" +
		"worktree /repo/.worktrees/bf-T4\nHEAD bbbb\nbranch refs/heads/other-branch\n\n"
	fr := newFakeRunner()
	fr.responses["git worktree list --porcelain"] = fakeResp{stdout: porcelain}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.GitWorktreeHandler(context.Background(), map[string]any{
		"op":   "create",
		"repo": "/repo",
		"id":   "bf-T4",
		"name": "fix/T4",
		"base": "main",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected domain error (path held by other branch)")
	}
	if !strings.Contains(res.Error, "other-branch") {
		t.Fatalf("error should name the conflicting branch, got: %s", res.Error)
	}
}

func TestGitWorktree_Create_MissingName(t *testing.T) {
	restore := host.SetExecRunnerForTest(newFakeRunner().run)
	defer restore()

	res, err := host.GitWorktreeHandler(context.Background(), map[string]any{"op": "create"})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected domain error")
	}
}

func TestGitWorktree_Sync_Happy(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["git worktree list --porcelain"] = fakeResp{stdout: sampleWorktreePorcelain}
	fr.responses["git pull --ff-only"] = fakeResp{stdout: "Already up to date.\n"}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.GitWorktreeHandler(context.Background(), map[string]any{
		"op": "sync",
		"id": "feature-x",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	if res.Data["ok"] != true {
		t.Fatalf("ok: %v", res.Data["ok"])
	}
}
