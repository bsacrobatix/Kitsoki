package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/host"
	"kitsoki/internal/jobs"
)

func TestGitopsGHAgentGateRequiresIndependentVerify(t *testing.T) {
	result := map[string]any{
		"gh_agent_enqueue_status":         "queued",
		"gh_agent_enqueued_count":         1,
		"gh_agent_drain_status":           "drained",
		"gh_agent_failed_count":           0,
		"gh_agent_active_count":           0,
		"gh_agent_done_count":             1,
		"gh_agent_missing_evidence_count": 0,
		"gh_agent_missing_triage_count":   0,
		"gh_agent_missing_verify_count":   0,
		"gh_agent_missing_run_url_count":  0,
	}
	if !gitopsGHAgentGateOK(result) {
		t.Fatalf("complete gh-agent result should pass")
	}
	result["gh_agent_missing_verify_count"] = 1
	if gitopsGHAgentGateOK(result) {
		t.Fatalf("missing independent verification must fail the gh-agent gate")
	}
	result["gh_agent_missing_verify_count"] = 0
	result["gh_agent_missing_triage_count"] = 1
	if gitopsGHAgentGateOK(result) {
		t.Fatalf("missing triage preflight evidence must fail the gh-agent gate")
	}
}

func TestGitopsEnqueueFixesClaimsGitHubIssueAndPersistsState(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	runDir := t.TempDir()
	findings := map[string]any{
		"run_id": "run-claim",
		"items": []any{
			map[string]any{
				"id":            "finding-claim",
				"kind":          "issue",
				"title":         "Parallel agents should see in-flight work",
				"summary":       "Claim comments and job context must be native gitops state.",
				"scenario":      "bugfix",
				"status":        "open",
				"origin":        "observed",
				"severity":      "high",
				"evidence_path": "evidence/bugfix.md",
				"github_issue": map[string]any{
					"url":    "https://github.com/o/r/issues/77",
					"repo":   "o/r",
					"number": "77",
				},
			},
		},
	}
	if err := gitopsWriteJSONFile(filepath.Join(runDir, "findings.json"), findings); err != nil {
		t.Fatalf("write findings: %v", err)
	}

	var commentBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues/77/comments":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode comment: %v", err)
			}
			commentBody, _ = payload["body"].(string)
			writeJSON(w, map[string]any{"html_url": "https://github.com/o/r/issues/77#issuecomment-claim"})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()
	restoreAPI := host.SetGitHubAPIForTest(srv.URL, srv.Client())
	defer restoreAPI()
	restoreExec := host.SetExecRunnerForTest(func(ctx context.Context, d, name string, args ...string) (string, string, int, error) {
		t.Errorf("gitops enqueue claim must use native GitHub APIs, got exec: %s %s", name, strings.Join(args, " "))
		return "", "", 1, nil
	})
	defer restoreExec()

	dbPath := filepath.Join(runDir, "gh-agent.sqlite")
	result, err := gitopsEnqueueFixes(context.Background(), runDir, dbPath, "o/r", "stories/bugfix")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if intValue(result, "gh_agent_enqueued_count") != 1 || intValue(result, "gh_agent_claim_count") != 1 {
		t.Fatalf("enqueue result = %+v", result)
	}
	if stringValue(result, "gh_agent_claim_status") != "claimed" {
		t.Fatalf("claim status = %q", stringValue(result, "gh_agent_claim_status"))
	}
	for _, want := range []string{"kitsoki-autofix-claim", "finding-claim", "stories/bugfix", "github:o/r/issue/77"} {
		if !strings.Contains(commentBody, want) {
			t.Fatalf("claim body missing %q:\n%s", want, commentBody)
		}
	}

	updated, err := gitopsReadJSONFile(filepath.Join(runDir, "findings.json"))
	if err != nil {
		t.Fatalf("read findings: %v", err)
	}
	issue := mapValue(gitopsFindingsItems(updated)[0], "github_issue")
	if stringValue(issue, "claim_comment_url") != "https://github.com/o/r/issues/77#issuecomment-claim" {
		t.Fatalf("claim URL not persisted: %+v", issue)
	}
	if stringValue(issue, "claim_job_id") == "" || stringValue(issue, "claimed_by") != "kitsoki gitops autonomous-fix" {
		t.Fatalf("claim metadata not persisted: %+v", issue)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	store, err := jobs.NewGHJobStore(db)
	if err != nil {
		t.Fatalf("job store: %v", err)
	}
	job, err := store.GetJob(context.Background(), stringValue(mapSliceValue(result, "gh_agent_jobs")[0], "job_id"))
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.OriginRef != "github:o/r/issue/77" || job.Story != "stories/bugfix" {
		t.Fatalf("job = %+v", job)
	}
	if job.Metadata["ticket_title"] != "Parallel agents should see in-flight work" {
		t.Fatalf("ticket title metadata = %q", job.Metadata["ticket_title"])
	}
	for _, want := range []string{"Claim comments and job context", "bugfix", "evidence/bugfix.md", "https://github.com/o/r/issues/77"} {
		if !strings.Contains(job.Metadata["ticket_body"], want) {
			t.Fatalf("ticket body metadata missing %q:\n%s", want, job.Metadata["ticket_body"])
		}
	}
	if job.Metadata["ticket_source_mode"] != "remote" || job.Metadata["ticket_source_ref"] != "https://github.com/o/r/issues/77" {
		t.Fatalf("ticket source metadata = %+v", job.Metadata)
	}
	if got := mapSliceValue(result, "gh_agent_jobs")[0]["triage_context"]; got != true {
		t.Fatalf("triage_context = %v, want true", got)
	}
}

