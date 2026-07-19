package queue

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func gitOutputEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("queue: git %s: %w%s", strings.Join(args, " "), err, outputSuffix(out))
	}
	return strings.TrimSpace(string(out)), nil
}

// WIP preservation keeps the protected checkout clean without ever losing
// data. Uncommitted or untracked work found in the protected checkout is
// captured as a commit on an immutable queue/preserved-wip/<stamp> branch —
// staged, unstaged, untracked, and deletions together, via a temporary index —
// and byte-completeness is proven before a single byte is removed from the
// checkout. A concurrent edit between capture and proof aborts with the
// checkout untouched.
//
// A path that cannot be read at all (permission denied, an unreadable
// embedded repo, ...) can never be captured — git itself cannot back up
// content it cannot open — so it is skipped rather than aborting capture of
// every other path in the checkout, and cleanup never touches it: leaving
// it exactly as it was on disk is the only way to guarantee it is not lost.

const preservedWIPPrefix = "queue/preserved-wip/"

// wipExcludedRoots are control/evidence directories that are never captured or
// cleaned by WIP preservation, whether or not the repository ignores them: the
// queue's own durable state lives under .capsules, and .artifacts/.context are
// the repo-convention evidence and scratch surfaces.
var wipExcludedRoots = []string{".capsules", ".artifacts", ".context", ".worktrees", ".kitsoki"}

func wipPathspec() []string {
	out := []string{"--", "."}
	for _, root := range wipExcludedRoots {
		out = append(out, ":(exclude)"+root)
	}
	return out
}

// PreserveWIP captures any dirty state of the git checkout at root onto a new
// preserved-WIP branch and restores the checkout to a clean HEAD, except for
// any path that could not be read at all — those are left completely
// untouched and returned in skipped so the caller can evidence them. It
// returns the created branch name, or "" when nothing capturable was dirty
// (which can still happen alongside a non-empty skipped: the only dirtiness
// found was on paths that could not even be inspected).
func PreserveWIP(ctx context.Context, root string, at time.Time) (branch string, skipped []string, err error) {
	tree, skipped, err := snapshotTree(ctx, root)
	if err != nil {
		return "", skipped, err
	}
	headTree, err := gitOutput(ctx, root, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return "", skipped, err
	}
	if tree == headTree {
		if len(skipped) == 0 {
			// Nothing content-bearing to preserve (e.g. mode-only churn);
			// still refuse to silently discard: leave the checkout alone.
			return "", nil, nil
		}
		// Every capturable path already matches HEAD; the only dirtiness is
		// on paths we could not even read. There is nothing to preserve and
		// nothing safe to clean — leave the checkout completely untouched.
		return "", skipped, nil
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	commit, err := gitOutput(ctx, root, "commit-tree", tree, "-p", "HEAD", "-m", "queue: preserved protected-checkout WIP captured at "+at.UTC().Format(time.RFC3339))
	if err != nil {
		return "", skipped, err
	}
	branch, err = preservedBranchName(ctx, root, at)
	if err != nil {
		return "", skipped, err
	}
	if _, err := gitOutput(ctx, root, "branch", branch, commit); err != nil {
		return "", skipped, err
	}
	// Byte-completeness proof: re-capture and require the identical tree and
	// the identical skip set. Any difference means the checkout changed
	// while we worked — abort with the checkout untouched (the preserved
	// branch stays; it is a superset of nothing and costs nothing).
	proof, proofSkipped, err := snapshotTree(ctx, root)
	if err != nil {
		return branch, skipped, err
	}
	if proof != tree || !equalPathSets(skipped, proofSkipped) {
		return branch, skipped, fmt.Errorf("queue: checkout changed during WIP capture; preserved branch %s retained, checkout untouched", branch)
	}
	if err := restoreCapturedPaths(ctx, root, skipped); err != nil {
		return branch, skipped, fmt.Errorf("queue: preserved branch %s created but checkout restore failed: %w", branch, err)
	}
	remainingStatus, err := porcelainStatus(ctx, root)
	if err != nil {
		return branch, skipped, err
	}
	if unexpected := setDiff(porcelainPaths(remainingStatus), skipped); len(unexpected) > 0 {
		return branch, skipped, fmt.Errorf("queue: checkout not clean after WIP preservation: %s", strings.Join(unexpected, ", "))
	}
	return branch, skipped, nil
}

// snapshotTree captures whatever is dirty in the checkout right now.
// Recomputing the dirty-path set fresh on every call (rather than reusing a
// list computed earlier) is what lets the byte-completeness proof in
// PreserveWIP detect a concurrent edit: a path that appeared, disappeared,
// or changed between the two calls changes what gets captured.
func snapshotTree(ctx context.Context, root string) (tree string, skipped []string, err error) {
	status, err := porcelainStatus(ctx, root)
	if err != nil {
		return "", nil, err
	}
	return captureTree(ctx, root, porcelainPaths(status))
}

// porcelainStatus returns the raw, untrimmed `git status --porcelain
// --untracked-files=all` output. It must not go through gitOutput's
// strings.TrimSpace: porcelain v1's first status column can legitimately be
// a space (e.g. " M base.txt" for a modified-but-not-staged file), and
// TrimSpace-ing the whole multi-line blob strips exactly that leading space
// off the first line only, shifting porcelainPaths' fixed-column parse by
// one byte for that one entry (confirmed by repro: "base.txt" parsed as
// "ase.txt", silently skipping the real file and leaving stale HEAD content
// in its place on the preserved branch).
func porcelainStatus(ctx context.Context, root string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"status", "--porcelain", "--untracked-files=all"}, wipPathspec()...)...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("queue: git status: %w%s", err, outputSuffix(out))
	}
	return string(out), nil
}

