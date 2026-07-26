package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeApplicationCommandFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	source := `
app: {id: demo, version: 1.0.0}
root: ready
states:
  ready:
    on:
      open: [{target: ready}]
intents:
  open: {title: Open}
application:
  schema: application/v1
  name: Demo
  description: Demonstrate application discovery.
  semantic_ref: demo.application
  shell: {entry: home}
  navigation:
    - id: home
      name: Home
      description: Open the home page.
      semantic_ref: demo.nav.home
      page: home
  pages:
    home:
      name: Home
      description: Show the main application page.
      semantic_ref: demo.page.home
      regions: {}
  actions:
    demo.open:
      name: Open
      description: Open the selected item.
      semantic_ref: demo.action.open
      handler: demo.open
exports:
  handlers:
    demo.open:
      name: Open
      description: Open the selected item.
      semantic_ref: demo.handler.open
      input_schema: schemas/open.json
      output_schema: schemas/frame.json
      session: required
      effect: read
      routing_mode: exact
      outcomes: [ok]
      dispatch: {intent: open, state: ready}
      expose: [jsonrpc, cli]
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestApplicationDescribeCommand(t *testing.T) {
	path := writeApplicationCommandFixture(t)
	cmd := applicationDescribeCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{path})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"semantic_ref": "demo.application"`) {
		t.Fatalf("output = %s", out.String())
	}
}

func TestApplicationHandlersCommandFiltersTransport(t *testing.T) {
	path := writeApplicationCommandFixture(t)
	cmd := applicationHandlersCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{path, "--transport", "mcp"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("output = %s", out.String())
	}
}

func TestApplicationGraphCommandIncludesSemanticAction(t *testing.T) {
	path := writeApplicationCommandFixture(t)
	cmd := applicationGraphCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{path})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"semantic_ref": "demo.action.open"`) {
		t.Fatalf("output = %s", out.String())
	}
}

func TestApplicationCallCommandUsesSharedJSONRPCRegistry(t *testing.T) {
	var gotMethod string
	var gotParams map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		var rpc struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(body, &rpc); err != nil {
			t.Fatal(err)
		}
		gotMethod, gotParams = rpc.Method, rpc.Params
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"schema":"application-outcome/v1","outcome":"ok"}}`)
	}))
	t.Cleanup(server.Close)

	cmd := applicationCallCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{
		"demo.open", "--url", server.URL, "--session-id", "session-1",
		"--input", `{"item_id":"item-1"}`,
	})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "runstatus.application.call" {
		t.Fatalf("method = %q", gotMethod)
	}
	if gotParams["transport"] != "cli" || gotParams["session_id"] != "session-1" {
		t.Fatalf("params = %#v", gotParams)
	}
	if !strings.Contains(out.String(), `"schema": "application-outcome/v1"`) {
		t.Fatalf("output = %s", out.String())
	}
}
