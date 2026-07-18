package queue

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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
// preserved-WIP branch and restores the checkout to a clean HEAD. It returns
// the created branch name, or "" when the checkout was already clean.
func PreserveWIP(ctx context.Context, root string, at time.Time) (string, error) {
	status, err := gitOutput(ctx, root, append([]string{"status", "--porcelain", "--untracked-files=all"}, wipPathspec()...)...)
	if err != nil {
		return "", err
	}
	if status == "" {
		return "", nil
	}
	tree, err := captureTree(ctx, root)
	if err != nil {
		return "", err
	}
	headTree, err := gitOutput(ctx, root, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return "", err
	}
	if tree == headTree {
		// Nothing content-bearing to preserve (e.g. mode-only churn); still
		// refuse to silently discard: leave the checkout alone.
		return "", fmt.Errorf("queue: checkout reports dirty but capture tree equals HEAD; refusing to clean")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	commit, err := gitOutput(ctx, root, "commit-tree", tree, "-p", "HEAD", "-m", "queue: preserved protected-checkout WIP captured at "+at.UTC().Format(time.RFC3339))
	if err != nil {
		return "", err
	}
	branch, err := preservedBranchName(ctx, root, at)
	if err != nil {
		return "", err
	}
	if _, err := gitOutput(ctx, root, "branch", branch, commit); err != nil {
		return "", err
	}
	// Byte-completeness proof: re-capture and require the identical tree. Any
	// difference means the checkout changed while we worked — abort with the
	// checkout untouched (the preserved branch stays; it is a superset of
	// nothing and costs nothing).
	proof, err := captureTree(ctx, root)
	if err != nil {
		return "", err
	}
	if proof != tree {
		return "", fmt.Errorf("queue: checkout changed during WIP capture; preserved branch %s retained, checkout untouched", branch)
	}
	if _, err := gitOutput(ctx, root, "reset", "-q", "--hard", "HEAD"); err != nil {
		return branch, fmt.Errorf("queue: preserved branch %s created but checkout restore failed: %w", branch, err)
	}
	cleanArgs := []string{"clean", "-fd"}
	for _, rootDir := range wipExcludedRoots {
		cleanArgs = append(cleanArgs, "-e", rootDir)
	}
	if _, err := gitOutput(ctx, root, cleanArgs...); err != nil {
		return branch, fmt.Errorf("queue: preserved branch %s created but untracked cleanup failed: %w", branch, err)
	}
	remaining, err := gitOutput(ctx, root, append([]string{"status", "--porcelain", "--untracked-files=all"}, wipPathspec()...)...)
	if err != nil {
		return branch, err
	}
	if remaining != "" {
		return branch, fmt.Errorf("queue: checkout not clean after WIP preservation: %s", remaining)
	}
	return branch, nil
}

// captureTree builds a tree of the full working state (staged + unstaged +
// untracked + deletions) using a temporary index, leaving the real index
// untouched.
func captureTree(ctx context.Context, root string) (string, error) {
	idx, err := gitOutput(ctx, root, "rev-parse", "--git-path", "queue-preserve-index")
	if err != nil {
		return "", err
	}
	env := []string{"GIT_INDEX_FILE=" + idx}
	if _, err := gitOutputEnv(ctx, root, env, "read-tree", "HEAD"); err != nil {
		return "", err
	}
	if _, err := gitOutputEnv(ctx, root, env, append([]string{"add", "-A"}, wipPathspec()...)...); err != nil {
		return "", err
	}
	return gitOutputEnv(ctx, root, env, "write-tree")
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
func syncProtectedCheckout(ctx context.Context, root, target, oldSHA string) error {
	head, err := gitOutput(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || head != strings.TrimPrefix(target, "refs/heads/") {
		return nil // detached or different branch checked out; ref move is enough
	}
	if strings.TrimSpace(oldSHA) != "" {
		if _, err := gitOutput(ctx, root, "diff-index", "--quiet", oldSHA, "--"); err != nil {
			return fmt.Errorf("queue: protected worktree does not match pre-finalization tip %s; refusing hard sync", oldSHA)
		}
	}
	untracked, err := gitOutput(ctx, root, append([]string{"ls-files", "--others", "--exclude-standard"}, wipPathspec()...)...)
	if err != nil {
		return err
	}
	if untracked != "" {
		return fmt.Errorf("queue: untracked files appeared in protected checkout during finalization; refusing hard sync")
	}
	_, err = gitOutput(ctx, root, "reset", "-q", "--hard", head)
	return err
}
