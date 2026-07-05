package host_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kitsoki/internal/host"
)

// TestGitHubTicket_RegisteredCreate proves the prefix-fallback dispatches the
// new create op like the other five.
func TestGitHubTicket_RegisteredCreate(t *testing.T) {
	r := host.NewRegistry()
	host.RegisterBuiltins(r)
	if _, ok := r.Get("host.gh.ticket.create"); !ok {
		t.Fatal("registry: host.gh.ticket.create missing (prefix-fallback should resolve)")
	}
}

func TestGitHubTicket_Create_RequiresTitle(t *testing.T) {
	res, err := host.GitHubTicketHandler(context.Background(), map[string]any{
		"op": "create",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected domain error when title missing")
	}
}

func TestGitHubTicket_Create_Happy(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	var payload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/constructorfabric/Kitsoki/issues" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"number":77,"html_url":"https://github.com/constructorfabric/Kitsoki/issues/77"}`))
	}))
	defer srv.Close()
	restore := host.SetGitHubAPIForTest(srv.URL, srv.Client())
	defer restore()

	res, err := host.GitHubTicketHandler(context.Background(), map[string]any{
		"op":          "create",
		"repo":        "constructorfabric/Kitsoki",
		"title":       "Esc hangs the TUI",
		"body":        "Pressing Esc twice hangs the input loop.",
		"severity":    "P1",
		"component":   "tui",
		"target":      "kitsoki",
		"status":      "in_progress",
		"trace_ref":   "trace://abc123",
		"kitsoki_rev": "deadbeef",
		"filed_by":    "brad",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	if res.Data["id"] != "77" || res.Data["number"] != "77" {
		t.Fatalf("issue number parse: id=%v number=%v", res.Data["id"], res.Data["number"])
	}
	if res.Data["url"] != "https://github.com/constructorfabric/Kitsoki/issues/77" {
		t.Fatalf("url: %v", res.Data["url"])
	}

	labels, _ := payload["labels"].([]any)
	gotLabels := strings.Join(anyStrings(labels), ",")
	for _, want := range []string{"P1", "comp:tui", "target:kitsoki", "in_progress"} {
		if !strings.Contains(gotLabels, want) {
			t.Errorf("create payload labels missing %q: %v", want, labels)
		}
	}
	// The ```kitsoki body-metadata block carries the GitHub-homeless fields.
	body, _ := payload["body"].(string)
	for _, want := range []string{"```kitsoki", "trace_ref: trace://abc123", "kitsoki_rev: deadbeef", "filed_by: brad"} {
		if !strings.Contains(body, want) {
			t.Errorf("create body missing %q\n  got: %s", want, body)
		}
	}
}

// TestGitHubTicket_Create_LabelPermissionDegrades proves a fork contributor
// without triage still files the issue (unlabelled) with a warning, rather than
// failing the create.
func TestGitHubTicket_Create_LabelPermissionDegrades(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	var labelled, unlabelled, labelEnsure bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/labels") {
			labelEnsure = true
			http.Error(w, `{"message":"Resource not accessible by integration (label)"}`, http.StatusForbidden)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/repos/constructorfabric/Kitsoki/issues" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if _, hasLabels := payload["labels"]; hasLabels {
			labelled = true
			http.Error(w, `{"message":"could not add label: you must have triage permission (HTTP 403)"}`, http.StatusForbidden)
			return
		}
		unlabelled = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"number":88,"html_url":"https://github.com/constructorfabric/Kitsoki/issues/88"}`))
	}))
	defer srv.Close()
	restore := host.SetGitHubAPIForTest(srv.URL, srv.Client())
	defer restore()

	res, err := host.GitHubTicketHandler(context.Background(), map[string]any{
		"op":       "create",
		"repo":     "constructorfabric/Kitsoki",
		"title":    "A fork contributor's bug",
		"severity": "P2",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("create should degrade, not fail: %s", res.Error)
	}
	if !labelled || !unlabelled || !labelEnsure {
		t.Fatalf("expected labelled attempt, label ensure, and unlabelled retry (labelled=%v ensure=%v unlabelled=%v)", labelled, labelEnsure, unlabelled)
	}
	if res.Data["id"] != "88" {
		t.Fatalf("issue number: %v", res.Data["id"])
	}
	if w, _ := res.Data["warning"].(string); w == "" {
		t.Fatal("expected a warning that labels were dropped")
	}
}

func anyStrings(values []any) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// TestGitHubTicket_Get_ParsesMetadata proves get() recovers the create-written
// ```kitsoki block (the round-trip slice #4's migration relies on).
func TestGitHubTicket_Get_ParsesMetadata(t *testing.T) {
	fr := newFakeRunner()
	fr.responses["gh --version"] = fakeResp{stdout: "gh version 2.x\n"}
	body := "Pressing Esc hangs.\n\n```kitsoki\ntrace_ref: trace://abc123\nlegacy_id: 2026-05-14T103205Z-tui-hang\n```\n"
	fr.responses["gh issue view 88"] = fakeResp{
		stdout: `{"number":88,"title":"Esc hangs","body":` + jsonQuote(body) + `,"state":"OPEN","url":"https://github.com/o/r/issues/88","comments":[]}`,
	}
	restore := host.SetExecRunnerForTest(fr.run)
	defer restore()

	res, err := host.GitHubTicketHandler(context.Background(), map[string]any{
		"op": "get",
		"id": "88",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("domain: %s", res.Error)
	}
	meta, ok := res.Data["kitsoki_meta"].(map[string]any)
	if !ok {
		t.Fatalf("kitsoki_meta not parsed: %v", res.Data)
	}
	if meta["trace_ref"] != "trace://abc123" {
		t.Errorf("trace_ref: %v", meta["trace_ref"])
	}
	if meta["legacy_id"] != "2026-05-14T103205Z-tui-hang" {
		t.Errorf("legacy_id: %v", meta["legacy_id"])
	}
}

// jsonQuote renders s as a JSON string literal (incl. surrounding quotes).
func jsonQuote(s string) string {
	r := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n", "\t", "\\t", "\r", "\\r")
	return "\"" + r.Replace(s) + "\""
}
