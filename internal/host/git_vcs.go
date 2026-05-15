// Package host — host.git — git/gh-backed VCS provider.
//
// Implements the `vcs` host_interface declared in
// docs/proposals/notes/dev-story-implementation-contract.md §2.2.  One
// prefix-fallback handler dispatches the seven vcs ops via the `op`
// arg.  Local git ops shell out to the `git` CLI; PR ops shell out to
// `gh`, which is optional — if missing or unauthenticated the handler
// returns a clean Result.Error rather than crashing.
//
// All exec calls go through the shared `cliExec` seam declared in
// cli_exec.go so tests don't shell out for real.
package host

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// runRealCommand executes name+args with the given working directory
// and captures stdout/stderr/exit-code.  An infrastructure error
// (binary not found, etc.) is returned as err; non-zero exits surface
// via exitCode + stderr only.
func runRealCommand(ctx context.Context, dir, name string, args ...string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	err := cmd.Run()
	if err == nil {
		return stdoutBuf.String(), stderrBuf.String(), 0, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return stdoutBuf.String(), stderrBuf.String(), exitErr.ExitCode(), nil
	}
	return stdoutBuf.String(), stderrBuf.String(), -1, err
}

// GitVCSHandler implements host.git (prefix-fallback for all 7 ops).
//
// Required args:
//   - op (string): one of branch, diff, commit, push, open_pr, pr_status, pr_comment.
//
// Common optional args:
//   - workdir (string): working directory for the git command; defaults to cwd.
//
// Per-op input/output follows the contract §2.2.
func GitVCSHandler(ctx context.Context, args map[string]any) (Result, error) {
	op, _ := args["op"].(string)
	op = strings.TrimSpace(op)
	if op == "" {
		return Result{Error: "host.git: op argument is required"}, nil
	}
	workdir, _ := args["workdir"].(string)

	switch op {
	case "branch":
		return gitBranch(ctx, workdir, args)
	case "diff":
		return gitDiff(ctx, workdir, args)
	case "commit":
		return gitCommit(ctx, workdir, args)
	case "push":
		return gitPush(ctx, workdir, args)
	case "open_pr":
		return ghOpenPR(ctx, workdir, args)
	case "pr_status":
		return ghPRStatus(ctx, workdir, args)
	case "pr_comment":
		return ghPRComment(ctx, workdir, args)
	default:
		return Result{Error: fmt.Sprintf("host.git: unknown op %q", op)}, nil
	}
}

// ─── git ops (always shell to `git`) ───────────────────────────────────────

func gitBranch(ctx context.Context, workdir string, args map[string]any) (Result, error) {
	name, _ := args["name"].(string)
	base, _ := args["base"].(string)
	if strings.TrimSpace(name) == "" {
		return Result{Error: "git.branch: name argument is required"}, nil
	}
	gitArgs := []string{"checkout", "-b", name}
	if base != "" {
		gitArgs = append(gitArgs, base)
	}
	_, stderr, code, err := cliExec(ctx, workdir, "git", gitArgs...)
	if err != nil {
		return Result{Error: fmt.Sprintf("git.branch: exec: %v", err)}, nil
	}
	if code != 0 {
		return Result{Error: fmt.Sprintf("git.branch: %s", strings.TrimSpace(stderr))}, nil
	}
	return Result{Data: map[string]any{"ok": true, "branch": name}}, nil
}

func gitDiff(ctx context.Context, workdir string, _ map[string]any) (Result, error) {
	stdout, stderr, code, err := cliExec(ctx, workdir, "git", "diff", "--patch")
	if err != nil {
		return Result{Error: fmt.Sprintf("git.diff: exec: %v", err)}, nil
	}
	if code != 0 {
		return Result{Error: fmt.Sprintf("git.diff: %s", strings.TrimSpace(stderr))}, nil
	}
	// Also surface the list of changed files for `bind:` ergonomics.
	filesOut, _, _, _ := cliExec(ctx, workdir, "git", "diff", "--name-only")
	var files []any
	for _, ln := range strings.Split(strings.TrimSpace(filesOut), "\n") {
		if ln != "" {
			files = append(files, ln)
		}
	}
	return Result{Data: map[string]any{"diff": stdout, "files": files}}, nil
}

func gitCommit(ctx context.Context, workdir string, args map[string]any) (Result, error) {
	message, _ := args["message"].(string)
	if strings.TrimSpace(message) == "" {
		return Result{Error: "git.commit: message argument is required"}, nil
	}
	// Optional files list; when empty, fall back to `git commit -a`.
	filesAny, _ := args["files"].([]any)
	if len(filesAny) > 0 {
		addArgs := []string{"add", "--"}
		for _, f := range filesAny {
			if s, ok := f.(string); ok && s != "" {
				addArgs = append(addArgs, s)
			}
		}
		if _, stderr, code, err := cliExec(ctx, workdir, "git", addArgs...); err != nil || code != 0 {
			return Result{Error: fmt.Sprintf("git.commit: stage: %s", strings.TrimSpace(stderr))}, nil
		}
	}
	commitArgs := []string{"commit", "-m", message}
	if len(filesAny) == 0 {
		// No explicit files → assume -a so authors can use the
		// fast-path on a dirty tree.
		commitArgs = []string{"commit", "-a", "-m", message}
	}
	_, stderr, code, err := cliExec(ctx, workdir, "git", commitArgs...)
	if err != nil {
		return Result{Error: fmt.Sprintf("git.commit: exec: %v", err)}, nil
	}
	if code != 0 {
		return Result{Error: fmt.Sprintf("git.commit: %s", strings.TrimSpace(stderr))}, nil
	}
	sha, _, _, _ := cliExec(ctx, workdir, "git", "rev-parse", "HEAD")
	return Result{Data: map[string]any{
		"ok":  true,
		"sha": strings.TrimSpace(sha),
	}}, nil
}

