// Package host — host.git_worktree — git-worktree-backed workspace provider.
//
// Implements the `workspace` host_interface (see docs/architecture/hosts.md).  A
// single prefix-fallback handler dispatches the four workspace ops via
// the `op` arg.  Operations shell out to `git worktree`.
//
// Convention: workspace ID == the worktree's directory basename; the
// worktrees live under `<repo>/.worktrees/<id>` (matching the
// kitsoki-dev dogfood path).
package host

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ownerSentinelFile is the basename of the per-worktree sentinel that records
// which kitsoki session created a worktree. worktreeCreate writes it on a
// successful `git worktree add` and reads it in the idempotency short-circuit
// to refuse handing one session's live tree to a different session — the
// host-side safety net for the destructive shared-checkout bug
// (2026-06-03T121409Z-concurrent-dogfood-sessions-share-checkout-destructive-git).
const ownerSentinelFile = ".kitsoki-owner"

const cloneSentinelFile = ".kitsoki-clone"

// writeOwnerSentinel records sid as the owning session of the worktree at path.
// Empty sid is a no-op (callers that omit the session dimension leave no
// sentinel — the short-circuit then treats an absent sentinel as shareable for
// back-compat). Best-effort: a write failure is non-fatal to create.
func writeOwnerSentinel(path, sid string) {
	if strings.TrimSpace(sid) == "" {
		return
	}
	_ = os.WriteFile(filepath.Join(path, ownerSentinelFile), []byte(sid), 0o644)
}

