// Package host — host.git_worktree — git-worktree-backed workspace provider.
//
// Implements the `workspace` host_interface declared in
// docs/proposals/notes/dev-story-implementation-contract.md §2.4.  A
// single prefix-fallback handler dispatches the four workspace ops via
// the `op` arg.  Operations shell out to `git worktree`.
//
// Convention: workspace ID == the worktree's directory basename; the
// worktrees live under `<repo>/.worktrees/<id>` (matching the
// kitsoki-dev dogfood path).
package host

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// GitWorktreeHandler implements host.git_worktree (prefix-fallback).
//
// Required args:
//   - op (string): one of list, get, create, sync.
//
// Optional/per-op args:
//   - repo (string): path to the main repository.  Defaults to cwd if absent.
//   - id   (string): workspace id (== basename of the worktree dir).
//
// The `create` op additionally requires `name` (the new branch);
// optional `ticket_id` (forwarded as Description) and `base` (the
// branch the worktree is rooted at).
func GitWorktreeHandler(ctx context.Context, args map[string]any) (Result, error) {
	op, _ := args["op"].(string)
	op = strings.TrimSpace(op)
	if op == "" {
		return Result{Error: "host.git_worktree: op argument is required"}, nil
	}
	repo, _ := args["repo"].(string)

	switch op {
	case "list":
		return worktreeList(ctx, repo)
	case "get":
		return worktreeGet(ctx, repo, args)
	case "create":
		return worktreeCreate(ctx, repo, args)
	case "sync":
		return worktreeSync(ctx, repo, args)
	default:
		return Result{Error: fmt.Sprintf("host.git_worktree: unknown op %q", op)}, nil
	}
}

// worktreeList parses `git worktree list --porcelain` into a slice of
// {id, path, branch, dirty} maps.
func worktreeList(ctx context.Context, repo string) (Result, error) {
	stdout, stderr, code, err := cliExec(ctx, repo, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return Result{Error: fmt.Sprintf("workspace.list: exec: %v", err)}, nil
	}
	if code != 0 {
		return Result{Error: fmt.Sprintf("workspace.list: %s", strings.TrimSpace(stderr))}, nil
	}
	wts := parseWorktreePorcelain(stdout)
	out := make([]map[string]any, 0, len(wts))
	for _, wt := range wts {
		out = append(out, worktreeSummary(wt))
	}
	return Result{Data: map[string]any{"workspaces": out}}, nil
}

func worktreeGet(ctx context.Context, repo string, args map[string]any) (Result, error) {
	id, _ := args["id"].(string)
	if strings.TrimSpace(id) == "" {
		return Result{Error: "workspace.get: id argument is required"}, nil
	}
	stdout, _, _, err := cliExec(ctx, repo, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return Result{Error: fmt.Sprintf("workspace.get: exec: %v", err)}, nil
	}
	for _, wt := range parseWorktreePorcelain(stdout) {
		if filepath.Base(wt.Path) == id {
			// Also probe `git status --porcelain` in the worktree to
			// resolve dirty.
			dirty := false
			if statusOut, _, _, sErr := cliExec(ctx, wt.Path, "git", "status", "--porcelain"); sErr == nil {
				dirty = strings.TrimSpace(statusOut) != ""
			}
			wt.Dirty = dirty
			data := worktreeSummary(wt)
			return Result{Data: data}, nil
		}
	}
	return Result{Error: fmt.Sprintf("workspace.get: %q not found", id)}, nil
}

func worktreeCreate(ctx context.Context, repo string, args map[string]any) (Result, error) {
	name, _ := args["name"].(string)
	if strings.TrimSpace(name) == "" {
		return Result{Error: "workspace.create: name argument is required"}, nil
	}
	base, _ := args["base"].(string)
	// Path: <repo>/.worktrees/<name>.  Slashes in name get flattened
	// to dashes to stay filesystem-safe (a branch named
	// `feature/foo` becomes the dir `feature-foo`).
	dirID := strings.ReplaceAll(name, "/", "-")
	path := filepath.Join(repo, ".worktrees", dirID)
	gitArgs := []string{"worktree", "add", "-b", name, path}
	if base != "" {
		gitArgs = append(gitArgs, base)
	}
	_, stderr, code, err := cliExec(ctx, repo, "git", gitArgs...)
	if err != nil {
		return Result{Error: fmt.Sprintf("workspace.create: exec: %v", err)}, nil
	}
	if code != 0 {
		return Result{Error: fmt.Sprintf("workspace.create: %s", strings.TrimSpace(stderr))}, nil
	}
	return Result{Data: map[string]any{
		"ok":   true,
		"path": path,
	}}, nil
}

func worktreeSync(ctx context.Context, repo string, args map[string]any) (Result, error) {
	id, _ := args["id"].(string)
	if strings.TrimSpace(id) == "" {
		return Result{Error: "workspace.sync: id argument is required"}, nil
	}
	// Find the path for the named workspace.
	stdout, _, _, err := cliExec(ctx, repo, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return Result{Error: fmt.Sprintf("workspace.sync: exec: %v", err)}, nil
	}
	var target *worktreeInfo
	for _, wt := range parseWorktreePorcelain(stdout) {
		if filepath.Base(wt.Path) == id {
			w := wt
			target = &w
			break
		}
	}
	if target == nil {
		return Result{Error: fmt.Sprintf("workspace.sync: %q not found", id)}, nil
	}
	// Pull --ff-only from the upstream — non-destructive, returns
	// error if the branch has diverged.
	pullOut, stderr, code, err := cliExec(ctx, target.Path, "git", "pull", "--ff-only")
	if err != nil {
		return Result{Error: fmt.Sprintf("workspace.sync: exec: %v", err)}, nil
	}
	if code != 0 {
		return Result{Error: fmt.Sprintf("workspace.sync: %s", strings.TrimSpace(stderr))}, nil
	}
	return Result{Data: map[string]any{
		"ok":  true,
		"log": pullOut,
	}}, nil
}

// ─── porcelain parser ───────────────────────────────────────────────────────

type worktreeInfo struct {
	Path   string
	Branch string
	Head   string
	Dirty  bool
}

// parseWorktreePorcelain reads `git worktree list --porcelain` output.
// Records are separated by blank lines; within each record, keys are
// "worktree <path>", "HEAD <sha>", "branch <refs/heads/...>" lines.
func parseWorktreePorcelain(s string) []worktreeInfo {
	var out []worktreeInfo
	var cur worktreeInfo
	flush := func() {
		if cur.Path != "" {
			out = append(out, cur)
		}
		cur = worktreeInfo{}
	}
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimRight(ln, "\r")
		if ln == "" {
			flush()
			continue
		}
		switch {
		case strings.HasPrefix(ln, "worktree "):
			cur.Path = strings.TrimPrefix(ln, "worktree ")
		case strings.HasPrefix(ln, "HEAD "):
			cur.Head = strings.TrimPrefix(ln, "HEAD ")
		case strings.HasPrefix(ln, "branch "):
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(ln, "branch "), "refs/heads/")
		}
	}
	flush()
	return out
}

func worktreeSummary(wt worktreeInfo) map[string]any {
	id := filepath.Base(wt.Path)
	return map[string]any{
		"id":     id,
		"path":   wt.Path,
		"branch": wt.Branch,
		"dirty":  wt.Dirty,
	}
}
