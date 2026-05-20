// Package host — deterministic git-commit step that runs after every
// meta-mode turn that edited files in the story tree.
//
// # Why this lives in the controller, not in Claude's prompt
//
// Meta-mode (story-author, /meta edit, …) lets Claude edit files in
// the story tree via Edit/Write tools. Without this file, the edits
// would sit uncommitted in the working tree and the user would have
// to remember to commit them by hand. The brief is "when meta
// completes a change, it should do a git commit; if someone wants
// updates in the same convo just amend the existing commit" — that
// has to be wired into the Go path because anything depending on
// Claude remembering to emit "<<<commit>>>" tokens is non-deterministic
// by construction.
//
// # The amend decision is encoded in HEAD's commit trailer
//
// CommitChangedFiles stamps every commit it creates with a trailer:
//
//	Kitsoki-Meta-Session: <chat_id>
//
// On the next apply during the same chat, we read HEAD's commit body
// and look for the same trailer. If present → `git commit --amend
// --no-edit`. If absent (or a different chat_id, or HEAD has moved
// past the trailer for any reason — a `git reset`, a user-typed
// commit, a `git push --force` from another worktree, anything) →
// fresh commit. The decision is stateless: there is no per-session
// "did we commit yet" bool to keep in sync with the filesystem. The
// trailer IS the bookkeeping.
//
// # Best-effort: never fail the turn
//
// The file edits are already on disk when CommitChangedFiles runs;
// failing the meta-mode turn because git refused (pre-commit hook, no
// repo, network filesystem, …) would corrupt the user's mental model:
// they'd see "turn failed" but the edits would in fact have landed.
// So CommitChangedFiles returns (sha, amended, err) and the caller
// (metamode.sendLocked) surfaces the error in SendResult.CommitError
// without failing the turn. The TUI can display "applied but commit
// failed: <reason>" — the user knows the files moved and the commit
// didn't.
package host

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"kitsoki/internal/app"
)

// metaSessionTrailer is the git-commit trailer key that ties a commit
// to a meta-mode chat. The full trailer is
// "Kitsoki-Meta-Session: <chat_id>". Picked to be unmistakable
// (mixed case + the "Kitsoki" namespace) so a sibling tool's trailer
// never accidentally matches.
const metaSessionTrailer = "Kitsoki-Meta-Session"

// commitRunner is the seam tests use to swap the real git invocation
// for a fake. Production points at runGitCommit.
var commitRunner = runGitCommit

// CommitChangedFiles stamps a meta-mode edit into a git commit. Called
// by metamode.sendLocked after the agent's Edit/Write tools have
// touched files in the story tree (detected via a pre/post snapshot
// diff).
//
// First call per chat_id creates a new commit; subsequent calls with
// the same chat_id amend HEAD (provided HEAD still carries this
// chat's trailer — if the user interleaved a manual commit, the amend
// decision falls back to a fresh commit so we never rewrite their
// work).
//
// Args:
//   - anyDir:      any directory inside the target git repo. Usually
//                  the story root. Used to discover repo top-level.
//   - paths:       absolute paths of files to stage. Empty → no-op
//                  (returns "", false, nil).
//   - summary:     commit subject. Empty → falls back to a placeholder.
//   - chatID:      meta-mode chat identifier; becomes the trailer value.
//   - appFilePath: optional path to the story's app.yaml. If non-empty,
//                  CommitChangedFiles runs app.Load against it BEFORE
//                  staging; on validation failure, NO commit is made
//                  and the load error is returned. This prevents
//                  broken edits (e.g. an undeclared intent referenced
//                  from an `on:` arc) from being amended into history.
//                  Pass "" to skip validation (useful when the change
//                  doesn't include the manifest).
//
// Returns (sha, amended, err). On any git failure, sha may be empty
// and err carries the diagnostic. Callers MUST NOT treat err as a
// failure of the underlying file edit — the files are already on disk
// when this runs; the commit step is purely a side effect.
//
// Skip behaviour: if anyDir is not inside a git repo, returns
// ("", false, nil) silently. "No repo" is a legitimate state for CLI
// authoring against a loose YAML file, not a failure.
func CommitChangedFiles(ctx context.Context, anyDir string, paths []string, summary, chatID, appFilePath string) (sha string, amended bool, err error) {
	if strings.TrimSpace(chatID) == "" {
		return "", false, fmt.Errorf("CommitChangedFiles: empty chat_id")
	}
	if len(paths) == 0 {
		return "", false, nil
	}

	// Validation gate — run BEFORE git add so a broken manifest never
	// touches the index. Without this gate the user's meta-mode agent
	// could make a syntactically valid but semantically broken edit
	// (e.g. add an `on:` arc referencing an undeclared intent) and we
	// would amend the broken state into HEAD; next reload would fail
	// and the operator would be stuck with a broken commit on their
	// branch. See trace at /tmp/kitsoki-dogfood-trace.log around
	// 2026-05-19 12:41 UTC for the production shape of this bug.
	if appFilePath != "" {
		if _, loadErr := app.Load(appFilePath); loadErr != nil {
			return "", false, fmt.Errorf("CommitChangedFiles: post-edit AppDef does not validate (skipping commit so broken state isn't pinned): %w", loadErr)
		}
	}

	repoRoot, err := commitRunner(ctx, "rev-parse-toplevel", anyDir, nil)
	if err != nil {
		// Most common cause: anyDir is not inside a git repo. Treat as
		// a benign skip — the edits landed, just no version control.
		return "", false, nil
	}
	repoRoot = strings.TrimSpace(repoRoot)

	amend := headHasSessionMarker(ctx, repoRoot, chatID)

	if _, err := commitRunner(ctx, "add", repoRoot, paths); err != nil {
		return "", amend, fmt.Errorf("CommitChangedFiles: git add: %w", err)
	}

	var commitErr error
	if amend {
		_, commitErr = commitRunner(ctx, "commit-amend", repoRoot, nil)
	} else {
		message := buildCommitMessage(summary, chatID)
		_, commitErr = commitRunner(ctx, "commit-new", repoRoot, []string{message})
	}
	if commitErr != nil {
		return "", amend, fmt.Errorf("CommitChangedFiles: git commit: %w", commitErr)
	}

	shaOut, shaErr := commitRunner(ctx, "rev-parse-head", repoRoot, nil)
	if shaErr != nil {
		return "", amend, fmt.Errorf("CommitChangedFiles: git rev-parse: %w", shaErr)
	}
	return strings.TrimSpace(shaOut), amend, nil
}