// readOwnerSentinel returns the session id recorded for the worktree at path,
// or "" when no sentinel is present (or it is unreadable/empty).
func readOwnerSentinel(path string) string {
	b, err := os.ReadFile(filepath.Join(path, ownerSentinelFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// GitWorktreeHandler implements host.git_worktree (prefix-fallback).
//
// Required args:
//   - op (string): one of list, get, create, sync.
//
// Optional/per-op args:
//   - repo (string): path to the main repository.  Defaults to cwd if absent.
//   - id   (string): workspace id (== basename of the worktree dir).
//
// The `create` op additionally requires `name` (the new branch).
// Optional create args:
//   - id   (string): explicit workspace id.  Becomes the worktree's
//     directory basename.  When absent, falls back to the legacy
//     slashes-flattened `name` (`feature/foo` → `feature-foo`) for
//     back-compat with callers that only supply the branch.  Authors
//     that bind `workspace_id` from world state should pass it as
//     `id:` so the on-disk dir matches what `sync` looks up by.
//   - base (string): branch the new worktree is rooted at.
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
	case "cleanup_scan":
		return worktreeCleanupScan(ctx, repo, args)
	case "cleanup_apply":
		return worktreeCleanupApply(ctx, repo, args)
	case "clone_create":
		return cloneCreate(ctx, repo, args)
	case "clone_cleanup_scan":
		return cloneCleanupScan(ctx, repo, args)
	case "clone_cleanup_apply":
		return cloneCleanupApply(ctx, repo, args)
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
	resolvedRepo, repoErr := resolveWorktreeRepo(ctx, repo)
	if repoErr != "" {
		return Result{Error: "workspace.create: " + repoErr}, nil
	}
	base, _ := args["base"].(string)
	// Explicit `id:` (from world.workspace_id) wins; fall back to the
	// slashes-flattened branch for callers that only supply `name`.
	// Without the explicit id, the on-disk dir basename diverged from
	// the workspace_id authors wrote into world state, so worktreeSync
	// (which keys on workspace_id) couldn't find the dir worktreeCreate
	// had just made. Symptom: implementing.on_enter → workspace.sync
	// errors with "not found" → on_error: idle quietly bounced the
	// operator back to the parked room.
	id, _ := args["id"].(string)
	if strings.TrimSpace(id) == "" {
		id = strings.ReplaceAll(name, "/", "-")
	}
	// session_id is the calling session's identity (projected into the
	// story as world.session_id). It is optional — callers that omit it
	// leave no ownership sentinel and keep the legacy shareable behaviour —
	// but when present it lets the idempotency short-circuit refuse to hand
	// one session's live tree to a DIFFERENT session.
	sessionID := strings.TrimSpace(worktreeStringArg(args, "session_id"))
	path := filepath.Join(resolvedRepo, ".worktrees", id)

	// Idempotency: if a worktree is already registered at our path
	// with our target branch, treat as success. This keeps re-entry
	// to bf.idle (e.g. after a process restart that lost
	// bf_autostart_attempted=true) from failing on a workspace that
	// already exists from a prior run.
	//
	// Safety net: before returning that existing tree as "ok", consult the
	// `.kitsoki-owner` sentinel. If it names a DIFFERENT session than the
	// caller, REFUSE — handing session B session A's live working tree is
	// the destructive shared-checkout bug
	// (2026-06-03T121409Z-concurrent-dogfood-sessions-share-checkout-destructive-git):
	// a routine `git checkout -- <file>` in one session silently and
	// unrecoverably reverts the other's uncommitted WIP. An absent sentinel
	// (legacy worktree, or a caller that omitted session_id) or a matching
	// one still short-circuits to success, preserving legitimate same-session
	// re-entry after a restart.
	if existing, ok := findWorktreeByPath(ctx, resolvedRepo, path); ok {
		if existing.Branch == name {
			if owner := readOwnerSentinel(path); owner != "" && sessionID != "" && owner != sessionID {
				return Result{Error: fmt.Sprintf("workspace.create: %q is already checked out by session %q; refusing to share — concurrent sessions on the same ticket must use distinct worktrees", id, owner)}, nil
			}
			return Result{Data: map[string]any{"ok": true, "path": path}}, nil
		}
		return Result{Error: fmt.Sprintf("workspace.create: %q already exists at %s but holds branch %q (wanted %q)", id, path, existing.Branch, name)}, nil
	}

	// Try the new-branch path first. The common case is a fresh
	// ticket where neither the branch nor the dir exists.
	gitArgs := []string{"worktree", "add", "-b", name, path}
	if base != "" {
		gitArgs = append(gitArgs, base)
	}
	_, stderr, code, err := cliExec(ctx, resolvedRepo, "git", gitArgs...)
	if err != nil {
		return Result{Error: fmt.Sprintf("workspace.create: exec: %v", err)}, nil
	}
	if code == 0 {
		writeOwnerSentinel(path, sessionID)
		return Result{Data: map[string]any{"ok": true, "path": path}}, nil
	}

	// Branch-already-exists recovery. Happens when a previous run
	// created the branch but the worktree dir was later removed
	// without `git branch -d`. Without this, the operator hits a
	// permanently-failing create that on_error: idle silently swallows
	// and has to clean up by hand. Re-attach the existing branch
	// to a fresh worktree at our path instead.
	if branchExistsError(stderr, name) {
		retryArgs := []string{"worktree", "add", path, name}
		_, retryStderr, retryCode, retryErr := cliExec(ctx, resolvedRepo, "git", retryArgs...)
		if retryErr != nil {
			return Result{Error: fmt.Sprintf("workspace.create: exec (reattach): %v", retryErr)}, nil
		}
		if retryCode == 0 {
			writeOwnerSentinel(path, sessionID)
			return Result{Data: map[string]any{
				"ok":     true,
				"path":   path,
				"reused": true,
				"branch": name,
			}}, nil
		}
		// Reattach can fail when the branch is checked out at *another*
		// worktree (a parallel session, an unrelated dir). Report the
		// underlying git message so the operator can locate the holder.
		return Result{Error: fmt.Sprintf("workspace.create: branch %q exists but reattach failed: %s", name, strings.TrimSpace(retryStderr))}, nil
	}

	return Result{Error: fmt.Sprintf("workspace.create: %s", strings.TrimSpace(stderr))}, nil
}

// resolveWorktreeRepo returns the absolute git toplevel used to anchor
// .worktrees/<id>. An omitted repo used to mean "whatever cwd the kitsoki
// process currently has", which made browser/server sessions return relative
// paths that later agent calls could not chdir into. Resolve through git so the
// worktree path is anchored to the repository that owns the running story.
func resolveWorktreeRepo(ctx context.Context, repo string) (string, string) {
	repo = strings.TrimSpace(repo)
	if repo != "" {
		abs, err := filepath.Abs(repo)
		if err != nil {
			return "", fmt.Sprintf("resolve repo %q: %v", repo, err)
		}
		return abs, ""
	}
	stdout, stderr, code, err := cliExec(ctx, "", "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Sprintf("resolve repo: %v", err)
	}
	if code != 0 {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = "git rev-parse --show-toplevel failed"
		}
		return "", msg
	}
	root := strings.TrimSpace(stdout)
	if root == "" {
		return "", "git rev-parse --show-toplevel returned an empty path"
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Sprintf("resolve repo %q: %v", root, err)
	}
	return abs, ""
}

// findWorktreeByPath returns the worktreeInfo registered for the
// workspace whose path matches `path`. `git worktree list --porcelain`
// always emits absolute paths, but callers commonly construct `path`
// relative to `repo` (which itself may be empty / cwd), so the
// straight `wt.Path == path` comparison silently misses every
// re-entry — which is exactly what made the dogfood session loop:
// the worktree at `/repo/.worktrees/bf-X` was actually registered,
// but we couldn't see it, so we fell through to `git worktree add`
// which then failed with `<path> already exists`.
//
// Normalise both sides via filepath.Abs (resolving `path` against the
// process cwd when `repo` is empty, which mirrors cliExec's behaviour)
// and also accept a basename match as a last resort — workspace ids
// are unique by convention in `.worktrees/<id>`.
func findWorktreeByPath(ctx context.Context, repo, path string) (worktreeInfo, bool) {
	stdout, _, _, err := cliExec(ctx, repo, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return worktreeInfo{}, false
	}
	absPath, _ := filepath.Abs(path)
	base := filepath.Base(path)
	for _, wt := range parseWorktreePorcelain(stdout) {
		if wt.Path == path || wt.Path == absPath {
			return wt, true
		}
		if base != "" && filepath.Base(wt.Path) == base {
			return wt, true
		}
	}
	return worktreeInfo{}, false
}

// branchExistsError reports whether the stderr from `git worktree add
// -b` indicates the branch already exists locally. Git's exact phrasing
// is "fatal: a branch named '<name>' already exists" (with surrounding
// noise from the porcelain). Match on the stable middle so phrasing
// drift between git versions doesn't silently break the recovery path.
func branchExistsError(stderr, name string) bool {
	s := strings.ToLower(stderr)
	if !strings.Contains(s, "already exists") {
		return false
	}
	return strings.Contains(stderr, "'"+name+"'") || strings.Contains(s, "branch named")
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
	// No-op if the branch has no upstream tracking. A fresh
	// `fix/<ticket>` feature branch typically has no remote yet —
	// `git pull --ff-only` would fail with `There is no tracking
	// information for the current branch`, on_error: idle would
	// silently bounce us back to a parked room, and the operator
	// would have no signal as to why. Detect via
	// `git rev-parse --abbrev-ref @{u}` (non-zero exit when no
	// upstream is set) and skip the pull in that case.
	if _, _, upstreamCode, upstreamErr := cliExec(ctx, target.Path, "git", "rev-parse", "--abbrev-ref", "@{u}"); upstreamErr != nil || upstreamCode != 0 {
		return Result{Data: map[string]any{
			"ok":             true,
			"log":            "",
			"skipped_reason": "no upstream tracking",
		}}, nil
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

func worktreeCleanupScan(ctx context.Context, repo string, args map[string]any) (Result, error) {
	base := strings.TrimSpace(worktreeStringArg(args, "base"))
	if base == "" {
		base = "main"
	}
	exclude := strings.TrimSpace(worktreeStringArg(args, "exclude"))
	protected := cleanupProtectedBranches(worktreeStringArg(args, "protected"))

	stdout, stderr, code, err := cliExec(ctx, repo, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return Result{Error: fmt.Sprintf("workspace.cleanup_scan: list worktrees: %v", err)}, nil
	}
	if code != 0 {
		return Result{Error: fmt.Sprintf("workspace.cleanup_scan: list worktrees: %s", strings.TrimSpace(stderr))}, nil
	}
	worktrees := parseWorktreePorcelain(stdout)
	attached := map[string]bool{}
	var primaryPath string
	if len(worktrees) > 0 {
		primaryPath = worktrees[0].Path
	}
	out := make([]map[string]any, 0, len(worktrees))
	for _, wt := range worktrees {
		if wt.Branch != "" {
			attached[wt.Branch] = true
		}
		if wt.Path == primaryPath {
			continue
		}
		wt.Dirty = worktreeDirty(ctx, wt.Path)
		c := cleanupCandidate(ctx, repo, wt, base, protected, exclude, "worktree")
		c["actions"] = []string{"worktree_remove", "branch_delete"}
		out = append(out, c)
		out = append(out, cleanupCacheCandidates(wt, exclude)...)
	}

	branchOut, branchErr, branchCode, branchExecErr := cliExec(ctx, repo, "git", "branch", "--format=%(refname:short)")
	if branchExecErr != nil {
		return Result{Error: fmt.Sprintf("workspace.cleanup_scan: list branches: %v", branchExecErr)}, nil
	}
	if branchCode != 0 {
		return Result{Error: fmt.Sprintf("workspace.cleanup_scan: list branches: %s", strings.TrimSpace(branchErr))}, nil
	}
	for _, branch := range strings.Split(branchOut, "\n") {
		branch = strings.TrimSpace(strings.TrimPrefix(branch, "* "))
		if branch == "" || attached[branch] {
			continue
		}
		wt := worktreeInfo{Branch: branch, Path: ""}
		c := cleanupCandidate(ctx, repo, wt, base, protected, exclude, "branch")
		c["actions"] = []string{"branch_delete"}
		out = append(out, c)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i]["recommended"] != out[j]["recommended"] {
			return out[i]["recommended"] == true
		}
		return fmt.Sprint(out[i]["branch"]) < fmt.Sprint(out[j]["branch"])
	})
	recommended := 0
	for _, c := range out {
		if c["recommended"] == true {
			recommended++
		}
	}
	return Result{Data: map[string]any{
		"ok":                true,
		"base":              base,
		"exclude":           exclude,
		"candidates":        out,
		"recommended_count": recommended,
	}}, nil
}

func worktreeCleanupApply(ctx context.Context, repo string, args map[string]any) (Result, error) {
	candidates, parseErr := cleanupCandidatesArg(args["candidates"])
	if parseErr != "" {
		return Result{Error: "workspace.cleanup_apply: " + parseErr}, nil
	}
	if len(candidates) == 0 {
		return Result{Data: map[string]any{"ok": true, "deleted": []map[string]any{}, "skipped": []map[string]any{}}}, nil
	}
	var deleted []map[string]any
	var skipped []map[string]any
	var errs []string
	for _, raw := range candidates {
		c, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		branch := strings.TrimSpace(fmt.Sprint(c["branch"]))
		path := strings.TrimSpace(fmt.Sprint(c["path"]))
		if c["recommended"] != true {
			skipped = append(skipped, map[string]any{"branch": branch, "path": path, "reason": "not recommended"})
			continue
		}
		if path != "" {
			if fmt.Sprint(c["kind"]) == "cache" {
				if err := validateCacheCleanupPath(repo, path); err != nil {
					errs = append(errs, fmt.Sprintf("%s: cache remove: %v", path, err))
					continue
				}
				if err := makeTreeWritable(path); err != nil {
					errs = append(errs, fmt.Sprintf("%s: cache chmod: %v", path, err))
					continue
				}
				if err := os.RemoveAll(path); err != nil {
					errs = append(errs, fmt.Sprintf("%s: cache remove: %v", path, err))
					continue
				}
				deleted = append(deleted, map[string]any{"branch": branch, "path": path, "kind": "cache"})
				continue
			} else {
				_, stderr, code, err := cliExec(ctx, repo, "git", "worktree", "remove", path)
				if err != nil {
					errs = append(errs, fmt.Sprintf("%s: worktree remove: %v", branch, err))
					continue
				}
				if code != 0 {
					errs = append(errs, fmt.Sprintf("%s: worktree remove: %s", branch, strings.TrimSpace(stderr)))
					continue
				}
			}
		}
		if branch != "" {
			_, stderr, code, err := cliExec(ctx, repo, "git", "branch", "-d", branch)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: branch delete: %v", branch, err))
				continue
			}
			if code != 0 {
				errs = append(errs, fmt.Sprintf("%s: branch delete: %s", branch, strings.TrimSpace(stderr)))
				continue
			}
		}
		deleted = append(deleted, map[string]any{"branch": branch, "path": path})
	}
	data := map[string]any{"ok": len(errs) == 0, "deleted": deleted, "skipped": skipped}
	if len(errs) > 0 {
		data["errors"] = errs
		return Result{Data: data, Error: "workspace.cleanup_apply: " + strings.Join(errs, "; ")}, nil
	}
	return Result{Data: data}, nil
}

