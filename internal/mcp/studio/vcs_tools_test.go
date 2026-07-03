package studio_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// vcs_tools_test.go — verification for the vcs.* / worktree.* surface. These run
// real git against a throwaway temp repo: git is deterministic, local, and free
// (no LLM, no network), so the safe-merge guarantees are exercised end-to-end.

// gitExec runs git in dir and fails the test on a non-zero exit.
func gitExec(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return string(out)
}

// initRepo creates a temp git repo on `main` with one committed file.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitExec(t, dir, "init", "-b", "main")
	gitExec(t, dir, "config", "user.email", "test@kitsoki.dev")
	gitExec(t, dir, "config", "user.name", "Kitsoki Test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644))
	// Mirror the repo convention (AGENTS.md): worktrees live under .worktrees/,
	// which is gitignored — so a worktree created inside the repo never makes the
	// main checkout's tree dirty (the precondition vcs.integrate guards on).
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".worktrees/\n"), 0o644))
	gitExec(t, dir, "add", "-A")
	gitExec(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func decodeOK(t *testing.T, res *mcpsdk.CallToolResult, out any) {
	t.Helper()
	require.False(t, res.IsError, contentText(res))
	require.NoError(t, json.Unmarshal([]byte(contentText(res)), out))
}

func TestVCS_StatusDiffLogCommit(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	cs := newStudioNoWorkspace(ctx, t)

	// Clean status on main.
	var st struct {
		Branch string `json:"branch"`
		Clean  bool   `json:"clean"`
		Files  []struct {
			XY   string `json:"xy"`
			Path string `json:"path"`
		} `json:"files"`
	}
	res, err := callTool(ctx, cs, "vcs.status", map[string]any{"dir": dir})
	require.NoError(t, err)
	decodeOK(t, res, &st)
	assert.Equal(t, "main", st.Branch)
	assert.True(t, st.Clean)
	assert.Empty(t, st.Files)

	// Make an untracked change → dirty.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o644))
	res, err = callTool(ctx, cs, "vcs.status", map[string]any{"dir": dir})
	require.NoError(t, err)
	decodeOK(t, res, &st)
	assert.False(t, st.Clean)
	require.NotEmpty(t, st.Files)

	// Commit it via vcs.commit.
	var commit struct {
		OK     bool   `json:"ok"`
		Commit string `json:"commit"`
	}
	res, err = callTool(ctx, cs, "vcs.commit", map[string]any{"dir": dir, "message": "add b"})
	require.NoError(t, err)
	decodeOK(t, res, &commit)
	assert.True(t, commit.OK)
	assert.NotEmpty(t, commit.Commit)

	// A second commit with nothing staged reports nothing_to_commit.
	var noop struct {
		NothingToCommit bool `json:"nothing_to_commit"`
	}
	res, err = callTool(ctx, cs, "vcs.commit", map[string]any{"dir": dir, "message": "noop"})
	require.NoError(t, err)
	decodeOK(t, res, &noop)
	assert.True(t, noop.NothingToCommit)

	// Log shows both commits.
	var lg struct {
		Commits []struct {
			Hash    string `json:"hash"`
			Subject string `json:"subject"`
		} `json:"commits"`
	}
	res, err = callTool(ctx, cs, "vcs.log", map[string]any{"dir": dir})
	require.NoError(t, err)
	decodeOK(t, res, &lg)
	require.Len(t, lg.Commits, 2)
	assert.Equal(t, "add b", lg.Commits[0].Subject)
}

// TestVCS_IntegratePreservesMainWork is the anti-footgun proof: vcs.integrate
// must NOT revert work landed on main after the worktree's base — the exact
// failure the `reset --soft main` ritual caused.
func TestVCS_IntegratePreservesMainWork(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	cs := newStudioNoWorkspace(ctx, t)

	// Branch a worktree off main's current tip.
	var wt struct {
		Path   string `json:"path"`
		Branch string `json:"branch"`
	}
	res, err := callTool(ctx, cs, "worktree.create", map[string]any{"dir": dir, "branch": "feat/x", "base": "main"})
	require.NoError(t, err)
	decodeOK(t, res, &wt)
	assert.DirExists(t, wt.Path)

	// MAIN advances AFTER the worktree's base (the post-base work that the old
	// ritual would have reverted).
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main-only.txt"), []byte("landed on main\n"), 0o644))
	gitExec(t, dir, "add", "-A")
	gitExec(t, dir, "commit", "-q", "-m", "main advances")

	// The worktree makes its own change.
	require.NoError(t, os.WriteFile(filepath.Join(wt.Path, "feat.txt"), []byte("feature\n"), 0o644))
	res, err = callTool(ctx, cs, "vcs.commit", map[string]any{"dir": wt.Path, "message": "feature work"})
	require.NoError(t, err)
	require.False(t, res.IsError, contentText(res))

	// Integrate — onto the CURRENT main tip.
	var integ struct {
		OK         bool     `json:"ok"`
		Integrated bool     `json:"integrated"`
		Commit     string   `json:"commit"`
		Conflicts  []string `json:"conflicts"`
		Refused    string   `json:"refused"`
	}
	res, err = callTool(ctx, cs, "vcs.integrate", map[string]any{
		"dir": dir, "branch": "feat/x", "onto": "main",
		"message": "integrate feat/x", "worktree_path": wt.Path,
	})
	require.NoError(t, err)
	decodeOK(t, res, &integ)
	require.Empty(t, integ.Refused, "should not refuse a clean integrate")
	require.True(t, integ.Integrated, "expected a clean squash land; conflicts=%v", integ.Conflicts)
	assert.NotEmpty(t, integ.Commit)

	// THE PROOF: main carries BOTH the post-base main work AND the feature.
	assert.FileExists(t, filepath.Join(dir, "main-only.txt"))
	assert.FileExists(t, filepath.Join(dir, "feat.txt"))

	// The worktree was cleaned up.
	var wl struct {
		Worktrees []struct {
			Path string `json:"path"`
		} `json:"worktrees"`
	}
	res, err = callTool(ctx, cs, "worktree.list", map[string]any{"dir": dir})
	require.NoError(t, err)
	decodeOK(t, res, &wl)
	for _, w := range wl.Worktrees {
		assert.NotEqual(t, wt.Path, w.Path, "integrated worktree should be removed")
	}
}

func TestVCS_IntegrateRefusesGuards(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	cs := newStudioNoWorkspace(ctx, t)

	res, err := callTool(ctx, cs, "worktree.create", map[string]any{"dir": dir, "branch": "feat/y", "base": "main"})
	require.NoError(t, err)
	require.False(t, res.IsError, contentText(res))

	// Guard: nothing to integrate (feat/y has no commits beyond main).
	var refused struct {
		Integrated bool   `json:"integrated"`
		Refused    string `json:"refused"`
	}
	res, err = callTool(ctx, cs, "vcs.integrate", map[string]any{
		"dir": dir, "branch": "feat/y", "onto": "main", "message": "noop",
	})
	require.NoError(t, err)
	decodeOK(t, res, &refused)
	assert.False(t, refused.Integrated)
	assert.NotEmpty(t, refused.Refused)

	// Guard: dirty integration tree is refused (clean-tree precondition).
	require.NoError(t, os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("x\n"), 0o644))
	res, err = callTool(ctx, cs, "vcs.integrate", map[string]any{
		"dir": dir, "branch": "feat/y", "onto": "main", "message": "noop",
	})
	require.NoError(t, err)
	decodeOK(t, res, &refused)
	assert.False(t, refused.Integrated)
	assert.Contains(t, refused.Refused, "uncommitted")
}
