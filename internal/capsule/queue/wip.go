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
		if headType, err := gitOutput(ctx, root, "cat-file", "-t", "HEAD:"+path); err == nil {
			// Tracked in HEAD: checkout restores both the index entry and
			// the worktree content in one step, correctly reverting
			// modified, staged-modified, and deleted-from-worktree cases.
			//
			// Exception: when HEAD recorded path as a plain file (blob) but
			// the checkout now has a non-empty directory there, `git
			// checkout -q HEAD -- path` recursively removes that directory
			// to recreate the file — silently destroying whatever is inside
			// it that isn't the tracked file itself (confirmed by repro: an
			// untracked nested file vanishes with no warning, and the only
			// sign anything went wrong is an incidental ENOTDIR surfacing
			// later from unrelated cleanup of a sibling porcelain entry).
			// This function cannot prove that content is preserved anywhere
			// else, so — per this file's own rule of never destroying
			// unread content — it refuses the destructive checkout and
			// fails loudly instead, leaving the directory completely
			// untouched. A HEAD directory (tree) colliding with a worktree
			// directory is not this hazard: checkout only updates the
			// tracked entries within it and never recursively wipes it.
			if headType == "blob" {
				full := filepath.Join(root, filepath.FromSlash(path))
				collides, entries, statErr := nonEmptyDirectoryAt(full)
				if statErr != nil {
					return statErr
				}
				if collides {
					return &RestoreTypeCollisionError{Path: path, Entries: entries}
				}
			}
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

// RestoreTypeCollisionError is returned by restoreCapturedPaths (and
// therefore surfaces through PreserveWIP) when a captured path's HEAD
// content is a tracked file but the checkout now holds a non-empty
// directory there. Destructively restoring the tracked file would require
// recursively removing that directory, and nothing in this function can
// prove the directory's content is preserved anywhere else — so the restore
// is refused instead, and the path is left exactly as found. When this
// arose through PreserveWIP, the checkout's full pre-restore state
// (including this directory) was already captured onto the preserved-WIP
// branch before restoreCapturedPaths ever ran; this error just means the
// automatic cleanup step won't also try to reconcile the collision, and it
// must be resolved by hand.
type RestoreTypeCollisionError struct {
	Path    string
	Entries int
}

func (e *RestoreTypeCollisionError) Error() string {
	return fmt.Sprintf("queue: refusing to restore %s: HEAD has a file at this path but the checkout now has a non-empty directory there (%d entries); leaving it untouched rather than silently deleting it — inspect the directory (and, if this ran through PreserveWIP, the preserved-WIP branch) before resolving manually", e.Path, e.Entries)
}

// nonEmptyDirectoryAt reports whether full currently exists on disk as a
// directory containing at least one entry. A path that does not exist, or
// exists but is not a directory, or is an empty directory, is not a
// collision: there is nothing there a destructive checkout could destroy.
func nonEmptyDirectoryAt(full string) (collides bool, entries int, err error) {
	info, err := os.Lstat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return false, 0, nil
		}
		return false, 0, err
	}
	if !info.IsDir() {
		return false, 0, nil
	}
	list, err := os.ReadDir(full)
	if err != nil {
		return false, 0, err
	}
	return len(list) > 0, len(list), nil
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