func cloneCreate(ctx context.Context, repo string, args map[string]any) (Result, error) {
	id := strings.TrimSpace(worktreeStringArg(args, "id"))
	if id == "" {
		return Result{Error: "workspace.clone_create: id argument is required"}, nil
	}
	if strings.Contains(id, "/") || strings.Contains(id, string(filepath.Separator)) || id == "." || id == ".." {
		return Result{Error: fmt.Sprintf("workspace.clone_create: invalid id %q", id)}, nil
	}
	source, repoErr := resolveWorktreeRepo(ctx, repo)
	if repoErr != "" {
		return Result{Error: "workspace.clone_create: " + repoErr}, nil
	}
	root := cloneRoot(source, worktreeStringArg(args, "root"))
	path := filepath.Join(root, id)
	if _, err := os.Stat(path); err == nil {
		return Result{Error: fmt.Sprintf("workspace.clone_create: clone %q already exists at %s", id, path)}, nil
	} else if err != nil && !os.IsNotExist(err) {
		return Result{Error: fmt.Sprintf("workspace.clone_create: stat %s: %v", path, err)}, nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return Result{Error: fmt.Sprintf("workspace.clone_create: mkdir %s: %v", root, err)}, nil
	}
	_, stderr, code, err := cliExec(ctx, source, "git", "clone", source, path)
	if err != nil {
		return Result{Error: fmt.Sprintf("workspace.clone_create: exec: %v", err)}, nil
	}
	if code != 0 {
		return Result{Error: fmt.Sprintf("workspace.clone_create: %s", strings.TrimSpace(stderr))}, nil
	}

	base := strings.TrimSpace(worktreeStringArg(args, "base"))
	name := strings.TrimSpace(worktreeStringArg(args, "name"))
	if name != "" {
		checkoutArgs := []string{"checkout", "-b", name}
		if base != "" {
			checkoutArgs = append(checkoutArgs, base)
		}
		if _, checkoutStderr, checkoutCode, checkoutErr := cliExec(ctx, path, "git", checkoutArgs...); checkoutErr != nil {
			return Result{Error: fmt.Sprintf("workspace.clone_create: checkout: %v", checkoutErr)}, nil
		} else if checkoutCode != 0 {
			return Result{Error: fmt.Sprintf("workspace.clone_create: checkout: %s", strings.TrimSpace(checkoutStderr))}, nil
		}
	} else if base != "" {
		if _, checkoutStderr, checkoutCode, checkoutErr := cliExec(ctx, path, "git", "checkout", base); checkoutErr != nil {
			return Result{Error: fmt.Sprintf("workspace.clone_create: checkout: %v", checkoutErr)}, nil
		} else if checkoutCode != 0 {
			return Result{Error: fmt.Sprintf("workspace.clone_create: checkout: %s", strings.TrimSpace(checkoutStderr))}, nil
		}
	}
	if err := writeCloneSentinel(path, map[string]any{
		"id":         id,
		"source":     source,
		"branch":     name,
		"base":       base,
		"session_id": strings.TrimSpace(worktreeStringArg(args, "session_id")),
		"created_at": time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return Result{Error: fmt.Sprintf("workspace.clone_create: write sentinel: %v", err)}, nil
	}
	return Result{Data: map[string]any{"ok": true, "id": id, "path": path, "branch": name, "root": root}}, nil
}

func cloneCleanupScan(ctx context.Context, repo string, args map[string]any) (Result, error) {
	source, repoErr := resolveWorktreeRepo(ctx, repo)
	if repoErr != "" {
		return Result{Error: "workspace.clone_cleanup_scan: " + repoErr}, nil
	}
	root := cloneRoot(source, worktreeStringArg(args, "root"))
	minAge := cloneMinAge(args)
	exclude := strings.TrimSpace(worktreeStringArg(args, "exclude"))
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return Result{Data: map[string]any{"ok": true, "root": root, "candidates": []map[string]any{}, "recommended_count": 0}}, nil
	}
	if err != nil {
		return Result{Error: fmt.Sprintf("workspace.clone_cleanup_scan: read %s: %v", root, err)}, nil
	}
	candidates := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name())
		meta, ok := readCloneSentinel(path)
		if !ok {
			continue
		}
		candidates = append(candidates, cloneCleanupCandidate(ctx, path, entry.Name(), meta, minAge, exclude))
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i]["recommended"] != candidates[j]["recommended"] {
			return candidates[i]["recommended"] == true
		}
		return fmt.Sprint(candidates[i]["id"]) < fmt.Sprint(candidates[j]["id"])
	})
	recommended := 0
	for _, c := range candidates {
		if c["recommended"] == true {
			recommended++
		}
	}
	return Result{Data: map[string]any{
		"ok":                true,
		"root":              root,
		"min_age_hours":     minAge.Hours(),
		"exclude":           exclude,
		"candidates":        candidates,
		"recommended_count": recommended,
	}}, nil
}

