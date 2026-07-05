package host_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kitsoki/internal/host"
)

// TestGitHubTicket_RegisteredAsBuiltin proves the registry's prefix-fallback
// dispatches every ticket op to the single `host.gh.ticket` registration.
func TestGitHubTicket_RegisteredAsBuiltin(t *testing.T) {
	r := host.NewRegistry()
	host.RegisterBuiltins(r)
	for _, name := range []string{
		"host.gh.ticket",
		"host.gh.ticket.search",
		"host.gh.ticket.get",
		"host.gh.ticket.comment",
		"host.gh.ticket.comment_edit",
		"host.gh.ticket.transition",
		"host.gh.ticket.list_mine",
	} {
		if _, ok := r.Get(name); !ok {
			t.Fatalf("registry: %s missing (prefix-fallback should resolve)", name)
		}
	}
}

func TestGitHubTicket_MissingOp(t *testing.T) {
	res, err := host.GitHubTicketHandler(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected domain error for missing op")
	}
}

func TestGitHubTicket_GhMissing(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["gh --version"] = fakeResp{err: fmt.Errorf("gh not on PATH")}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.GitHubTicketHandler(context.Background(), map[string]any{
		"op": "search",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected domain error when gh missing")
	}
	if !strings.Contains(res.Error, "gh CLI") {
		t.Fatalf("error should mention gh CLI: %s", res.Error)
	}
}

func TestGitHubTicket_Search_Happy(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["gh --version"] = fakeResp{stdout: "gh version 2.x\n"}
	// `gh issue list ... --search "esc"` returns a JSON list. The fake
	// runner's prefix-fallback matches the longest registered key, so we
	// pin the list invocation under a stable prefix.
	fr.responses["gh issue list --state all --limit 30 --json number,title,state,labels,assignees,url --search esc"] = fakeResp{
		stdout: `[{"number":42,"title":"Esc hangs the TUI","state":"OPEN","url":"https://github.com/o/r/issues/42","assignees":[{"login":"brad"}]}]`,
	}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.GitHubTicketHandler(context.Background(), map[string]any{
		"op":    "search",
		"query": "esc",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	tickets, _ := res.Data["tickets"].([]map[string]any)
	if len(tickets) != 1 {
		t.Fatalf("expected 1, got %d (%v)", len(tickets), res.Data)
	}
	if tickets[0]["id"] != "42" {
		t.Fatalf("id: %v", tickets[0]["id"])
	}
	if tickets[0]["assignee"] != "brad" {
		t.Fatalf("assignee: %v", tickets[0]["assignee"])
	}
	if tickets[0]["status"] != "open" {
		t.Fatalf("status (should be lowercased): %v", tickets[0]["status"])
	}
}

func TestListGitHubInboxItems_Happy(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["gh --version"] = fakeResp{stdout: "gh version 2.x\n"}
	fr.responses["gh issue list --repo acme/repo --state open --assignee @me --limit 25 --json number,title,assignees,url"] = fakeResp{
		stdout: `[{"number":7,"title":"Assigned issue","url":"https://github.com/acme/repo/issues/7","assignees":[{"login":"brad"}]}]`,
	}
	fr.responses["gh pr list --repo acme/repo --state open --search review-requested:@me --limit 25 --json number,title,author,url"] = fakeResp{
		stdout: `[{"number":42,"title":"Review this","url":"https://github.com/acme/repo/pull/42","author":{"login":"alice"}}]`,
	}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	items, err := host.ListGitHubInboxItems(context.Background(), host.GitHubInboxOptions{
		Repo:          "acme/repo",
		IncludeIssues: true,
		IncludePRs:    true,
		Limit:         25,
	})
	if err != nil {
		t.Fatalf("ListGitHubInboxItems: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d (%+v)", len(items), items)
	}
	if items[0].Kind != "issue" || items[0].Number != "7" || items[0].Author != "brad" {
		t.Fatalf("issue projection: %+v", items[0])
	}
	if items[1].Kind != "pr" || items[1].Number != "42" || items[1].Author != "alice" {
		t.Fatalf("pr projection: %+v", items[1])
	}
}

func TestListGitHubInboxItems_CustomFilters(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["gh --version"] = fakeResp{stdout: "gh version 2.x\n"}
	fr.responses["gh issue list --state open --assignee octo --limit 5 --json number,title,assignees,url"] = fakeResp{
		stdout: `[]`,
	}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	items, err := host.ListGitHubInboxItems(context.Background(), host.GitHubInboxOptions{
		IncludeIssues: true,
		Assignee:      "octo",
		Limit:         5,
	})
	if err != nil {
		t.Fatalf("ListGitHubInboxItems: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected no items, got %+v", items)
	}
	for _, cmd := range fr.calls {
		if strings.Contains(cmd, "pr list") {
			t.Fatalf("did not expect PR query when IncludePRs=false: %v", fr.calls)
		}
	}
}

func TestGitHubTicket_Search_BadJSON(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["gh --version"] = fakeResp{stdout: "gh version 2.x\n"}
	fr.defaultResp = fakeResp{stdout: "not-json"}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.GitHubTicketHandler(context.Background(), map[string]any{
		"op":    "search",
		"query": "x",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected domain error for bad JSON")
	}
}

func TestGitHubTicket_Get_Happy(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/42":
			writeJSON(w, map[string]any{
				"number":   42,
				"title":    "Esc hangs",
				"body":     "Expected x",
				"state":    "OPEN",
				"html_url": "https://github.com/o/r/issues/42",
				"assignees": []map[string]any{
					{"login": "brad"},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/42/comments":
			writeJSON(w, []map[string]any{{"id": 1, "body": "repro"}})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()
	restoreAPI := host.SetGitHubAPIForTest(srv.URL, srv.Client())
	defer restoreAPI()
	restoreExec := host.SetExecRunnerForTest(func(ctx context.Context, d, name string, args ...string) (string, string, int, error) {
		t.Errorf("ticket.get must use native GitHub APIs, got exec: %s %s", name, strings.Join(args, " "))
		return "", "", 1, nil
	})
	defer restoreExec()

	res, err := host.GitHubTicketHandler(context.Background(), map[string]any{
		"op":   "get",
		"id":   "42",
		"repo": "o/r",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	if res.Data["title"] != "Esc hangs" {
		t.Fatalf("title: %v", res.Data["title"])
	}
	if res.Data["body"] != "Expected x" {
		t.Fatalf("body: %v", res.Data["body"])
	}
	if res.Data["url"] != "https://github.com/o/r/issues/42" {
		t.Fatalf("url: %v", res.Data["url"])
	}
	comments, _ := res.Data["comments"].([]any)
	if len(comments) != 1 {
		t.Fatalf("comments: %v", res.Data["comments"])
	}
}

// TestGitHubTicket_Search_ClassifiesType proves a GitHub-sourced ticket lands a
// concrete `type` (P3): a `bug` label → "bug", a `feature`/`enhancement` label →
// "feature", an `epic` label → "epic", and an unlabelled issue defaults to "bug"
// (never ""), so dev-story's type-guarded `drive` arc never falls through to its
// no-op self-loop. Every row also carries source="github" for the P5 mapping.
func TestGitHubTicket_Search_ClassifiesType(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["gh --version"] = fakeResp{stdout: "gh version 2.x\n"}
	fr.defaultResp = fakeResp{
		stdout: `[
			{"number":42,"title":"Esc hangs","state":"OPEN","url":"u42","labels":[{"name":"bug"},{"name":"P1"}]},
			{"number":43,"title":"Add export","state":"OPEN","url":"u43","labels":[{"name":"enhancement"}]},
			{"number":44,"title":"Tracker epic","state":"OPEN","url":"u44","labels":[{"name":"epic"}]},
			{"number":45,"title":"Unlabelled defect","state":"OPEN","url":"u45","labels":[]}
		]`,
	}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.GitHubTicketHandler(context.Background(), map[string]any{
		"op": "search",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	tickets, _ := res.Data["tickets"].([]map[string]any)
	want := map[string]string{"42": "bug", "43": "feature", "44": "epic", "45": "bug"}
	if len(tickets) != len(want) {
		t.Fatalf("expected %d tickets, got %d", len(want), len(tickets))
	}
	for _, tk := range tickets {
		id, _ := tk["id"].(string)
		if got := tk["type"]; got != want[id] {
			t.Errorf("ticket %s type = %v, want %q", id, got, want[id])
		}
		if tk["source"] != "github" {
			t.Errorf("ticket %s source = %v, want github", id, tk["source"])
		}
	}
}

// TestGitHubTicket_Get_SurfacesIdentity proves ticket.get lifts the legacy local
// bug-file id out of the ```kitsoki metadata block to a top-level `legacy_id`
// field, and marks source=github — making the local-file ↔ GitHub-issue mapping
// visible to the ticket view (P5).
func TestGitHubTicket_Get_SurfacesIdentity(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	body := "Esc hangs the TUI.\n\n```kitsoki\nlegacy_id: 2026-06-19T12-00-00Z-esc-hang\nfiled_by: brad\n```\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/19":
			writeJSON(w, map[string]any{
				"number":    19,
				"title":     "Esc hangs",
				"body":      body,
				"state":     "OPEN",
				"html_url":  "https://github.com/o/r/issues/19",
				"labels":    []map[string]any{{"name": "bug"}},
				"assignees": []map[string]any{},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/19/comments":
			writeJSON(w, []map[string]any{})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()
	restoreAPI := host.SetGitHubAPIForTest(srv.URL, srv.Client())
	defer restoreAPI()
	restoreExec := host.SetExecRunnerForTest(func(ctx context.Context, d, name string, args ...string) (string, string, int, error) {
		t.Errorf("ticket.get must use native GitHub APIs, got exec: %s %s", name, strings.Join(args, " "))
		return "", "", 1, nil
	})
	defer restoreExec()

	res, err := host.GitHubTicketHandler(context.Background(), map[string]any{
		"op":   "get",
		"id":   "19",
		"repo": "o/r",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	if res.Data["type"] != "bug" {
		t.Fatalf("type: %v (want bug)", res.Data["type"])
	}
	if res.Data["source"] != "github" {
		t.Fatalf("source: %v (want github)", res.Data["source"])
	}
	if res.Data["legacy_id"] != "2026-06-19T12-00-00Z-esc-hang" {
		t.Fatalf("legacy_id should be lifted from the kitsoki metadata block: %v", res.Data["legacy_id"])
	}
	meta, _ := res.Data["kitsoki_meta"].(map[string]any)
	if meta["filed_by"] != "brad" {
		t.Fatalf("kitsoki_meta should still carry the full block: %v", meta)
	}
}

func TestGitHubTicket_Get_RequiresID(t *testing.T) {
	res, err := host.GitHubTicketHandler(context.Background(), map[string]any{
		"op": "get",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected domain error when id missing")
	}
}

func TestGitHubTicket_Comment_Happy(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/o/r/issues/42/comments" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		body, _ = payload["body"].(string)
		writeJSON(w, map[string]any{"html_url": "https://github.com/o/r/issues/42#issuecomment-1"})
	}))
	defer srv.Close()
	restoreAPI := host.SetGitHubAPIForTest(srv.URL, srv.Client())
	defer restoreAPI()
	restoreExec := host.SetExecRunnerForTest(func(ctx context.Context, d, name string, args ...string) (string, string, int, error) {
		t.Errorf("ticket.comment must use native GitHub APIs, got exec: %s %s", name, strings.Join(args, " "))
		return "", "", 1, nil
	})
	defer restoreExec()

	res, err := host.GitHubTicketHandler(context.Background(), map[string]any{
		"op":   "comment",
		"id":   "42",
		"repo": "o/r",
		"body": "Repro confirmed.",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	if res.Data["ok"] != true {
		t.Fatalf("ok: %v", res.Data["ok"])
	}
	if body != "Repro confirmed." {
		t.Fatalf("posted body = %q", body)
	}
	if !strings.Contains(res.Data["comment_id"].(string), "issuecomment") || res.Data["url"] != res.Data["comment_id"] {
		t.Fatalf("comment URL fields: %v", res.Data)
	}
}

func TestGitHubTicket_Comment_AcceptsIssueURL(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	posted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/bsacrobatix/Kitsoki/issues/117/comments" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		posted = true
		writeJSON(w, map[string]any{"html_url": "https://github.com/bsacrobatix/Kitsoki/issues/117#issuecomment-1"})
	}))
	defer srv.Close()
	restoreAPI := host.SetGitHubAPIForTest(srv.URL, srv.Client())
	defer restoreAPI()
	restoreExec := host.SetExecRunnerForTest(func(ctx context.Context, d, name string, args ...string) (string, string, int, error) {
		t.Errorf("ticket.comment must use native GitHub APIs, got exec: %s %s", name, strings.Join(args, " "))
		return "", "", 1, nil
	})
	defer restoreExec()

	res, err := host.GitHubTicketHandler(context.Background(), map[string]any{
		"op":   "comment",
		"id":   "https://github.com/bsacrobatix/Kitsoki/issues/117",
		"body": "Fixed in abc123.",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	if !posted {
		t.Fatal("expected issue URL to provide repo + number")
	}
}

func TestGitHubTicket_CommentUsesContextToken(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		writeJSON(w, map[string]any{"html_url": "https://github.com/o/r/issues/42#issuecomment-1"})
	}))
	defer srv.Close()
	restoreAPI := host.SetGitHubAPIForTest(srv.URL, srv.Client())
	defer restoreAPI()

	ctx := host.WithCLIExecEnv(context.Background(), map[string]string{
		"GH_TOKEN":     "app-token",
		"GITHUB_TOKEN": "app-token",
	})
	res, err := host.GitHubTicketHandler(ctx, map[string]any{
		"op":   "comment",
		"id":   "42",
		"repo": "o/r",
		"body": "Repro confirmed.",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	if auth != "Bearer app-token" {
		t.Fatalf("Authorization = %q, want bearer app-token", auth)
	}
}

func TestGitHubTicket_Comment_BodyRequired(t *testing.T) {
	res, err := host.GitHubTicketHandler(context.Background(), map[string]any{
		"op": "comment",
		"id": "42",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected domain error when body missing")
	}
}

func TestGitHubTicket_CommentEdit_Happy(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/repos/o/r/issues/comments/1" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		body, _ = payload["body"].(string)
		writeJSON(w, map[string]any{"html_url": "https://github.com/o/r/issues/42#issuecomment-1"})
	}))
	defer srv.Close()
	restoreAPI := host.SetGitHubAPIForTest(srv.URL, srv.Client())
	defer restoreAPI()
	restoreExec := host.SetExecRunnerForTest(func(ctx context.Context, d, name string, args ...string) (string, string, int, error) {
		t.Errorf("ticket.comment_edit must use native GitHub APIs, got exec: %s %s", name, strings.Join(args, " "))
		return "", "", 1, nil
	})
	defer restoreExec()

	res, err := host.GitHubTicketHandler(context.Background(), map[string]any{
		"op":         "comment_edit",
		"comment_id": "https://github.com/o/r/issues/42#issuecomment-1",
		"body":       "Updated status.",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	if res.Data["ok"] != true {
		t.Fatalf("ok: %v", res.Data["ok"])
	}
	if got := res.Data["comment_id"]; got != "https://github.com/o/r/issues/42#issuecomment-1" {
		t.Fatalf("comment_id: %v", got)
	}
	if body != "Updated status." {
		t.Fatalf("patched body = %q", body)
	}
}

func TestGitHubTicket_Transition_CloseHappy(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	var state string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/repos/o/r/issues/42" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		state, _ = payload["state"].(string)
		writeJSON(w, map[string]any{"state": state})
	}))
	defer srv.Close()
	restoreAPI := host.SetGitHubAPIForTest(srv.URL, srv.Client())
	defer restoreAPI()
	restoreExec := host.SetExecRunnerForTest(func(ctx context.Context, d, name string, args ...string) (string, string, int, error) {
		t.Errorf("ticket.transition must use native GitHub APIs, got exec: %s %s", name, strings.Join(args, " "))
		return "", "", 1, nil
	})
	defer restoreExec()

	res, err := host.GitHubTicketHandler(context.Background(), map[string]any{
		"op":   "transition",
		"repo": "o/r",
		"id":   "42",
		"to":   "resolved",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	if res.Data["ok"] != true {
		t.Fatalf("ok: %v", res.Data["ok"])
	}
	if state != "closed" || res.Data["status"] != "closed" {
		t.Fatalf("state/status = %q/%v, want closed", state, res.Data["status"])
	}
}

func TestGitHubTicket_Transition_AcceptsIssueURL(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	transitioned := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/repos/bsacrobatix/Kitsoki/issues/117" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		transitioned = true
		writeJSON(w, map[string]any{"state": "closed"})
	}))
	defer srv.Close()
	restoreAPI := host.SetGitHubAPIForTest(srv.URL, srv.Client())
	defer restoreAPI()
	restoreExec := host.SetExecRunnerForTest(func(ctx context.Context, d, name string, args ...string) (string, string, int, error) {
		t.Errorf("ticket.transition must use native GitHub APIs, got exec: %s %s", name, strings.Join(args, " "))
		return "", "", 1, nil
	})
	defer restoreExec()

	res, err := host.GitHubTicketHandler(context.Background(), map[string]any{
		"op": "transition",
		"id": "https://github.com/bsacrobatix/Kitsoki/issues/117",
		"to": "resolved",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	if !transitioned {
		t.Fatal("expected issue URL to provide repo + number")
	}
}

func TestGitHubTicket_Transition_ReopenHappy(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	var state string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/repos/o/r/issues/42" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		state, _ = payload["state"].(string)
		writeJSON(w, map[string]any{"state": state})
	}))
	defer srv.Close()
	restoreAPI := host.SetGitHubAPIForTest(srv.URL, srv.Client())
	defer restoreAPI()
	restoreExec := host.SetExecRunnerForTest(func(ctx context.Context, d, name string, args ...string) (string, string, int, error) {
		t.Errorf("ticket.transition must use native GitHub APIs, got exec: %s %s", name, strings.Join(args, " "))
		return "", "", 1, nil
	})
	defer restoreExec()

	res, err := host.GitHubTicketHandler(context.Background(), map[string]any{
		"op":   "transition",
		"repo": "o/r",
		"id":   "42",
		"to":   "open",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	if state != "open" || res.Data["status"] != "open" {
		t.Fatalf("state/status = %q/%v, want open", state, res.Data["status"])
	}
}

func TestGitHubTicket_Transition_RequiresTo(t *testing.T) {
	res, err := host.GitHubTicketHandler(context.Background(), map[string]any{
		"op": "transition",
		"id": "42",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected domain error when to missing")
	}
}

func TestGitHubTicket_ListMine_DefaultsToMe(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["gh --version"] = fakeResp{stdout: "gh version 2.x\n"}
	fr.responses["gh issue list --state open --assignee @me --limit 100 --json number,title,state,labels,assignees,url"] = fakeResp{
		stdout: `[{"number":1,"title":"One","state":"OPEN","url":"u1","assignees":[]},{"number":2,"title":"Two","state":"OPEN","url":"u2","assignees":[]}]`,
	}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.GitHubTicketHandler(context.Background(), map[string]any{
		"op": "list_mine",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	tickets, _ := res.Data["tickets"].([]map[string]any)
	if len(tickets) != 2 {
		t.Fatalf("expected 2, got %d (%v)", len(tickets), tickets)
	}
}

func TestGitHubTicket_ListMine_ErrorPropagates(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["gh --version"] = fakeResp{stdout: "gh version 2.x\n"}
	fr.defaultResp = fakeResp{stderr: "auth: token expired", code: 4}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.GitHubTicketHandler(context.Background(), map[string]any{
		"op": "list_mine",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected domain error when gh exit != 0")
	}
	if !strings.Contains(res.Error, "token expired") {
		t.Fatalf("error should propagate stderr: %s", res.Error)
	}
}

func TestGitHubTicket_UnknownOpRejected(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["gh --version"] = fakeResp{stdout: "gh version 2.x\n"}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.GitHubTicketHandler(context.Background(), map[string]any{
		"op": "smoke",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected domain error for unknown op")
	}
}
