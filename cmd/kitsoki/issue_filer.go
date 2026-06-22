package main

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	studio "kitsoki/internal/mcp/studio"
)

// ghIssueFiler is the production studio.IssueFiler: it shells to the GitHub CLI
// (`gh`) to create the issue. gh is the authenticated path the operator already
// has, so the studio inherits that auth with no token plumbing of its own. The
// seam keeps the studio package free of any exec/network dependency — tests
// inject a fake instead (no `gh`, no network).
//
// Best-effort label bootstrap: source-autonomous may not exist in the repo yet,
// and `gh issue create` rejects an unknown label. We `gh label create --force`
// it first (idempotent: --force upserts), ignoring the result so a repo where it
// already exists, or a gh without label perms, still proceeds — gh will surface
// any genuinely-fatal label problem on the create call.
func ghIssueFiler(ctx context.Context, req studio.IssueRequest) (studio.IssueResult, error) {
	for _, label := range req.Labels {
		if label == "source-autonomous" {
			args := []string{"label", "create", label,
				"--color", "BFD4F2",
				"--description", "Filed by an autonomous agent",
				"--force"}
			if req.Repo != "" {
				args = append(args, "--repo", req.Repo)
			}
			_ = exec.CommandContext(ctx, "gh", args...).Run() // best-effort
		}
	}

	args := []string{"issue", "create", "--title", req.Title, "--body", req.Body}
	if req.Repo != "" {
		args = append(args, "--repo", req.Repo)
	}
	for _, label := range req.Labels {
		args = append(args, "--label", label)
	}

	out, err := exec.CommandContext(ctx, "gh", args...).Output()
	if err != nil {
		// Surface gh's stderr — it explains auth / unknown-label / repo errors.
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return studio.IssueResult{}, fmt.Errorf("gh issue create: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return studio.IssueResult{}, fmt.Errorf("gh issue create: %w", err)
	}

	// gh prints the new issue's URL on stdout, e.g.
	// https://github.com/owner/repo/issues/123
	url := strings.TrimSpace(string(out))
	return studio.IssueResult{URL: url, Number: issueNumberFromURL(url)}, nil
}

// issueNumberFromURL extracts the trailing issue number from a gh-printed issue
// URL (".../issues/123"). Returns 0 when the URL doesn't end in a number.
func issueNumberFromURL(url string) int {
	idx := strings.LastIndex(url, "/")
	if idx < 0 || idx == len(url)-1 {
		return 0
	}
	n, err := strconv.Atoi(url[idx+1:])
	if err != nil {
		return 0
	}
	return n
}