func cloneCleanupApply(ctx context.Context, repo string, args map[string]any) (Result, error) {
	source, repoErr := resolveWorktreeRepo(ctx, repo)
	if repoErr != "" {
		return Result{Error: "workspace.clone_cleanup_apply: " + repoErr}, nil
	}
	root := cloneRoot(source, worktreeStringArg(args, "root"))
	candidates, parseErr := cleanupCandidatesArg(args["candidates"])
	if parseErr != "" {
		return Result{Error: "workspace.clone_cleanup_apply: " + parseErr}, nil
	}
	var deleted []map[string]any
	var skipped []map[string]any
	var errs []string
	for _, raw := range candidates {
		c, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id := strings.TrimSpace(fmt.Sprint(c["id"]))
		path := strings.TrimSpace(fmt.Sprint(c["path"]))
		if c["recommended"] != true {
			skipped = append(skipped, map[string]any{"id": id, "path": path, "reason": "not recommended"})
			continue
		}
		if err := validateCloneCleanupPath(root, path); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		if _, ok := readCloneSentinel(path); !ok {
			errs = append(errs, fmt.Sprintf("%s: missing %s sentinel", id, cloneSentinelFile))
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			errs = append(errs, fmt.Sprintf("%s: remove: %v", id, err))
			continue
		}
		deleted = append(deleted, map[string]any{"id": id, "path": path})
	}
	data := map[string]any{"ok": len(errs) == 0, "deleted": deleted, "skipped": skipped}
	if len(errs) > 0 {
		data["errors"] = errs
		return Result{Data: data, Error: "workspace.clone_cleanup_apply: " + strings.Join(errs, "; ")}, nil
	}
	return Result{Data: data}, nil
}

