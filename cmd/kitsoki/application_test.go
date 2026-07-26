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

func TestApplicationDescribeKeepsImportedSchemaReferencesPortable(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	parent := filepath.Join(root, "parent")
	if err := os.MkdirAll(filepath.Join(child, "schemas"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(child, "ui"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(child, "schemas", "input.json"),
		[]byte(`{"$id":"input.json","type":"object"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "ui", "panel.js"), []byte("export default {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "app.yaml"), []byte(`
app: {id: child, version: 1.0.0}
root: idle
intents:
  run: {title: Run, description: Run the imported action.}
states:
  idle:
    description: Imported room.
    on:
      run: [{target: idle}]
exports:
  intents: [run]
  application:
    actions: [child.run]
    components: [child.panel]
    schemas: [input]
application:
  schema: application/v1
  name: Child
  description: Imported application members.
  semantic_ref: child.application
  schemas:
    input: schemas/input.json
  actions:
    child.run:
      name: Run
      description: Run the imported action.
      semantic_ref: child.action.run
      intent: run
      input_schema: schemas/input.json
  components:
    child.panel:
      name: Panel
      description: Imported panel.
      semantic_ref: child.component.panel
      web: {module: ui/panel.js}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	parentPath := filepath.Join(parent, "app.yaml")
	if err := os.WriteFile(parentPath, []byte(`
app: {id: parent, version: 1.0.0}
root: ready
imports:
  module:
    source: ../child
    entry: idle
states:
  ready: {description: Parent room.}
application:
  schema: application/v1
  name: Parent
  description: Compose imported application members.
  semantic_ref: parent.application
  shell: {entry: home}
  pages:
    home:
      name: Home
      description: Parent home.
      semantic_ref: parent.page.home
      regions: {}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := applicationDescribeCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{parentPath})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"input_schema": "schemas/input.json"`) ||
		!strings.Contains(out.String(), `"parent.module.input": "schemas/input.json"`) ||
		!strings.Contains(out.String(), `"module": "ui/panel.js"`) {
		t.Fatalf("portable describe output = %s", out.String())
	}
	canonicalRoot, _ := filepath.EvalSymlinks(root)
	for _, forbidden := range []string{root, filepath.ToSlash(root), canonicalRoot, filepath.ToSlash(canonicalRoot)} {
		if forbidden != "" && strings.Contains(out.String(), forbidden) {
			t.Fatalf("describe leaked imported root %q: %s", forbidden, out.String())
		}
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
	if gotMethod != "runstatus.application.cli_call" {
		t.Fatalf("method = %q", gotMethod)
	}
	if _, ok := gotParams["transport"]; ok {
		t.Fatalf("CLI supplied a client-controlled transport: %#v", gotParams)
	}
	if gotParams["session_id"] != "session-1" {
		t.Fatalf("params = %#v", gotParams)
	}
	if !strings.Contains(out.String(), `"schema": "application-outcome/v1"`) {
		t.Fatalf("output = %s", out.String())
	}
}

func TestApplicationFeedbackCommandUsesSharedReviewedSink(t *testing.T) {
	var gotMethod string
	var gotParams map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var rpc struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.NewDecoder(request.Body).Decode(&rpc); err != nil {
			t.Fatal(err)
		}
		gotMethod, gotParams = rpc.Method, rpc.Params
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{
			"report":{"schema":"kitsoki.feedback.report.v1","anchor":{
				"kind":"semantic_element","semantic_element":{
					"plugin":"kitsoki.application","ref":"demo.card.item"
				}
			}},
			"receipt":{"ref":"feedback-1","deduped":false}
		}}`)
	}))
	t.Cleanup(server.Close)

	cmd := applicationFeedbackCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{
		"demo.card.item", "--url", server.URL, "--session-id", "session-1",
		"--instruction", "The item summary is unclear.",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "runstatus.application.feedback" ||
		gotParams["session_id"] != "session-1" ||
		gotParams["ref"] != "demo.card.item" {
		t.Fatalf("method=%q params=%#v", gotMethod, gotParams)
	}
	if !strings.Contains(out.String(), `"ref": "demo.card.item"`) {
		t.Fatalf("output = %s", out.String())
	}
}