func gitPush(ctx context.Context, workdir string, args map[string]any) (Result, error) {
	remote, _ := args["remote"].(string)
	if remote == "" {
		remote = "origin"
	}
	// Push the current HEAD.  `git push -u <remote> HEAD` makes the
	// upstream tracking branch on first push, no-ops on subsequent
	// pushes.
	_, stderr, code, err := cliExec(ctx, workdir, "git", "push", "-u", remote, "HEAD")
	if err != nil {
		return Result{Error: fmt.Sprintf("git.push: exec: %v", err)}, nil
	}
	if code != 0 {
		return Result{Error: fmt.Sprintf("git.push: %s", strings.TrimSpace(stderr))}, nil
	}
	// Best-effort URL discovery (remote URL).  Not all push targets
	// have a fetchable URL (e.g. local file remotes); empty string is
	// fine and the contract just says `url: string`.
	urlOut, _, _, _ := cliExec(ctx, workdir, "git", "remote", "get-url", remote)
	return Result{Data: map[string]any{
		"ok":  true,
		"url": strings.TrimSpace(urlOut),
	}}, nil
}

// ─── gh ops (optional — clean error if `gh` absent) ────────────────────────

// ghAvailable reports whether the `gh` binary is on PATH.  A negative
// answer turns the four PR ops into a clean domain error so the YAML
// `on_error:` arc fires instead of crashing.
func ghAvailable(ctx context.Context, workdir string) bool {
	_, _, code, err := cliExec(ctx, workdir, "gh", "--version")
	return err == nil && code == 0
}

func ghOpenPR(ctx context.Context, workdir string, args map[string]any) (Result, error) {
	if !ghAvailable(ctx, workdir) {
		return Result{Error: "git.open_pr: gh CLI not available — install github.com/cli/cli"}, nil
	}
	title, _ := args["title"].(string)
	body, _ := args["body"].(string)
	base, _ := args["base"].(string)
	if strings.TrimSpace(title) == "" {
		return Result{Error: "git.open_pr: title argument is required"}, nil
	}
	ghArgs := []string{"pr", "create", "--title", title, "--body", body}
	if base != "" {
		ghArgs = append(ghArgs, "--base", base)
	}
	stdout, stderr, code, err := cliExec(ctx, workdir, "gh", ghArgs...)
	if err != nil {
		return Result{Error: fmt.Sprintf("git.open_pr: exec: %v", err)}, nil
	}
	if code != 0 {
		return Result{Error: fmt.Sprintf("git.open_pr: %s", strings.TrimSpace(stderr))}, nil
	}
	// `gh pr create` prints the PR URL on the last line of stdout.
	url := lastNonEmptyLine(stdout)
	prID := prIDFromURL(url)
	return Result{Data: map[string]any{
		"ok":    true,
		"url":   url,
		"pr_id": prID,
	}}, nil
}

func ghPRStatus(ctx context.Context, workdir string, args map[string]any) (Result, error) {
	if !ghAvailable(ctx, workdir) {
		return Result{Error: "git.pr_status: gh CLI not available"}, nil
	}
	prID, _ := args["pr_id"].(string)
	if strings.TrimSpace(prID) == "" {
		return Result{Error: "git.pr_status: pr_id argument is required"}, nil
	}
	stdout, stderr, code, err := cliExec(ctx, workdir, "gh", "pr", "view", prID, "--json", "state,statusCheckRollup")
	if err != nil {
		return Result{Error: fmt.Sprintf("git.pr_status: exec: %v", err)}, nil
	}
	if code != 0 {
		return Result{Error: fmt.Sprintf("git.pr_status: %s", strings.TrimSpace(stderr))}, nil
	}
	// Surface the raw JSON envelope on `state` + `checks`; callers may
	// JSON-parse via host.run's stdout_json convention if they want
	// structured access.  Wave 1 just hands the raw blob back.
	return Result{Data: map[string]any{
		"state":    stdout,
		"checks":   []any{},
		"comments": []any{},
	}}, nil
}

func ghPRComment(ctx context.Context, workdir string, args map[string]any) (Result, error) {
	if !ghAvailable(ctx, workdir) {
		return Result{Error: "git.pr_comment: gh CLI not available"}, nil
	}
	prID, _ := args["pr_id"].(string)
	body, _ := args["body"].(string)
	if strings.TrimSpace(prID) == "" || strings.TrimSpace(body) == "" {
		return Result{Error: "git.pr_comment: pr_id and body are required"}, nil
	}
	_, stderr, code, err := cliExec(ctx, workdir, "gh", "pr", "comment", prID, "--body", body)
	if err != nil {
		return Result{Error: fmt.Sprintf("git.pr_comment: exec: %v", err)}, nil
	}
	if code != 0 {
		return Result{Error: fmt.Sprintf("git.pr_comment: %s", strings.TrimSpace(stderr))}, nil
	}
	return Result{Data: map[string]any{"ok": true}}, nil
}

// prIDFromURL extracts the trailing `/pull/<N>` segment.  Returns "" on
// any URL that doesn't match — the caller's domain error rendering then
// surfaces the empty pr_id explicitly.
func prIDFromURL(url string) string {
	url = strings.TrimSpace(url)
	idx := strings.LastIndex(url, "/pull/")
	if idx < 0 {
		return ""
	}
	return url[idx+len("/pull/"):]
}