func cleanupCandidate(ctx context.Context, repo string, wt worktreeInfo, base string, protected map[string]bool, exclude string, kind string) map[string]any {
	branch := wt.Branch
	id := filepath.Base(wt.Path)
	if wt.Path == "" {
		id = branch
	}
	merged := branchMerged(ctx, repo, branch, base)
	reason := "branch is merged into " + base
	recommended := true
	switch {
	case branch == "":
		recommended, reason = false, "detached worktree has no branch"
	case protected[branch]:
		recommended, reason = false, "protected branch"
	case wt.Dirty:
		recommended, reason = false, "worktree has uncommitted changes"
	case exclude != "" && cleanupCandidateMatches(wt, exclude):
		recommended, reason = false, "excluded by refinement"
	case !merged:
		recommended, reason = false, "branch is not merged into "+base
	}
	return map[string]any{
		"id":          id,
		"kind":        kind,
		"path":        wt.Path,
		"branch":      branch,
		"base":        base,
		"merged":      merged,
		"dirty":       wt.Dirty,
		"recommended": recommended,
		"reason":      reason,
	}
}

func cleanupCacheCandidates(wt worktreeInfo, exclude string) []map[string]any {
	if wt.Path == "" {
		return nil
	}
	var out []map[string]any
	_ = filepath.WalkDir(wt.Path, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		name := d.Name()
		if name == ".git" || name == ".worktrees" {
			return filepath.SkipDir
		}
		if !isDisposableCacheDir(name) {
			return nil
		}
		rel, _ := filepath.Rel(wt.Path, path)
		id := filepath.Base(wt.Path) + ":" + rel
		recommended := true
		reason := "generated cache directory"
		if exclude != "" && strings.Contains(strings.ToLower(id+" "+path+" "+wt.Branch), strings.ToLower(exclude)) {
			recommended, reason = false, "excluded by refinement"
		}
		out = append(out, map[string]any{
			"id":               id,
			"kind":             "cache",
			"path":             path,
			"branch":           wt.Branch,
			"worktree_path":    wt.Path,
			"relative_path":    rel,
			"size_bytes":       dirSize(path),
			"recommended":      recommended,
			"reason":           reason,
			"actions":          []string{"cache_remove"},
			"preserves_branch": true,
		})
		return filepath.SkipDir
	})
	return out
}

