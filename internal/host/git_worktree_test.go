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