// captureTree builds a tree of the given paths' full working state (staged +
// unstaged + untracked + deletions) using a temporary index, leaving the
// real index untouched. Each path is added individually rather than via a
// single `git add -A`: git add -A makes zero index progress on ANY path when
// even one path in the batch cannot be read (confirmed by repro: a single
// permission-denied file aborts the whole `add -A` with no partial index
// update at all), so one unreadable path used to abort capture of every
// other dirty path in the checkout too. Paths that fail to index are
// returned in skipped rather than aborting the rest.
func captureTree(ctx context.Context, root string, paths []string) (tree string, skipped []string, err error) {
	idx, err := gitOutput(ctx, root, "rev-parse", "--git-path", "queue-preserve-index")
	if err != nil {
		return "", nil, err
	}
	env := []string{"GIT_INDEX_FILE=" + idx}
	if _, err := gitOutputEnv(ctx, root, env, "read-tree", "HEAD"); err != nil {
		return "", nil, err
	}
	for _, path := range paths {
		if _, err := gitOutputEnv(ctx, root, env, "add", "-A", "--", path); err != nil {
			skipped = append(skipped, path)
		}
	}
	sort.Strings(skipped)
	tree, err = gitOutputEnv(ctx, root, env, "write-tree")
	return tree, skipped, err
}