// headHasSessionMarker reads HEAD's commit body and reports whether it
// carries this chat's trailer. Any error reading HEAD (no commits yet,
// detached HEAD oddities, etc.) maps to false — i.e. "fresh commit",
// the safe default.
func headHasSessionMarker(ctx context.Context, repoRoot, chatID string) bool {
	out, err := commitRunner(ctx, "log-head-body", repoRoot, nil)
	if err != nil {
		return false
	}
	marker := metaSessionTrailer + ": " + chatID
	return strings.Contains(out, marker)
}

// buildCommitMessage renders the subject + trailer for a fresh
// commit. Empty summaries get a placeholder subject so the commit
// isn't headed by just the trailer (git would still accept it but the
// log would be unreadable).
func buildCommitMessage(summary, chatID string) string {
	subject := strings.TrimSpace(summary)
	if subject == "" {
		subject = "meta-mode: applied edit"
	}
	return fmt.Sprintf("%s\n\n%s: %s\n", subject, metaSessionTrailer, chatID)
}

// runGitCommit is the default commitRunner. It maps a small set of
// named operations to git subcommands so the test seam can intercept
// the operation without needing to parse argv. Real-world git calls
// here are dirt-cheap (local filesystem only); no need for concurrency.
//
// Ops:
//
//	rev-parse-toplevel  args ignored;        out = repo root
//	rev-parse-head      args ignored;        out = HEAD SHA
//	log-head-body       args ignored;        out = HEAD's commit body
//	add                 args = list of paths to stage
//	commit-amend        args ignored;        amends HEAD with --no-edit
//	commit-new          args = [message];    creates a new commit
//
// dir is repo root (or, for rev-parse-toplevel, anyDir). git -C handles
// the rest.
func runGitCommit(ctx context.Context, op, dir string, args []string) (string, error) {
	var cmdArgs []string
	switch op {
	case "rev-parse-toplevel":
		cmdArgs = []string{"-C", dir, "rev-parse", "--show-toplevel"}
	case "rev-parse-head":
		cmdArgs = []string{"-C", dir, "rev-parse", "HEAD"}
	case "log-head-body":
		cmdArgs = []string{"-C", dir, "log", "-1", "--format=%B", "HEAD"}
	case "add":
		cmdArgs = append([]string{"-C", dir, "add", "--"}, args...)
	case "commit-amend":
		cmdArgs = []string{"-C", dir, "commit", "--amend", "--no-edit"}
	case "commit-new":
		if len(args) == 0 {
			return "", fmt.Errorf("runGitCommit: commit-new requires a message")
		}
		cmdArgs = []string{"-C", dir, "commit", "-m", args[0]}
	default:
		return "", fmt.Errorf("runGitCommit: unknown op %q", op)
	}
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w (output: %s)", op, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// OverrideCommitRunner replaces the commitRunner seam (see above) with
// a stub. Used by tests that want to assert the post-apply commit step
// was invoked with specific arguments without actually shelling out to
// git. Returns a restore function the caller must invoke to revert.
//
// The package-test seam used to live in authoring_tools_seam.go
// alongside OverrideApplyRunner / OverrideProposeRunner — those went
// away with the legacy authoring surface; only this one survives.
func OverrideCommitRunner(fn func(ctx context.Context, op, dir string, args []string) (string, error)) func() {
	prev := commitRunner
	commitRunner = fn
	return func() { commitRunner = prev }
}
