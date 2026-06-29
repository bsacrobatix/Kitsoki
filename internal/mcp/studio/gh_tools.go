package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"kitsoki/internal/host"
)

// gh_tools.go — a read (and comment) surface over the GitHub CLI.
//
// issue.create files an issue and inbox.sync_github pulls assigned issues INTO a
// handle's inbox, but the everyday GitHub reads an agent needs while developing —
// "list the open issues", "show me this PR's body + changed files + diff" (the
// bake-off needs the PR's own regression oracle), "leave a comment" — had no MCP
// path and forced a `gh` shell-out. These wrap `gh` (argv mode, via the shared
// host.RunHandler) so they stay inside the studio. gh must be authenticated on
// the host (the same precondition issue.create / inbox.sync_github already rely
// on). No LLM.
//
// gh.issues / gh.pr_view are pure reads (available read-only); gh.comment posts,
// so a read-only server omits it.

// registerGHTools wires the gh.* tools.
func (srv *Server) registerGHTools() {
	mcpsdk.AddTool(srv.mcpSrv, &mcpsdk.Tool{
		Name:        "gh.issues",
		Description: "List GitHub issues via `gh issue list --json`. {dir? (a repo checkout to infer the repo from; default the server cwd), repo? (owner/name override), state? (open|closed|all; default open), assignee? (e.g. @me), search? (a gh search query), limit? (default 30)} → {ok, issues[{number, title, state, url, labels[], assignees[]}]}. Read-only.",
	}, srv.handleGHIssues)

	mcpsdk.AddTool(srv.mcpSrv, &mcpsdk.Tool{
		Name:        "gh.pr_view",
		Description: "View a pull request via `gh pr view --json` (+ optional `gh pr diff`). {number (required), dir?, repo?, include_diff? (also fetch the unified diff)} → {ok, pr:{number, title, state, url, body, files[{path, additions, deletions}]}, diff?}. Use it to read a PR's body and the exact files/diff it changed (e.g. a filed bug's own regression test). Read-only.",
	}, srv.handleGHPRView)

	if srv.readOnly {
		return
	}

	mcpsdk.AddTool(srv.mcpSrv, &mcpsdk.Tool{
		Name:        "gh.comment",
		Description: "Comment on a GitHub issue or PR via `gh issue/pr comment`. {number (required), body (required), on? (issue|pr; default issue), dir?, repo?} → {ok, url}. Mutating (posts to GitHub).",
	}, srv.handleGHComment)
}

// ── gh.issues ─────────────────────────────────────────────────────────────────