// restoreCapturedPaths resets every currently-dirty path not in skip back to
// its HEAD state (or removes it, if HEAD never had it) — the same end state
// `git reset --hard && git clean -fd` would produce, but scoped away from
// skip. That scoping is load-bearing: both `git reset --hard` and
// `git clean -fd` will happily overwrite or delete a file whose permissions
// forbid reading it (removing/recreating a path only needs write permission
// on its parent directory, not on the file itself — confirmed by repro), so
// a blanket reset/clean would silently destroy exactly the content this
// function exists to protect.
func restoreCapturedPaths(ctx context.Context, root string, skip []string) error {
	status, err := porcelainStatus(ctx, root)
	if err != nil {
		return err
	}
	skipSet := make(map[string]bool, len(skip))
	for _, p := range skip {
		skipSet[p] = true
	}
	for _, path := range porcelainPaths(status) {
		if skipSet[path] {
			continue
		}
		if _, err := gitOutput(ctx, root, "cat-file", "-e", "HEAD:"+path); err == nil {
			// Tracked in HEAD: checkout restores both the index entry and
			// the worktree content in one step, correctly reverting
			// modified, staged-modified, and deleted-from-worktree cases.
			if _, err := gitOutput(ctx, root, "checkout", "-q", "HEAD", "--", path); err != nil {
				return err
			}
			continue
		}
		// Not in HEAD: staged-new or plain untracked. Unstage if present in
		// the index, then remove the worktree file if it still exists.
		if _, err := gitOutput(ctx, root, "rm", "-f", "-q", "--cached", "--ignore-unmatch", "--", path); err != nil {
			return err
		}
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// porcelainPaths extracts the path from each `git status --porcelain
// --untracked-files=all` line. Standard porcelain v1 format is always
// exactly two status characters, one space, then the path (C-quoted by git
// itself when it contains unusual bytes); a rename/copy line additionally
// carries " -> " before the new path, which is the path this function
// returns — the destination is what actually needs capturing, the vacated
// source path shows up dirty (as a deletion) in its own right if unstaged.
func porcelainPaths(status string) []string {
	var paths []string
	for _, line := range strings.Split(status, "\n") {
		if len(line) < 4 {
			continue
		}
		path := line[3:]
		if idx := strings.Index(path, " -> "); idx >= 0 {
			path = path[idx+4:]
		}
		path = strings.Trim(path, "\"")
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

func equalPathSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sa, sb := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(sa)
	sort.Strings(sb)
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}

// setDiff returns the members of all that are not present in exclude.
func setDiff(all, exclude []string) []string {
	excludeSet := make(map[string]bool, len(exclude))
	for _, p := range exclude {
		excludeSet[p] = true
	}
	var out []string
	for _, p := range all {
		if !excludeSet[p] {
			out = append(out, p)
		}
	}
	return out
}

func preservedBranchName(ctx context.Context, root string, at time.Time) (string, error) {
	base := preservedWIPPrefix + at.UTC().Format("20060102T150405Z")
	name := base
	for i := 1; ; i++ {
		if _, err := gitOutput(ctx, root, "rev-parse", "--verify", "--quiet", "refs/heads/"+name); err != nil {
			return name, nil
		}
		name = fmt.Sprintf("%s-%d", base, i)
		if i > 100 {
			return "", fmt.Errorf("queue: cannot allocate preserved WIP branch name near %s", base)
		}
	}
}

// syncProtectedCheckout hard-resets the protected checkout when (and only
// when) the just-updated target ref is its checked-out branch, so the worktree
// matches the new protected tip after the ref CAS. The ref has already moved,
// so cleanliness is judged against the old target tree: only a worktree that
// still exactly matches the pre-CAS tip (callers preserve WIP first) is
// eligible for a hard sync.
// tolerateUntracked lists paths that are expected to still be untracked —
// WIP preservation's own skipped (unreadable) paths, deliberately left
// exactly as found — so their presence must not itself look like a race
// that appeared mid-finalization. Anything untracked beyond that set still
// refuses the hard sync exactly as before.
func syncProtectedCheckout(ctx context.Context, root, target, oldSHA string, tolerateUntracked []string) error {
	head, err := gitOutput(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || head != strings.TrimPrefix(target, "refs/heads/") {
		return nil // detached or different branch checked out; ref move is enough
	}
	if strings.TrimSpace(oldSHA) != "" {
		if _, err := gitOutput(ctx, root, "diff-index", "--quiet", oldSHA, "--"); err != nil {
			return fmt.Errorf("queue: protected worktree does not match pre-finalization tip %s; refusing hard sync", oldSHA)
		}
	}
	untrackedOut, err := gitOutput(ctx, root, append([]string{"ls-files", "--others", "--exclude-standard"}, wipPathspec()...)...)
	if err != nil {
		return err
	}
	var untracked []string
	for _, line := range strings.Split(untrackedOut, "\n") {
		if line != "" {
			untracked = append(untracked, line)
		}
	}
	if unexpected := setDiff(untracked, tolerateUntracked); len(unexpected) > 0 {
		return fmt.Errorf("queue: untracked files appeared in protected checkout during finalization; refusing hard sync: %s", strings.Join(unexpected, ", "))
	}
	_, err = gitOutput(ctx, root, "reset", "-q", "--hard", head)
	return err
}