func isDisposableCacheDir(name string) bool {
	switch name {
	case ".cache", "go-cache", "go-build-cache", "go-mod-cache", "bf-73-go-build", "paired-task-work":
		return true
	default:
		return false
	}
}

func validateCacheCleanupPath(repo, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("empty cache path")
	}
	if !isDisposableCacheDir(filepath.Base(path)) {
		return fmt.Errorf("path %s is not a known generated cache directory", path)
	}
	root := filepath.Join(repo, ".worktrees")
	if strings.TrimSpace(repo) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve cwd: %v", err)
		}
		root = filepath.Join(cwd, ".worktrees")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve worktree root: %v", err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve cache path: %v", err)
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return fmt.Errorf("compare path: %v", err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("path %s is outside worktree root %s", path, root)
	}
	return nil
}

func makeTreeWritable(path string) error {
	return filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			return os.Chmod(p, 0o700)
		}
		return os.Chmod(p, 0o600)
	})
}

func dirSize(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func cleanupProtectedBranches(extra string) map[string]bool {
	out := map[string]bool{
		"main": true, "master": true, "develop": true, "dev": true,
		"trunk": true, "release": true,
	}
	for _, b := range strings.Split(extra, ",") {
		b = strings.TrimSpace(b)
		if b != "" {
			out[b] = true
		}
	}
	return out
}

func cleanupCandidateMatches(wt worktreeInfo, needle string) bool {
	needle = strings.ToLower(strings.TrimSpace(needle))
	if needle == "" {
		return false
	}
	hay := strings.ToLower(filepath.Base(wt.Path) + " " + wt.Path + " " + wt.Branch)
	return strings.Contains(hay, needle)
}

func cloneRoot(repo, override string) string {
	if strings.TrimSpace(override) != "" {
		if filepath.IsAbs(override) {
			return filepath.Clean(override)
		}
		return filepath.Join(repo, override)
	}
	return filepath.Join(repo, ".worktrees", "clones")
}

func writeCloneSentinel(path string, meta map[string]any) error {
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(filepath.Join(path, cloneSentinelFile), b, 0o644)
}

func readCloneSentinel(path string) (map[string]any, bool) {
	b, err := os.ReadFile(filepath.Join(path, cloneSentinelFile))
	if err != nil {
		return nil, false
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, false
	}
	return out, true
}

func cloneMinAge(args map[string]any) time.Duration {
	v := strings.TrimSpace(worktreeStringArg(args, "min_age_hours"))
	if v == "" {
		return 24 * time.Hour
	}
	var hours float64
	if _, err := fmt.Sscanf(v, "%f", &hours); err != nil || hours < 0 {
		return 24 * time.Hour
	}
	return time.Duration(hours * float64(time.Hour))
}

func cloneCleanupCandidate(ctx context.Context, path, id string, meta map[string]any, minAge time.Duration, exclude string) map[string]any {
	branchOut, _, branchCode, branchErr := cliExec(ctx, path, "git", "branch", "--show-current")
	branch := strings.TrimSpace(branchOut)
	if branchErr != nil || branchCode != 0 {
		branch = strings.TrimSpace(fmt.Sprint(meta["branch"]))
	}
	dirty := worktreeDirty(ctx, path)
	createdAt, _ := time.Parse(time.RFC3339, strings.TrimSpace(fmt.Sprint(meta["created_at"])))
	age := time.Duration(0)
	if !createdAt.IsZero() {
		age = time.Since(createdAt)
	}
	recommended := true
	reason := "clone is clean and older than minimum age"
	switch {
	case dirty:
		recommended, reason = false, "clone has uncommitted changes"
	case createdAt.IsZero():
		recommended, reason = false, "clone sentinel has no valid created_at"
	case age < minAge:
		recommended, reason = false, "clone is newer than minimum age"
	case exclude != "" && strings.Contains(strings.ToLower(id+" "+path+" "+branch), strings.ToLower(exclude)):
		recommended, reason = false, "excluded by refinement"
	}
	return map[string]any{
		"id":            id,
		"kind":          "clone",
		"path":          path,
		"branch":        branch,
		"dirty":         dirty,
		"created_at":    createdAt.Format(time.RFC3339),
		"age_hours":     age.Hours(),
		"recommended":   recommended,
		"reason":        reason,
		"actions":       []string{"clone_remove"},
		"isolated_refs": true,
	}
}

func validateCloneCleanupPath(root, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("empty clone path")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve root: %v", err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve path: %v", err)
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return fmt.Errorf("compare path: %v", err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("path %s is outside clone root %s", path, root)
	}
	return nil
}

func branchMerged(ctx context.Context, repo, branch, base string) bool {
	if strings.TrimSpace(branch) == "" {
		return false
	}
	_, _, code, err := cliExec(ctx, repo, "git", "merge-base", "--is-ancestor", branch, base)
	return err == nil && code == 0
}

func worktreeDirty(ctx context.Context, path string) bool {
	out, _, code, err := cliExec(ctx, path, "git", "status", "--porcelain")
	return err == nil && code == 0 && strings.TrimSpace(out) != ""
}

func worktreeStringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	v, _ := args[key].(string)
	return v
}

func cleanupCandidatesArg(raw any) ([]any, string) {
	switch v := raw.(type) {
	case nil:
		return nil, ""
	case []any:
		return v, ""
	case []map[string]any:
		out := make([]any, 0, len(v))
		for _, c := range v {
			out = append(out, c)
		}
		return out, ""
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return nil, ""
		}
		var out []any
		if err := json.Unmarshal([]byte(s), &out); err != nil {
			return nil, fmt.Sprintf("candidates must be a list or JSON list: %v", err)
		}
		return out, ""
	default:
		return nil, fmt.Sprintf("candidates must be a list, got %T", raw)
	}
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
