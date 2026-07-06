package host_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/host"
)

// writeFindingsBundle lays down a minimal product-journey run bundle:
//   - finding-1: credible issue with a resolvable screenshot + a scenario
//     evidence artifact on disk
//   - finding-2: seeded demo issue (never filed)
//   - finding-3: strength (never filed)
//   - finding-4: credible issue whose evidence ref does not resolve
//   - finding-5: blocked issue (capture gap, never filed)
func writeFindingsBundle(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	must := func(name string, v any) {
		t.Helper()
		data, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "evidence"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "evidence", "shot.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "evidence", "trace.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	must("run.json", map[string]any{
		"run_id":  "run-777",
		"seed":    "demo",
		"project": map[string]any{"id": "vscode"},
		"persona": map[string]any{"id": "ide-first-engineer"},
		"scenarios": []any{
			map[string]any{"id": "dependency-bugfix", "task_prompt": "Fix the flaky dependency test via the bugfix story."},
			map[string]any{"id": "project-onboarding", "task_prompt": "Onboard the repo."},
		},
	})
	must("driver-plan.json", map[string]any{
		"scenarios": []any{
			map[string]any{
				"scenario":         "dependency-bugfix",
				"task_prompt":      "Fix the flaky dependency test via the bugfix story.",
				"success_criteria": []any{"The bugfix story reaches a verified fix.", "Evidence is captured for the fix diff."},
			},
		},
	})
	must("driver-journal.json", map[string]any{
		"items": []any{
			map[string]any{
				"scenario":  "dependency-bugfix",
				"summary":   "Drove the bugfix story to the verify gate",
				"mcp_tools": []any{"session_drive", "visual_snapshot"},
			},
			map[string]any{"scenario": "project-onboarding", "summary": "Onboarding attempt"},
		},
	})
	must("evidence.json", map[string]any{
		"items": []any{
			map[string]any{"scenario": "dependency-bugfix", "kind": "session_trace", "path": "evidence/trace.json", "status": "captured"},
			map[string]any{"scenario": "dependency-bugfix", "kind": "planned_only", "path": "evidence/never.png", "status": "planned"},
		},
	})
	must("findings.json", map[string]any{
		"run_id": "run-777",
		"items": []any{
			map[string]any{
				"id": "finding-1", "kind": "issue", "origin": "observed",
				"title":         "verify gate loops forever",
				"summary":       "The verify gate re-entered itself after the fix landed.",
				"scenario":      "dependency-bugfix",
				"severity":      "high",
				"status":        "open",
				"evidence_path": "evidence/shot.png",
			},
			map[string]any{
				"id": "finding-2", "kind": "issue", "origin": "seeded",
				"title": "seeded demo issue", "summary": "seeded", "scenario": "dependency-bugfix",
			},
			map[string]any{
				"id": "finding-3", "kind": "strength", "origin": "observed",
				"title": "great deck", "summary": "deck is nice",
			},
			map[string]any{
				"id": "finding-4", "kind": "issue",
				"title":         "onboarding config generator emits stale commands",
				"summary":       "Generated onboarding config referenced commands that do not exist in the repo.",
				"scenario":      "project-onboarding",
				"severity":      "high",
				"status":        "open",
				"evidence_path": "cassette://product-journey/run-777/missing/none.json",
			},
			map[string]any{
				"id": "finding-5", "kind": "issue",
				"title":         "onboarding blocked on missing cassette",
				"summary":       "Scenario could not be captured without a cassette.",
				"scenario":      "project-onboarding",
				"severity":      "high",
				"status":        "blocked",
				"evidence_path": "cassette://product-journey/run-777/missing/none.json",
			},
		},
	})
	return dir
}

// githubFindingsAPI stubs the native GitHub API: releases succeed, each issue
// create returns an incrementing issue URL, and issue bodies/upload counts are
// recorded for assertions.
func githubFindingsAPI(t *testing.T, issueBodies *[]string, uploads *int, failFirstIssue bool) func() {
	t.Helper()
	t.Setenv("GH_TOKEN", "test-token")
	issue := 100
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/search/issues":
			writeJSON(w, map[string]any{"items": []map[string]any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/releases/tags/kitsoki-artifacts":
			writeJSON(w, map[string]any{"upload_url": "http://" + r.Host + "/uploads/assets{?name,label}"})
		case r.Method == http.MethodPost && r.URL.Path == "/uploads/assets":
			*uploads++
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			title, _ := payload["title"].(string)
			body, _ := payload["body"].(string)
			*issueBodies = append(*issueBodies, title+"\n"+body)
			if failFirstIssue && len(*issueBodies) == 1 {
				http.Error(w, `{"message":"boom: api down"}`, http.StatusInternalServerError)
				return
			}
			issue++
			writeJSON(w, map[string]any{
				"number":   issue,
				"html_url": fmt.Sprintf("https://github.com/o/r/issues/%d", issue),
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	restoreAPI := host.SetGitHubAPIForTest(srv.URL, srv.Client())
	return func() {
		restoreAPI()
		srv.Close()
	}
}

// TestGitHubFileFindings_FilesCredibleIssues proves the core walk: one issue
// per credible finding, expected/actual/repro body, evidence uploaded, results
// recorded back into findings.json.
func TestGitHubFileFindings_FilesCredibleIssues(t *testing.T) {
	dir := writeFindingsBundle(t)
	var issueBodies []string
	uploads := 0
	restore := githubFindingsAPI(t, &issueBodies, &uploads, false)
	defer restore()
	restoreExec := host.SetExecRunnerForTest(func(ctx context.Context, d, name string, args ...string) (string, string, int, error) {
		t.Errorf("findings filing must use native GitHub APIs, got exec: %s %s", name, strings.Join(args, " "))
		return "", "", 1, nil
	})
	defer restoreExec()

	res, err := host.GitHubFileFindings(context.Background(), host.FindingsFilingInput{
		RunDir: dir, RepoRoot: dir, Repo: "o/r", FiledBy: "qa",
	})
	if err != nil {
		t.Fatalf("GitHubFileFindings: %v", err)
	}
	if res.Filed != 2 || res.Skipped != 0 || res.Failed != 0 {
		t.Fatalf("counts filed/skipped/failed = %d/%d/%d, want 2/0/0", res.Filed, res.Skipped, res.Failed)
	}
	if res.Related != 0 {
		t.Fatalf("related = %d, want 0", res.Related)
	}
	if len(res.Outcomes) != 2 {
		t.Fatalf("outcomes = %d, want 2 (seeded + strength + blocked findings excluded)", len(res.Outcomes))
	}

	if len(issueBodies) != 2 {
		t.Fatalf("issue create calls = %d, want 2", len(issueBodies))
	}
	// finding-1 uploads its screenshot + the scenario's captured trace.
	if uploads != 2 {
		t.Errorf("release uploads = %d, want 2 (shot.png + trace.json)", uploads)
	}
	first := issueBodies[0]
	for _, want := range []string{
		"product-journey dependency-bugfix: verify gate loops forever",
		"## Expected",
		"The bugfix story reaches a verified fix.",
		"## Actual",
		"The verify gate re-entered itself after the fix landed.",
		"Severity: high",
		"## Reproduction",
		"Product-journey QA run `run-777` (project `vscode`, persona `ide-first-engineer`, seed `demo`)",
		"Fix the flaky dependency test via the bugfix story.",
		"Drove the bugfix story to the verify gate (tools: session_drive, visual_snapshot)",
		"## Run bundle",
		"## Artifacts",
		"releases/download/kitsoki-artifacts/",
		"```kitsoki",
		"trace_ref: product-journey://run-777/finding-1",
	} {
		if !strings.Contains(first, want) {
			t.Errorf("finding-1 issue body missing %q", want)
		}
	}
	// finding-4: unresolvable evidence stays a body reference; fallback
	// expected line renders.
	second := issueBodies[1]
	for _, want := range []string{
		"product-journey project-onboarding: onboarding config generator emits stale commands",
		"The project-onboarding journey completes without the problem described below.",
		"## Additional evidence references",
		"cassette://product-journey/run-777/missing/none.json",
	} {
		if !strings.Contains(second, want) {
			t.Errorf("finding-4 issue body missing %q", want)
		}
	}
	// finding-5 is a blocked capture-gap finding: it must never be filed.
	for _, c := range issueBodies {
		if strings.Contains(c, "onboarding blocked on missing cassette") {
			t.Error("blocked finding must not be filed as a GitHub issue")
		}
	}

	// Results written back: per-finding github_issue + the filing block.
	findings, err := os.ReadFile(filepath.Join(dir, "findings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	if err := json.Unmarshal(findings, &f); err != nil {
		t.Fatal(err)
	}
	items := f["items"].([]any)
	first1 := items[0].(map[string]any)
	gi := first1["github_issue"].(map[string]any)
	if gi["url"] != "https://github.com/o/r/issues/101" || gi["repo"] != "o/r" {
		t.Errorf("finding-1 github_issue = %v", gi)
	}
	assets, _ := gi["evidence_assets"].([]any)
	if len(assets) != 2 {
		t.Fatalf("finding-1 evidence_assets len = %d, want 2: %v", len(assets), gi["evidence_assets"])
	}
	assetURLs := map[string]string{}
	for _, raw := range assets {
		asset := raw.(map[string]any)
		assetURLs[asset["name"].(string)] = asset["url"].(string)
	}
	if !strings.Contains(assetURLs["shot.png"], "https://github.com/o/r/releases/download/kitsoki-artifacts/") ||
		!strings.Contains(assetURLs["trace.json"], "https://github.com/o/r/releases/download/kitsoki-artifacts/") {
		t.Fatalf("finding-1 evidence asset URLs = %v", assetURLs)
	}
	if _, has := items[1].(map[string]any)["github_issue"]; has {
		t.Error("seeded finding must not be filed")
	}
	filing := f["filing"].(map[string]any)
	if filing["requested"] != true || filing["ticket_repo"] != "o/r" || filing["filed"] != float64(2) {
		t.Errorf("filing block = %v", filing)
	}
}

func TestGitHubFileFindings_RelatesDuplicateOpenIssue(t *testing.T) {
	dir := writeFindingsBundle(t)
	t.Setenv("GH_TOKEN", "test-token")
	var searchQueries []string
	var issueCreates int
	var comments []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/search/issues":
			q := r.URL.Query().Get("q")
			searchQueries = append(searchQueries, q)
			if strings.Contains(q, "verify") {
				writeJSON(w, map[string]any{
					"items": []map[string]any{{
						"number":   900,
						"title":    "verify gate loops forever",
						"state":    "open",
						"html_url": "https://github.com/o/r/issues/900",
					}},
				})
				return
			}
			writeJSON(w, map[string]any{"items": []map[string]any{}})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues/900/comments":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			comments = append(comments, payload["body"].(string))
			writeJSON(w, map[string]any{"html_url": "https://github.com/o/r/issues/900#issuecomment-1"})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/releases/tags/kitsoki-artifacts":
			writeJSON(w, map[string]any{"upload_url": "http://" + r.Host + "/uploads/assets{?name,label}"})
		case r.Method == http.MethodPost && r.URL.Path == "/uploads/assets":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues":
			issueCreates++
			writeJSON(w, map[string]any{
				"number":   901,
				"html_url": "https://github.com/o/r/issues/901",
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()
	restoreAPI := host.SetGitHubAPIForTest(srv.URL, srv.Client())
	defer restoreAPI()
	restoreExec := host.SetExecRunnerForTest(func(ctx context.Context, d, name string, args ...string) (string, string, int, error) {
		t.Errorf("findings filing must use native GitHub APIs, got exec: %s %s", name, strings.Join(args, " "))
		return "", "", 1, nil
	})
	defer restoreExec()

	res, err := host.GitHubFileFindings(context.Background(), host.FindingsFilingInput{
		RunDir: dir, RepoRoot: dir, Repo: "o/r", FiledBy: "qa",
	})
	if err != nil {
		t.Fatalf("GitHubFileFindings: %v", err)
	}
	if res.Related != 1 || res.Filed != 1 || res.Failed != 0 {
		t.Fatalf("counts filed/related/failed = %d/%d/%d, want 1/1/0", res.Filed, res.Related, res.Failed)
	}
	if issueCreates != 1 {
		t.Fatalf("issue creates = %d, want 1 (second finding only)", issueCreates)
	}
	if len(comments) != 1 {
		t.Fatalf("comments = %d, want 1", len(comments))
	}
	for _, want := range []string{
		"<!-- kitsoki-related-product-journey-finding -->",
		"attached the new evidence here instead of filing a duplicate",
		"finding-1",
		"## Related observation",
		"The verify gate re-entered itself after the fix landed.",
		"evidence/shot.png",
	} {
		if !strings.Contains(comments[0], want) {
			t.Errorf("related comment missing %q", want)
		}
	}
	if len(searchQueries) == 0 || !strings.Contains(searchQueries[0], "is:open") || !strings.Contains(searchQueries[0], "in:title") {
		t.Fatalf("search queries = %v, want open-title search", searchQueries)
	}

	data, err := os.ReadFile(filepath.Join(dir, "findings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var findings map[string]any
	if err := json.Unmarshal(data, &findings); err != nil {
		t.Fatal(err)
	}
	first := findings["items"].([]any)[0].(map[string]any)
	gi := first["github_issue"].(map[string]any)
	if gi["url"] != "https://github.com/o/r/issues/900" || gi["relation"] != "related" {
		t.Fatalf("related github_issue = %v", gi)
	}
	if gi["comment_url"] != "https://github.com/o/r/issues/900#issuecomment-1" {
		t.Fatalf("comment_url = %v", gi["comment_url"])
	}
	filing := findings["filing"].(map[string]any)
	if filing["related"] != float64(1) {
		t.Fatalf("filing related = %v", filing)
	}
}

func TestGitHubFileFindings_RelatesSimilarOpenIssue(t *testing.T) {
	dir := writeFindingsBundle(t)
	t.Setenv("GH_TOKEN", "test-token")
	var issueCreates int
	var comments []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/search/issues":
			q := r.URL.Query().Get("q")
			if strings.Contains(q, "verify") {
				writeJSON(w, map[string]any{
					"items": []map[string]any{{
						"number":   902,
						"title":    "product-journey dependency-bugfix: verify gate looped forever",
						"state":    "open",
						"html_url": "https://github.com/o/r/issues/902",
					}},
				})
				return
			}
			writeJSON(w, map[string]any{"items": []map[string]any{}})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues/902/comments":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			comments = append(comments, payload["body"].(string))
			writeJSON(w, map[string]any{"html_url": "https://github.com/o/r/issues/902#issuecomment-1"})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/releases/tags/kitsoki-artifacts":
			writeJSON(w, map[string]any{"upload_url": "http://" + r.Host + "/uploads/assets{?name,label}"})
		case r.Method == http.MethodPost && r.URL.Path == "/uploads/assets":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues":
			issueCreates++
			writeJSON(w, map[string]any{
				"number":   903,
				"html_url": "https://github.com/o/r/issues/903",
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()
	restoreAPI := host.SetGitHubAPIForTest(srv.URL, srv.Client())
	defer restoreAPI()
	restoreExec := host.SetExecRunnerForTest(func(ctx context.Context, d, name string, args ...string) (string, string, int, error) {
		t.Errorf("findings filing must use native GitHub APIs, got exec: %s %s", name, strings.Join(args, " "))
		return "", "", 1, nil
	})
	defer restoreExec()

	res, err := host.GitHubFileFindings(context.Background(), host.FindingsFilingInput{
		RunDir: dir, RepoRoot: dir, Repo: "o/r", FiledBy: "qa",
	})
	if err != nil {
		t.Fatalf("GitHubFileFindings: %v", err)
	}
	if res.Related != 1 || res.Filed != 1 || res.Failed != 0 {
		t.Fatalf("counts filed/related/failed = %d/%d/%d, want 1/1/0", res.Filed, res.Related, res.Failed)
	}
	if issueCreates != 1 {
		t.Fatalf("issue creates = %d, want 1 (similar first finding only comments)", issueCreates)
	}
	if len(comments) != 1 || !strings.Contains(comments[0], "attached the new evidence here instead of filing a duplicate") {
		t.Fatalf("related comment = %v", comments)
	}

	data, err := os.ReadFile(filepath.Join(dir, "findings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var findings map[string]any
	if err := json.Unmarshal(data, &findings); err != nil {
		t.Fatal(err)
	}
	first := findings["items"].([]any)[0].(map[string]any)
	gi := first["github_issue"].(map[string]any)
	if gi["url"] != "https://github.com/o/r/issues/902" || gi["relation"] != "related" {
		t.Fatalf("related github_issue = %v", gi)
	}
}

func TestGitHubFileFindings_RelatesTraceFingerprint(t *testing.T) {
	dir := writeFindingsBundle(t)
	t.Setenv("GH_TOKEN", "test-token")
	traceRef := "product-journey://run-777/finding-1"
	var searchQueries []string
	var issueCreates int
	var comments []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/search/issues":
			q := r.URL.Query().Get("q")
			searchQueries = append(searchQueries, q)
			if strings.Contains(q, "in:body") && strings.Contains(q, traceRef) {
				writeJSON(w, map[string]any{
					"items": []map[string]any{{
						"number":   777,
						"title":    "old unrelated-looking journey failure",
						"state":    "open",
						"html_url": "https://github.com/o/r/issues/777",
					}},
				})
				return
			}
			writeJSON(w, map[string]any{"items": []map[string]any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/777":
			writeJSON(w, map[string]any{
				"number":   777,
				"title":    "old unrelated-looking journey failure",
				"state":    "open",
				"html_url": "https://github.com/o/r/issues/777",
				"body":     "```kitsoki\ntrace_ref: " + traceRef + "\n```",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/777/comments":
			writeJSON(w, []map[string]any{})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues/777/comments":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			comments = append(comments, payload["body"].(string))
			writeJSON(w, map[string]any{"html_url": "https://github.com/o/r/issues/777#issuecomment-1"})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/releases/tags/kitsoki-artifacts":
			writeJSON(w, map[string]any{"upload_url": "http://" + r.Host + "/uploads/assets{?name,label}"})
		case r.Method == http.MethodPost && r.URL.Path == "/uploads/assets":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues":
			issueCreates++
			writeJSON(w, map[string]any{
				"number":   778,
				"html_url": "https://github.com/o/r/issues/778",
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()
	restoreAPI := host.SetGitHubAPIForTest(srv.URL, srv.Client())
	defer restoreAPI()
	restoreExec := host.SetExecRunnerForTest(func(ctx context.Context, d, name string, args ...string) (string, string, int, error) {
		t.Errorf("findings filing must use native GitHub APIs, got exec: %s %s", name, strings.Join(args, " "))
		return "", "", 1, nil
	})
	defer restoreExec()

	res, err := host.GitHubFileFindings(context.Background(), host.FindingsFilingInput{
		RunDir: dir, RepoRoot: dir, Repo: "o/r", FiledBy: "qa",
	})
	if err != nil {
		t.Fatalf("GitHubFileFindings: %v", err)
	}
	if res.Related != 1 || res.Filed != 1 || res.Failed != 0 {
		t.Fatalf("counts filed/related/failed = %d/%d/%d, want 1/1/0", res.Filed, res.Related, res.Failed)
	}
	if issueCreates != 1 {
		t.Fatalf("issue creates = %d, want 1 (second finding only)", issueCreates)
	}
	if len(comments) != 1 {
		t.Fatalf("comments = %d, want 1", len(comments))
	}
	if !strings.Contains(comments[0], "- Trace ref: `"+traceRef+"`") {
		t.Fatalf("related comment missing trace ref: %s", comments[0])
	}
	var sawBodyTraceSearch bool
	for _, q := range searchQueries {
		if strings.Contains(q, "in:body") && strings.Contains(q, traceRef) {
			sawBodyTraceSearch = true
		}
	}
	if !sawBodyTraceSearch {
		t.Fatalf("search queries = %v, want body trace-ref search", searchQueries)
	}

	data, err := os.ReadFile(filepath.Join(dir, "findings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var findings map[string]any
	if err := json.Unmarshal(data, &findings); err != nil {
		t.Fatal(err)
	}
	first := findings["items"].([]any)[0].(map[string]any)
	gi := first["github_issue"].(map[string]any)
	if gi["url"] != "https://github.com/o/r/issues/777" || gi["relation"] != "related" {
		t.Fatalf("related github_issue = %v", gi)
	}
	if gi["comment_url"] != "https://github.com/o/r/issues/777#issuecomment-1" {
		t.Fatalf("comment_url = %v", gi["comment_url"])
	}
}

// TestGitHubFileFindings_Idempotent proves a re-run skips already-filed
// findings instead of filing duplicates.
func TestGitHubFileFindings_Idempotent(t *testing.T) {
	dir := writeFindingsBundle(t)
	var issueBodies []string
	uploads := 0
	restore := githubFindingsAPI(t, &issueBodies, &uploads, false)
	defer restore()

	in := host.FindingsFilingInput{RunDir: dir, RepoRoot: dir, Repo: "o/r"}
	if _, err := host.GitHubFileFindings(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	firstIssueCreates := len(issueBodies)

	res, err := host.GitHubFileFindings(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Filed != 0 || res.Skipped != 2 {
		t.Fatalf("second run filed/skipped = %d/%d, want 0/2", res.Filed, res.Skipped)
	}
	secondIssueCreates := len(issueBodies)
	if secondIssueCreates != firstIssueCreates {
		t.Fatalf("re-run created %d extra issues", secondIssueCreates-firstIssueCreates)
	}
	// Skipped outcomes still carry the existing URL so callers can report it.
	for _, out := range res.Outcomes {
		if out.IssueURL == "" {
			t.Errorf("skipped outcome %s missing issue url", out.FindingID)
		}
	}
}

// TestGitHubFileFindings_DryRun proves dry-run renders bodies without touching
// the network or the bundle.
func TestGitHubFileFindings_DryRun(t *testing.T) {
	dir := writeFindingsBundle(t)
	before, err := os.ReadFile(filepath.Join(dir, "findings.json"))
	if err != nil {
		t.Fatal(err)
	}
	restore := host.SetExecRunnerForTest(func(ctx context.Context, d, name string, args ...string) (string, string, int, error) {
		t.Errorf("dry-run must not exec, got: %s %s", name, strings.Join(args, " "))
		return "", "", 1, nil
	})
	defer restore()

	res, err := host.GitHubFileFindings(context.Background(), host.FindingsFilingInput{
		RunDir: dir, RepoRoot: dir, Repo: "o/r", DryRun: true,
	})
	if err != nil {
		t.Fatalf("GitHubFileFindings dry-run: %v", err)
	}
	if res.Status != "findings_dry_run" || len(res.Outcomes) != 2 {
		t.Fatalf("dry-run result: status=%q outcomes=%d", res.Status, len(res.Outcomes))
	}
	for _, out := range res.Outcomes {
		if out.Status != "dry-run" || out.Body == "" {
			t.Errorf("outcome %s: status=%q body-empty=%v", out.FindingID, out.Status, out.Body == "")
		}
		if !strings.Contains(out.Body, "## Expected") || !strings.Contains(out.Body, "## Reproduction") {
			t.Errorf("outcome %s body missing sections", out.FindingID)
		}
	}
	after, err := os.ReadFile(filepath.Join(dir, "findings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("dry-run must not modify findings.json")
	}
}

// TestGitHubFileFindings_PerFindingFailure proves one failing filing does not
// abort the walk and is reported (not exit-coded) in the result.
func TestGitHubFileFindings_PerFindingFailure(t *testing.T) {
	dir := writeFindingsBundle(t)
	var issueBodies []string
	uploads := 0
	restore := githubFindingsAPI(t, &issueBodies, &uploads, true)
	defer restore()

	res, err := host.GitHubFileFindings(context.Background(), host.FindingsFilingInput{
		RunDir: dir, RepoRoot: dir, Repo: "o/r",
	})
	if err != nil {
		t.Fatalf("walk must complete despite one failure: %v", err)
	}
	if res.Filed != 1 || res.Failed != 1 {
		t.Fatalf("filed/failed = %d/%d, want 1/1", res.Filed, res.Failed)
	}
	var f map[string]any
	data, _ := os.ReadFile(filepath.Join(dir, "findings.json"))
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if _, has := f["items"].([]any)[0].(map[string]any)["github_issue"]; has {
		t.Error("failed finding must stay unfiled for the next re-run")
	}
	filing := f["filing"].(map[string]any)
	if filing["failed"] != float64(1) {
		t.Errorf("filing block should record the failure: %v", filing)
	}
}