// syncProtectedCheckout brings the protected checkout up to the new protected
// tip when (and only when) the just-updated target ref is its checked-out
// branch. The ref CAS moves the ref only — it never touches the index or the
// worktree — so without this step the checkout keeps serving the pre-landing
// tree, which git then reports as a *staged reversal of the commit that just
// landed* (including staged deletions of every file the landing added). A
// plain `git commit` in that state silently backs the landing out, so a
// successful landing must never leave it.
//
// The sync is deliberately scoped to the paths the landing actually changed
// (oldSHA..HEAD) and is applied from HEAD, rather than being a blanket
// `git reset --hard`. Two independent hazards make the blanket form wrong:
//
//   - Eligibility could never be satisfied. The old form gated the reset on
//     `git diff-index --quiet <oldSHA> --` over the WHOLE tree, with none of
//     wipPathspec's exclusions — while PreserveWIP excludes those same roots
//     from both capture and cleanup, so anything permanently dirty under
//     .kitsoki/.artifacts/.context (in the real protected checkout, the
//     regenerated .kitsoki/bin/claude and .kitsoki/bin/codex launch-policy
//     shims) left the checkout permanently ineligible. Every landing then
//     refused the sync and left exactly the staged reversal described above.
//     Judging only the paths the landing touched removes that whole class of
//     false ineligibility: a path the landing did not touch cannot possibly
//     need syncing, so its dirtiness is none of this function's business.
//
//   - Succeeding was destructive too. `git reset --hard` rewrites every
//     tracked path, including the excluded-root paths PreserveWIP
//     deliberately declined to capture — so on the rare clean-enough
//     checkout it silently destroyed the one category of WIP that has no
//     preserved-branch copy to recover from. Touching only landed paths is
//     the narrowest mutation that still achieves the acceptance criterion.
//
// Where the two genuinely collide — the landing changed a path the operator
// also has dirty, or added a file the operator independently created
// untracked — there is no safe winner to pick, so this refuses loudly and
// names the path instead of overwriting local work or leaving a reversal.
//
// This takes no tolerated-untracked set, unlike the blanket form it replaces.
// WIP preservation's own skipped (unreadable) paths only matter here if the
// landing also changed one, and then they are a named conflict like any other
// collision — never something to tolerate and overwrite, since a path that
// could not be read was never captured onto a preserved branch either.
// Elsewhere in the tree they are now simply unreachable: a scoped sync cannot
// touch a path the landing did not change. (The old blanket untracked refusal
// protected nothing on its own either — `git reset --hard` does not remove
// untracked files. Its only real effect was to abort the sync and leave the
// reversal behind.)
// It returns a short summary of what it actually did, which the caller records
// in the finalization log. That summary is not cosmetic. The previous version
// returned a bare nil both when it had synced the checkout and when it had
// deliberately done nothing (detached HEAD, or a different branch checked out),
// and the caller only appended to the log on error — so a successful sync and a
// skipped sync were indistinguishable in the recorded evidence. That silence is
// a large part of why this defect survived ~100 finalizations without being
// localized: the logs could not answer "did the sync run?". Every outcome now
// names itself.
func syncProtectedCheckout(ctx context.Context, root, target, oldSHA string) (string, error) {
	head, err := gitOutput(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || head != strings.TrimPrefix(target, "refs/heads/") {
		// Detached or a different branch checked out; the ref move is enough.
		return "checkout not synced: target is not the checked-out branch", nil
	}
	oldSHA = strings.TrimSpace(oldSHA)
	if oldSHA == "" {
		// Without the pre-finalization tip there is no way to tell stale
		// pre-landing content apart from genuine local WIP, and therefore no
		// way to prove any mutation is safe. Refuse loudly rather than
		// hard-resetting the checkout on a guess.
		return "", fmt.Errorf("queue: cannot sync protected checkout without the pre-finalization tip; refusing to touch the worktree")
	}
	landed, err := landedPaths(ctx, root, oldSHA)
	if err != nil {
		return "", err
	}
	if len(landed) == 0 {
		return "checkout already at the landed tip; no path needed syncing", nil
	}
	conflicts, err := landedPathConflicts(ctx, root, oldSHA, landed)
	if err != nil {
		return "", err
	}
	if len(conflicts) > 0 {
		return "", &ProtectedSyncConflictError{Paths: conflicts, OldSHA: oldSHA}
	}
	for _, path := range landed {
		if _, err := gitOutput(ctx, root, "cat-file", "-e", "HEAD:"+path); err == nil {
			// Present in the new tip: checkout updates the index entry and the
			// worktree content together, covering both "the landing modified
			// it" and "the landing added it".
			if _, err := gitOutput(ctx, root, "checkout", "-q", "HEAD", "--", path); err != nil {
				return "", err
			}
			continue
		}
		// Absent from the new tip: the landing deleted it. Drop the stale
		// index entry and remove the stale worktree file.
		if _, err := gitOutput(ctx, root, "rm", "-f", "-q", "--cached", "--ignore-unmatch", "--", path); err != nil {
			return "", err
		}
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(path))); err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}
	return fmt.Sprintf("checkout synced to the landed tip across %d path(s)", len(landed)), nil
}

// landedPaths lists every path that differs between the pre-finalization tip
// and the new protected tip now at HEAD — exactly the paths whose stale
// content would otherwise read as a reversal of the landing. HEAD is used as
// the post-landing side (rather than the CAS's reported new target) because
// HEAD is what a subsequent `git commit` in this checkout would diff against,
// and matching HEAD is the property that has to hold.
func landedPaths(ctx context.Context, root, oldSHA string) ([]string, error) {
	out, err := gitOutput(ctx, root, "diff", "--name-only", "--no-renames", oldSHA, "HEAD")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// landedPathConflicts returns the landed paths that cannot be synced without
// destroying local work. A landed path is safe when the checkout still holds
// the pre-finalization content there (plain stale content — the normal case,
// and what the sync exists to fix) or already holds the new content. Anything
// else means the operator's own edit sits on a path the landing also changed,
// and no automatic choice between them is defensible. An untracked file at a
// landed path is the same collision in its added-file form: syncing would
// overwrite content that has no preserved-branch copy.
func landedPathConflicts(ctx context.Context, root, oldSHA string, landed []string) ([]string, error) {
	var conflicts []string
	for _, path := range landed {
		if _, err := gitOutput(ctx, root, "diff-index", "--quiet", oldSHA, "--", path); err == nil {
			continue // matches the pre-finalization tip: stale, safe to advance
		}
		if _, err := gitOutput(ctx, root, "diff-index", "--quiet", "HEAD", "--", path); err == nil {
			continue // already matches the new tip: nothing to do
		}
		conflicts = append(conflicts, path)
	}
	untrackedOut, err := gitOutput(ctx, root, append([]string{"ls-files", "--others", "--exclude-standard", "--"}, landed...)...)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(untrackedOut, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			conflicts = append(conflicts, line)
		}
	}
	sort.Strings(conflicts)
	return dedupePaths(conflicts), nil
}

func dedupePaths(paths []string) []string {
	var out []string
	for i, p := range paths {
		if i == 0 || p != paths[i-1] {
			out = append(out, p)
		}
	}
	return out
}

// ProtectedSyncConflictError reports that the protected checkout could not be
// advanced to the newly landed tip because the operator holds local changes on
// paths the landing itself changed. Both states are real work and picking
// either silently is a data-loss bug — overwriting loses the local edit,
// skipping leaves a staged reversal of the landing that a plain `git commit`
// would apply. The named paths must be reconciled by hand.
type ProtectedSyncConflictError struct {
	Paths  []string
	OldSHA string
}

func (e *ProtectedSyncConflictError) Error() string {
	return fmt.Sprintf("queue: refusing to sync the protected checkout: %d path(s) changed by the landing also hold local changes since the pre-finalization tip %s: %s; the landing itself is applied and the protected ref has moved, but these paths were left untouched — reconcile them by hand (inspect any queue/preserved-wip/* branch from this finalization) before committing in this checkout",
		len(e.Paths), e.OldSHA, strings.Join(e.Paths, ", "))
}