func TestGitopsCloseoutFixedIssuesCommentsClosesAndPersistsState(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	runDir := t.TempDir()
	findings := map[string]any{
		"run_id": "run-1",
		"items": []any{
			map[string]any{
				"id":            "finding-1",
				"kind":          "issue",
				"title":         "Done room should close tickets",
				"summary":       "manual close-out should be story-owned",
				"scenario":      "bugfix",
				"severity":      "high",
				"evidence_path": "trace.md",
				"status":        "blocked",
				"origin":        "observed",
				"github_issue": map[string]any{
					"url":    "https://github.com/o/r/issues/42",
					"repo":   "o/r",
					"number": "42",
				},
			},
		},
	}
	if err := gitopsWriteJSONFile(filepath.Join(runDir, "findings.json"), findings); err != nil {
		t.Fatalf("write findings: %v", err)
	}

	var commentBody string
	var closedState string
	commentCalls := 0
	closeCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues/42/comments":
			commentCalls++
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode comment: %v", err)
			}
			commentBody, _ = payload["body"].(string)
			writeJSON(w, map[string]any{"html_url": "https://github.com/o/r/issues/42#issuecomment-9"})
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/o/r/issues/42":
			closeCalls++
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode transition: %v", err)
			}
			closedState, _ = payload["state"].(string)
			writeJSON(w, map[string]any{"state": closedState})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()
	restoreAPI := host.SetGitHubAPIForTest(srv.URL, srv.Client())
	defer restoreAPI()
	restoreExec := host.SetExecRunnerForTest(func(ctx context.Context, d, name string, args ...string) (string, string, int, error) {
		t.Errorf("gitops closeout must use native GitHub APIs, got exec: %s %s", name, strings.Join(args, " "))
		return "", "", 1, nil
	})
	defer restoreExec()

	status := map[string]any{
		"gh_agent_drained_jobs": []any{
			map[string]any{
				"origin_ref": "github:o/r/issue/42",
				"job_id":     "job-42",
				"state":      "done",
				"run_url":    "https://agent.example/run/job-42",
				"assets": []any{
					map[string]any{"name": "fix-report.md", "url": "https://agent.example/run/job-42/assets/fix-report.md"},
					map[string]any{"name": "independent-verify.md", "url": "https://agent.example/run/job-42/assets/independent-verify.md"},
				},
			},
		},
	}
	result, err := gitopsCloseoutFixedIssues(context.Background(), runDir, "o/r", status)
	if err != nil {
		t.Fatalf("closeout: %v", err)
	}
	if stringValue(result, "issue_closeout_status") != "closed" || intValue(result, "issue_closeout_count") != 1 {
		t.Fatalf("closeout result = %+v", result)
	}
	if closedState != "closed" {
		t.Fatalf("closed state = %q, want closed", closedState)
	}
	if commentCalls != 1 || closeCalls != 1 {
		t.Fatalf("first closeout calls: comments=%d closes=%d, want 1/1", commentCalls, closeCalls)
	}
	for _, want := range []string{"kitsoki-fixed-in", "job-42", "independent-verify.md", "https://agent.example/run/job-42"} {
		if !strings.Contains(commentBody, want) {
			t.Fatalf("comment body missing %q:\n%s", want, commentBody)
		}
	}
	updated, err := gitopsReadJSONFile(filepath.Join(runDir, "findings.json"))
	if err != nil {
		t.Fatalf("read findings: %v", err)
	}
	item := gitopsFindingsItems(updated)[0]
	issue := mapValue(item, "github_issue")
	if stringValue(item, "status") != "fixed" || stringValue(issue, "state") != "closed" {
		t.Fatalf("finding/issue not marked fixed/closed: item=%+v issue=%+v", item, issue)
	}
	if stringValue(issue, "closeout_comment_url") != "https://github.com/o/r/issues/42#issuecomment-9" {
		t.Fatalf("closeout comment url not persisted: %+v", issue)
	}
	closeout := mapValue(updated, "issue_closeout")
	if stringValue(closeout, "status") != "closed" || intValue(closeout, "count") != 1 {
		t.Fatalf("top-level closeout summary not persisted: %+v", closeout)
	}
	comments := mapSliceValue(issue, "comments")
	if len(comments) != 1 || !strings.Contains(stringValue(comments[0], "body"), "kitsoki-fixed-in") {
		t.Fatalf("fixed marker comment not persisted: %+v", comments)
	}

	second, err := gitopsCloseoutFixedIssues(context.Background(), runDir, "o/r", status)
	if err != nil {
		t.Fatalf("second closeout: %v", err)
	}
	if stringValue(second, "issue_closeout_status") != "closed" || intValue(second, "issue_closeout_count") != 1 {
		t.Fatalf("second closeout result = %+v", second)
	}
	if intValue(second, "issue_already_closed_count") != 1 {
		t.Fatalf("second closeout did not report already closed issue: %+v", second)
	}
	if commentCalls != 1 || closeCalls != 1 {
		t.Fatalf("second closeout must not repeat GitHub mutations: comments=%d closes=%d", commentCalls, closeCalls)
	}
	updatedAgain, err := gitopsReadJSONFile(filepath.Join(runDir, "findings.json"))
	if err != nil {
		t.Fatalf("read second findings: %v", err)
	}
	secondComments := mapSliceValue(mapValue(gitopsFindingsItems(updatedAgain)[0], "github_issue"), "comments")
	if len(secondComments) != 1 {
		t.Fatalf("second closeout duplicated comments: %+v", secondComments)
	}
}