// GHIssuesArgs is the input to gh.issues.
type GHIssuesArgs struct {
	Dir      string `json:"dir,omitempty"`
	Repo     string `json:"repo,omitempty"`
	State    string `json:"state,omitempty"`
	Assignee string `json:"assignee,omitempty"`
	Search   string `json:"search,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

// GHIssuesOK is the gh.issues result. Issues is the parsed `gh --json` array,
// passed through as generic JSON so the field set tracks gh without a rebuild.
type GHIssuesOK struct {
	OK     bool `json:"ok"`
	Issues any  `json:"issues"`
}

func (srv *Server) handleGHIssues(ctx context.Context, req *mcpsdk.CallToolRequest, args GHIssuesArgs) (*mcpsdk.CallToolResult, any, error) {
	limit := args.Limit
	if limit <= 0 {
		limit = 30
	}
	ghArgs := []string{"issue", "list", "--json", "number,title,state,url,labels,assignees", "--limit", strconv.Itoa(limit)}
	if args.State != "" {
		ghArgs = append(ghArgs, "--state", args.State)
	}
	if args.Assignee != "" {
		ghArgs = append(ghArgs, "--assignee", args.Assignee)
	}
	if args.Search != "" {
		ghArgs = append(ghArgs, "--search", args.Search)
	}
	if args.Repo != "" {
		ghArgs = append(ghArgs, "--repo", args.Repo)
	}
	out, exit, err := ghRun(ctx, args.Dir, ghArgs...)
	if rerr := ghErr("gh.issues", out, exit, err); rerr != nil {
		return rerr, nil, nil
	}
	parsed, perr := parseJSONAny(out)
	if perr != nil {
		return buildToolError(ErrBadRequest, fmt.Sprintf("gh.issues: parse gh output: %v", perr)), nil, nil
	}
	return nil, GHIssuesOK{OK: true, Issues: parsed}, nil
}

// ── gh.pr_view ────────────────────────────────────────────────────────────────

// GHPRViewArgs is the input to gh.pr_view.
type GHPRViewArgs struct {
	Number      int    `json:"number"`
	Dir         string `json:"dir,omitempty"`
	Repo        string `json:"repo,omitempty"`
	IncludeDiff bool   `json:"include_diff,omitempty"`
}

// GHPRViewOK is the gh.pr_view result.
type GHPRViewOK struct {
	OK   bool   `json:"ok"`
	PR   any    `json:"pr"`
	Diff string `json:"diff,omitempty"`
}

func (srv *Server) handleGHPRView(ctx context.Context, req *mcpsdk.CallToolRequest, args GHPRViewArgs) (*mcpsdk.CallToolResult, any, error) {
	if args.Number <= 0 {
		return buildToolError(ErrBadRequest, "gh.pr_view: number is required"), nil, nil
	}
	num := strconv.Itoa(args.Number)
	ghArgs := []string{"pr", "view", num, "--json", "number,title,state,url,body,files"}
	if args.Repo != "" {
		ghArgs = append(ghArgs, "--repo", args.Repo)
	}
	out, exit, err := ghRun(ctx, args.Dir, ghArgs...)
	if rerr := ghErr("gh.pr_view", out, exit, err); rerr != nil {
		return rerr, nil, nil
	}
	parsed, perr := parseJSONAny(out)
	if perr != nil {
		return buildToolError(ErrBadRequest, fmt.Sprintf("gh.pr_view: parse gh output: %v", perr)), nil, nil
	}
	res := GHPRViewOK{OK: true, PR: parsed}
	if args.IncludeDiff {
		diffArgs := []string{"pr", "diff", num}
		if args.Repo != "" {
			diffArgs = append(diffArgs, "--repo", args.Repo)
		}
		dout, dexit, derr := ghRun(ctx, args.Dir, diffArgs...)
		if rerr := ghErr("gh.pr_view (diff)", dout, dexit, derr); rerr != nil {
			return rerr, nil, nil
		}
		res.Diff = dout
	}
	return nil, res, nil
}

// ── gh.comment ────────────────────────────────────────────────────────────────

// GHCommentArgs is the input to gh.comment.
type GHCommentArgs struct {
	Number int    `json:"number"`
	Body   string `json:"body"`
	On     string `json:"on,omitempty"`
	Dir    string `json:"dir,omitempty"`
	Repo   string `json:"repo,omitempty"`
}

// GHCommentOK is the gh.comment result; URL is the posted comment's URL (gh
// echoes it on stdout).
type GHCommentOK struct {
	OK  bool   `json:"ok"`
	URL string `json:"url"`
}

func (srv *Server) handleGHComment(ctx context.Context, req *mcpsdk.CallToolRequest, args GHCommentArgs) (*mcpsdk.CallToolResult, any, error) {
	if args.Number <= 0 {
		return buildToolError(ErrBadRequest, "gh.comment: number is required"), nil, nil
	}
	if args.Body == "" {
		return buildToolError(ErrBadRequest, "gh.comment: body is required"), nil, nil
	}
	kind := args.On
	if kind == "" {
		kind = "issue"
	}
	if kind != "issue" && kind != "pr" {
		return buildToolError(ErrBadRequest, "gh.comment: on must be \"issue\" or \"pr\""), nil, nil
	}
	ghArgs := []string{kind, "comment", strconv.Itoa(args.Number), "--body", args.Body}
	if args.Repo != "" {
		ghArgs = append(ghArgs, "--repo", args.Repo)
	}
	out, exit, err := ghRun(ctx, args.Dir, ghArgs...)
	if rerr := ghErr("gh.comment", out, exit, err); rerr != nil {
		return rerr, nil, nil
	}
	return nil, GHCommentOK{OK: true, URL: strings.TrimSpace(out)}, nil
}

// ── gh execution + helpers ────────────────────────────────────────────────────

// ghRun runs the gh CLI in argv mode via the shared host.RunHandler. dir is the
// working directory (a repo checkout, so gh can infer the repo); empty runs in
// the server cwd. Returns combined stdout, exit code, and an infra error.
func ghRun(ctx context.Context, dir string, args ...string) (stdout string, exit int, err error) {
	anyArgs := make([]any, len(args))
	for i, a := range args {
		anyArgs[i] = a
	}
	hargs := map[string]any{"cmd": "gh", "args": anyArgs}
	if dir != "" {
		hargs["cwd"] = dir
	}
	res, err := host.RunHandler(ctx, hargs)
	if err != nil {
		return "", -1, err
	}
	if res.Error != "" && res.Data == nil {
		return "", -1, errors.New(res.Error)
	}
	exit, _ = res.Data["exit_code"].(int)
	stdout, _ = res.Data["stdout"].(string)
	return stdout, exit, nil
}

// ghErr maps a gh invocation to a tool error when it could not start or exited
// non-zero (gh prints actionable messages — not authenticated, no such PR — on
// stderr, which RunHandler folds into stdout).
func ghErr(tool, out string, exit int, err error) *mcpsdk.CallToolResult {
	if err != nil {
		return buildToolError(ErrBadRequest, fmt.Sprintf("%s: %v", tool, err))
	}
	if exit != 0 {
		return buildToolError(ErrBadRequest, fmt.Sprintf("%s: gh exited %d: %s", tool, exit, strings.TrimSpace(out)))
	}
	return nil
}

// parseJSONAny unmarshals gh's --json output into a generic value so the result
// passes the field set through verbatim.
func parseJSONAny(s string) (any, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, err
	}
	return v, nil
}
