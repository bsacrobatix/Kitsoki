package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kitsoki/internal/app"
	appplatform "kitsoki/internal/application"
	"kitsoki/internal/host"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/runstatus"
)

type applicationTestSource struct {
	def    *app.AppDef
	header runstatus.SessionHeader
}

func (s applicationTestSource) Snapshot() (runstatus.Snapshot, error) {
	return runstatus.Snapshot{Session: s.header, App: s.def}, nil
}

func (s applicationTestSource) Events() ([]runstatus.TraceEvent, error) { return nil, nil }
func (s applicationTestSource) AppDef() *app.AppDef                     { return s.def }

type applicationTestProvider struct {
	mu      sync.Mutex
	def     *app.AppDef
	entries map[string]Entry
	created *captureDriver
}

type applicationBudgetFunc func(context.Context, appplatform.HandlerDefinition, appplatform.Invocation) (appplatform.BudgetDecision, error)

func (f applicationBudgetFunc) Decide(ctx context.Context, def appplatform.HandlerDefinition, invocation appplatform.Invocation) (appplatform.BudgetDecision, error) {
	return f(ctx, def, invocation)
}

type applicationReceiptCollector struct {
	mu       sync.Mutex
	receipts []appplatform.Receipt
}

func (c *applicationReceiptCollector) Record(_ context.Context, receipt appplatform.Receipt) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.receipts = append(c.receipts, receipt)
	return nil
}

func (p *applicationTestProvider) Get(id string) (Entry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.entries[id]
	return entry, ok
}

func (p *applicationTestProvider) List() []runstatus.SessionHeader {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]runstatus.SessionHeader, 0, len(p.entries))
	for _, entry := range p.entries {
		snapshot, _ := entry.Source.Snapshot()
		out = append(out, snapshot.Session)
	}
	return out
}

func (p *applicationTestProvider) NewSession(context.Context, string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	id := "created-session"
	p.created = &captureDriver{}
	p.entries[id] = Entry{
		Source: applicationTestSource{
			def: p.def,
			header: runstatus.SessionHeader{
				SessionID: id, AppID: "demo", CurrentState: "ready", Turn: 1,
			},
		},
		Driver: p.created,
	}
	return id, nil
}

func (*applicationTestProvider) Reload(context.Context, string) (bool, error) {
	return true, nil
}
func (*applicationTestProvider) Staleness(context.Context, string) (bool, string, error) {
	return false, "", nil
}
func (*applicationTestProvider) ListStories() []StoryHeader     { return nil }
func (*applicationTestProvider) Rescan() ([]StoryHeader, error) { return nil, nil }

func applicationRPCFixture(t *testing.T, opts ...Option) (*httptest.Server, *captureDriver, *Server) {
	t.Helper()
	def, err := app.LoadBytes([]byte(`
app: {id: demo, version: 1.0.0}
root: ready
world:
  catalog: {type: object, default: {}}
intents:
  open:
    title: Open
    slots:
      item_id: {type: string, required: true}
states:
  ready:
    on:
      open: [{target: ready}]
application:
  schema: application/v1
  name: Demo
  description: Exercise the shared application RPC boundary.
  semantic_ref: demo.application
  data:
    catalog:
      source: world.catalog
      sensitivity: internal
      policy: include
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
      description: Show the current work.
      semantic_ref: demo.page.home
      regions:
        main:
          name: Main
          description: Show the selected item and its actions.
          semantic_ref: demo.region.main
          items:
            - card:
                id: item
                name: Item
                description: Show one item.
                semantic_ref: demo.card.item
                actions: [demo.open, demo.intent]
  actions:
    demo.open:
      name: Open
      description: Open the selected item.
      semantic_ref: demo.action.open
      handler: demo.open
    demo.intent:
      name: Advance
      description: Advance through the offered story intent.
      semantic_ref: demo.action.intent
      intent: open
      state: ready
      input_schema: schemas/open.json
      routing_mode: exact
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
      dispatch: {intent: open, state: ready, slots_from: input}
      expose: [jsonrpc, web, vscode, cli, mcp]
    demo.lookup:
      name: Lookup
      description: Look up an item without a story session.
      semantic_ref: demo.handler.lookup
      input_schema: schemas/open.json
      output_schema: schemas/lookup.json
      session: none
      effect: pure
      routing_mode: exact
      outcomes: [ok]
      starlark: {script: scripts/lookup.star}
      expose: [jsonrpc, mcp]
    demo.create:
      name: Create
      description: Create a session and open the item.
      semantic_ref: demo.handler.create
      input_schema: schemas/open.json
      output_schema: schemas/frame.json
      session: create
      effect: read
      routing_mode: exact
      outcomes: [ok]
      dispatch: {intent: open, state: ready, slots_from: input}
      expose: [jsonrpc, mcp]
    demo.write:
      name: Write
      description: Write an item with authenticated attribution.
      semantic_ref: demo.handler.write
      input_schema: schemas/open.json
      output_schema: schemas/lookup.json
      session: none
      effect: write
      routing_mode: exact
      outcomes: [ok]
      idempotency: {key: input.request_id, scope: application}
      starlark: {script: scripts/lookup.star}
      expose: [jsonrpc]
    demo.cli-only:
      name: CLI only
      description: Exercise protocol-bound exposure.
      semantic_ref: demo.handler.cli-only
      input_schema: schemas/open.json
      output_schema: schemas/lookup.json
      session: none
      effect: pure
      routing_mode: exact
      outcomes: [ok]
      starlark: {script: scripts/lookup.star}
      expose: [cli]
events:
  item-updated:
    source: demo.item.updated
    input_schema: schemas/open.json
    session: required
    mode: background
    dispatch: {handler: demo.open}
  item-interrupted:
    source: demo.item.interrupted
    input_schema: schemas/open.json
    session: required
    mode: interrupt
    dispatch: {intent: open}
`))
	if err != nil {
		t.Fatal(err)
	}
	def.BaseDir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(def.BaseDir, "schemas"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(def.BaseDir, "schemas", "open.json"), []byte(`{
		"type":"object",
		"required":["item_id"],
		"properties":{"item_id":{"type":"string"}},
		"additionalProperties":true
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(def.BaseDir, "schemas", "frame.json"), []byte(`{"type":"object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(def.BaseDir, "schemas", "lookup.json"), []byte(`{
		"type":"object",
		"required":["outcome","item_id"],
		"properties":{"outcome":{"const":"ok"},"item_id":{"type":"string"}}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(def.BaseDir, "scripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(def.BaseDir, "scripts", "lookup.star"), []byte(`
def main(ctx):
    return {"outcome": "ok", "item_id": ctx.inputs["item_id"]}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(def.BaseDir, "scripts", "lookup.star.yaml"), []byte(`
inputs:
  item_id: {type: string, required: true}
outputs:
  outcome: {type: string}
  item_id: {type: string}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	driver := &captureDriver{world: map[string]any{
		"catalog": map[string]any{"revision": 1},
		"ambient": "/private/path",
	}}
	source := applicationTestSource{
		def: def,
		header: runstatus.SessionHeader{
			SessionID: "session-1", AppID: "demo", CurrentState: "ready", Turn: 12,
		},
	}
	provider := &applicationTestProvider{
		def: def, entries: map[string]Entry{"session-1": {Source: source, Driver: driver}},
	}
	live := NewMulti(provider, opts...)
	server := httptest.NewServer(live.Handler())
	t.Cleanup(server.Close)
	return server, driver, live
}

func applicationRPCCall(t *testing.T, server *httptest.Server, method string, params map[string]any, result any) *rpcError {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(server.URL+"/rpc", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error == nil && result != nil {
		if err := json.Unmarshal(envelope.Result, result); err != nil {
			t.Fatal(err)
		}
	}
	return envelope.Error
}

func TestApplicationRPCFrameDiscoverInspectAndAction(t *testing.T) {
	server, driver, _ := applicationRPCFixture(t)
	params := map[string]any{"session_id": "session-1"}

	var frame appplatform.Frame
	if rpcErr := applicationRPCCall(t, server, "runstatus.application.frame", params, &frame); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if frame.Schema != appplatform.FrameSchema || frame.Revision != 12 || frame.Page != "home" {
		t.Fatalf("frame = %#v", frame)
	}
	if got := string(frame.Data["catalog"].Value); got != `{"revision":1}` {
		t.Fatalf("frame catalog data = %s", got)
	}
	driver.world["catalog"] = map[string]any{"revision": 2}
	if rpcErr := applicationRPCCall(t, server, "runstatus.application.frame", params, &frame); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if got := string(frame.Data["catalog"].Value); got != `{"revision":2}` {
		t.Fatalf("refreshed frame catalog data = %s", got)
	}
	wire, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "ambient") || strings.Contains(string(wire), "/private/path") {
		t.Fatalf("frame leaked ambient world data: %s", wire)
	}

	var handlers []appplatform.HandlerDefinition
	if rpcErr := applicationRPCCall(t, server, "runstatus.application.discover", params, &handlers); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if len(handlers) != 4 || handlers[1].ID != "demo.lookup" {
		t.Fatalf("handlers = %#v", handlers)
	}

	var inspection appplatform.SemanticInspection
	inspectParams := map[string]any{"session_id": "session-1", "ref": "demo.card.item"}
	if rpcErr := applicationRPCCall(t, server, "runstatus.application.inspect", inspectParams, &inspection); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if inspection.Node.Source.Story != "demo" || inspection.Current.FrameRevision != 12 {
		t.Fatalf("inspection = %#v", inspection)
	}

	var outcome appplatform.OutcomeEnvelope
	actionParams := map[string]any{
		"session_id": "session-1", "action": "demo.open", "frame_revision": 12,
		"input": map[string]any{"item_id": "item-1"}, "actor": "operator-1",
	}
	if rpcErr := applicationRPCCall(t, server, "runstatus.application.action", actionParams, &outcome); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if outcome.Schema != appplatform.OutcomeSchema || outcome.Receipt.Transport != appplatform.TransportJSONRPC {
		t.Fatalf("outcome = %#v", outcome)
	}
	if outcome.SelectedImplementor != "ready" || outcome.Receipt.SelectedImplementor != "ready" {
		t.Fatalf("selected implementor outcome=%q receipt=%q", outcome.SelectedImplementor, outcome.Receipt.SelectedImplementor)
	}
	if got := driver.lastSlots["item_id"]; got != "item-1" {
		t.Fatalf("dispatched slots = %#v", driver.lastSlots)
	}
	if got := driver.lastSlots[authorSlot]; got != "operator-1" || outcome.Receipt.Actor != "operator-1" {
		t.Fatalf("actor slots=%#v receipt=%#v", driver.lastSlots, outcome.Receipt)
	}
}

func TestApplicationRPCFeedbackPersistsCanonicalSemanticAttachment(t *testing.T) {
	root := t.TempDir()
	server, _, _ := applicationRPCFixture(t, WithMaterializeRoot(root))
	params := map[string]any{
		"session_id": "session-1", "ref": "demo.card.item",
		"instruction":     "The selected item is unclear.",
		"idempotency_key": "feedback-demo-card-item",
	}
	var first struct {
		Report struct {
			Schema string                `json:"schema"`
			Anchor host.AnnotationAnchor `json:"anchor"`
		} `json:"report"`
		Receipt map[string]any `json:"receipt"`
	}
	if rpcErr := applicationRPCCall(t, server, "runstatus.application.feedback", params, &first); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if first.Report.Schema != "kitsoki.feedback.report.v1" ||
		first.Report.Anchor.SemanticElement == nil ||
		first.Report.Anchor.SemanticElement.Ref != "demo.card.item" ||
		first.Receipt["ref"] != "feedback-demo-card-item" {
		t.Fatalf("feedback response = %#v", first)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".artifacts", "feedback", "feedback.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), root) || !strings.Contains(string(raw), `"ref":"demo.card.item"`) {
		t.Fatalf("feedback ledger leaked local path or lost semantic ref: %s", raw)
	}
	var retry struct {
		Receipt map[string]any `json:"receipt"`
	}
	if rpcErr := applicationRPCCall(t, server, "runstatus.application.feedback", params, &retry); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if retry.Receipt["deduped"] != true {
		t.Fatalf("retry receipt = %#v", retry.Receipt)
	}
	raw, err = os.ReadFile(filepath.Join(root, ".artifacts", "feedback", "feedback.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimSpace(string(raw)), "\n") + 1; lines != 1 {
		t.Fatalf("ledger lines = %d", lines)
	}
}

func TestApplicationIntentRefusesSameNameInWrongRoom(t *testing.T) {
	driver := &captureDriver{}
	entry := Entry{
		Source: applicationTestSource{
			def: &app.AppDef{},
			header: runstatus.SessionHeader{
				SessionID: "session-1", CurrentState: "other-room",
			},
		},
		Driver: driver,
	}
	_, err := runApplicationIntent(
		context.Background(), entry, "demo.open", "open", "target-room", "",
		[]string{"ok"}, "exact", appplatform.Invocation{
			HandlerID: "demo.open", SessionID: "session-1", Transport: appplatform.TransportJSONRPC,
			Input: json.RawMessage(`{"item_id":"item-1"}`),
		},
	)
	if err == nil || !strings.Contains(err.Error(), "refusing same-name intent dispatch") {
		t.Fatalf("wrong-room dispatch error = %v", err)
	}
	if driver.lastSlots != nil {
		t.Fatalf("wrong-room intent reached driver: %#v", driver.lastSlots)
	}
}

func TestApplicationIntentPropagatesResolvedActorToHostContext(t *testing.T) {
	driver := &captureDriver{}
	entry := Entry{
		Source: applicationTestSource{
			def: &app.AppDef{},
			header: runstatus.SessionHeader{
				SessionID: "session-1", CurrentState: "ready",
			},
		},
		Driver: driver,
	}
	_, err := runApplicationIntent(
		context.Background(), entry, "demo.intent", "open", "ready", "",
		[]string{"ok"}, "exact", appplatform.Invocation{
			HandlerID: "demo.intent", SessionID: "session-1", Actor: "operator-1",
			Transport: appplatform.TransportWeb, Input: json.RawMessage(`{"item_id":"item-1"}`),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if driver.lastActor != "operator-1" {
		t.Fatalf("host actor = %q, want operator-1", driver.lastActor)
	}
}

func TestApplicationStarlarkUsesSessionHostRegistry(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "host.star")
	if err := os.WriteFile(script, []byte(`
def main(ctx):
    result = ctx.host.call("host.workspace_manager.get", {"id": "workspace-1"})
    return {"outcome": "ok", "id": result["id"]}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script+".yaml", []byte(`
outputs:
  outcome: {type: string}
  id: {type: string}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	hostRegistry := host.NewRegistry()
	called := false
	hostRegistry.Register("host.workspace_manager.get", func(ctx context.Context, args map[string]any) (host.Result, error) {
		called = true
		if actor := host.ActorFromContext(ctx); actor != "operator-1" {
			return host.Result{}, fmt.Errorf("host actor = %q, want operator-1", actor)
		}
		return host.Result{Data: map[string]any{"id": args["id"]}}, nil
	})
	result, err := runApplicationStarlark(
		context.Background(), Entry{}, &app.AppDef{BaseDir: root}, "demo.host",
		&app.ApplicationHandler{
			Outcomes: []string{"ok"},
			Starlark: &app.ApplicationStarlarkHandler{
				Script: "host.star",
				Capabilities: map[string]any{
					"host": map[string]any{"verbs": []any{"host.workspace_manager.get"}},
				},
			},
		},
		appplatform.Invocation{Actor: "operator-1", Transport: appplatform.TransportJSONRPC},
		hostRegistry,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !called || result.Outcome != "ok" || !strings.Contains(string(result.Output), `"id":"workspace-1"`) {
		t.Fatalf("host-backed Starlark result=%#v called=%v", result, called)
	}
}

func TestApplicationIntentActionUsesCanonicalServerReplay(t *testing.T) {
	replay := appplatform.NewMemoryReplayStore()
	receipts := &applicationReceiptCollector{}
	server, driver, _ := applicationRPCFixture(t, WithApplicationDependencies(appplatform.Dependencies{
		Replay: replay, Receipts: receipts,
	}))
	params := map[string]any{
		"session_id": "session-1", "action": "demo.intent", "frame_revision": 12,
		"input": map[string]any{"item_id": "item-1"}, "actor": "operator-1",
		"idempotency_key": "untrusted-client-key-1",
	}
	var first appplatform.OutcomeEnvelope
	if rpcErr := applicationRPCCall(t, server, "runstatus.application.web_action", params, &first); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	params["idempotency_key"] = "untrusted-client-key-2"
	var second appplatform.OutcomeEnvelope
	if rpcErr := applicationRPCCall(t, server, "runstatus.application.web_action", params, &second); rpcErr != nil {
		t.Fatal(rpcErr)
	}

	if driver.submits != 1 {
		t.Fatalf("intent effects = %d, want 1", driver.submits)
	}
	if driver.lastActor != "operator-1" {
		t.Fatalf("host actor = %q, want operator-1", driver.lastActor)
	}
	wantKey := applicationIntentActionKey(12)
	if first.Receipt.IdempotencyKey != wantKey || second.Receipt.IdempotencyKey != wantKey {
		t.Fatalf("receipt keys first=%q second=%q, want %q",
			first.Receipt.IdempotencyKey, second.Receipt.IdempotencyKey, wantKey)
	}
	if first.Receipt.Replayed || !second.Receipt.Replayed || second.Receipt.ReplayOf != first.Receipt.ID {
		t.Fatalf("first receipt=%#v second receipt=%#v", first.Receipt, second.Receipt)
	}
	params["input"] = map[string]any{"item_id": "item-2"}
	if rpcErr := applicationRPCCall(t, server, "runstatus.application.web_action", params, nil); rpcErr == nil ||
		!strings.Contains(rpcErr.Message, "idempotency key reused with different input") {
		t.Fatalf("conflicting replay error = %#v", rpcErr)
	}
	if driver.submits != 1 {
		t.Fatalf("intent effects after conflicting replay = %d, want 1", driver.submits)
	}
	receipts.mu.Lock()
	defer receipts.mu.Unlock()
	if len(receipts.receipts) != 2 {
		t.Fatalf("receipts = %d, want effect receipt plus replay receipt", len(receipts.receipts))
	}
}

func TestApplicationRPCValidatesSchemaAndAuthenticatedActor(t *testing.T) {
	server, driver, _ := applicationRPCFixture(t)
	rpcErr := applicationRPCCall(t, server, "runstatus.application.call", map[string]any{
		"session_id": "session-1", "handler": "demo.open", "input": map[string]any{},
	}, nil)
	if rpcErr == nil || !strings.Contains(rpcErr.Message, "validate input") {
		t.Fatalf("schema error = %#v", rpcErr)
	}
	if driver.lastSlots != nil {
		t.Fatalf("invalid input reached driver: %#v", driver.lastSlots)
	}

	params := map[string]any{
		"session_id": "session-1", "handler": "demo.write",
		"input": map[string]any{"item_id": "item-1", "request_id": "request-1"},
	}
	rpcErr = applicationRPCCall(t, server, "runstatus.application.call", params, nil)
	if rpcErr == nil || !strings.Contains(rpcErr.Message, "authenticated actor") {
		t.Fatalf("authorization error = %#v", rpcErr)
	}
	params["actor"] = "operator-1"
	var outcome appplatform.OutcomeEnvelope
	if rpcErr := applicationRPCCall(t, server, "runstatus.application.call", params, &outcome); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if outcome.Receipt.Actor != "operator-1" || outcome.Receipt.IdempotencyKey != "request-1" {
		t.Fatalf("authenticated receipt = %#v", outcome.Receipt)
	}
}

func TestApplicationRPCUsesInjectedDeploymentBudgetPolicy(t *testing.T) {
	server, _, _ := applicationRPCFixture(t, WithApplicationDependencies(appplatform.Dependencies{
		Budget: applicationBudgetFunc(func(context.Context, appplatform.HandlerDefinition, appplatform.Invocation) (appplatform.BudgetDecision, error) {
			return appplatform.BudgetDecision{
				Allowed: false, Code: "deployment_limit", Reason: "deployment budget exhausted",
			}, nil
		}),
	}))
	rpcErr := applicationRPCCall(t, server, "runstatus.application.call", map[string]any{
		"session_id": "session-1", "handler": "demo.lookup",
		"input": map[string]any{"item_id": "item-1"},
	}, nil)
	if rpcErr == nil || !strings.Contains(rpcErr.Message, "deployment budget exhausted") {
		t.Fatalf("injected budget error = %#v", rpcErr)
	}
}

func TestApplicationRPCTransportCannotBeSpoofed(t *testing.T) {
	server, _, _ := applicationRPCFixture(t)
	var handlers []appplatform.HandlerDefinition
	if rpcErr := applicationRPCCall(t, server, "runstatus.application.discover", map[string]any{
		"session_id": "session-1", "transport": "cli",
	}, &handlers); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	for _, handler := range handlers {
		if handler.ID == "demo.cli-only" {
			t.Fatal("JSON-RPC discovery trusted spoofed CLI transport")
		}
	}
	rpcErr := applicationRPCCall(t, server, "runstatus.application.call", map[string]any{
		"session_id": "session-1", "handler": "demo.cli-only", "transport": "cli",
		"input": map[string]any{"item_id": "item-1"},
	}, nil)
	if rpcErr == nil || !strings.Contains(rpcErr.Message, "not exposed") {
		t.Fatalf("spoofed transport call error = %#v", rpcErr)
	}

	var action appplatform.OutcomeEnvelope
	if rpcErr := applicationRPCCall(t, server, "runstatus.application.action", map[string]any{
		"session_id": "session-1", "action": "demo.open", "frame_revision": 12,
		"transport": "web", "input": map[string]any{"item_id": "item-1"},
	}, &action); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if action.Receipt.Transport != appplatform.TransportJSONRPC {
		t.Fatalf("generic action trusted spoofed transport: %#v", action.Receipt)
	}
}

func TestApplicationCLIProtocolMethodUsesCLITransport(t *testing.T) {
	server, _, _ := applicationRPCFixture(t)
	var outcome appplatform.OutcomeEnvelope
	if rpcErr := applicationRPCCall(t, server, "runstatus.application.cli_call", map[string]any{
		"session_id": "session-1", "handler": "demo.cli-only",
		"transport": "jsonrpc",
		"input":     map[string]any{"item_id": "item-1"},
	}, &outcome); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if outcome.Receipt.Transport != appplatform.TransportCLI {
		t.Fatalf("receipt transport = %q, want %q", outcome.Receipt.Transport, appplatform.TransportCLI)
	}
	if string(outcome.Output) != `{"item_id":"item-1","outcome":"ok"}` {
		t.Fatalf("CLI-only output = %s", outcome.Output)
	}
}

func TestApplicationSurfaceActionMethodsUseBoundTransports(t *testing.T) {
	server, _, _ := applicationRPCFixture(t)
	tests := []struct {
		method    string
		transport appplatform.Transport
	}{
		{method: "runstatus.application.web_action", transport: appplatform.TransportWeb},
		{method: "runstatus.application.vscode_action", transport: appplatform.TransportVSCode},
	}
	for _, tc := range tests {
		t.Run(string(tc.transport), func(t *testing.T) {
			var outcome appplatform.OutcomeEnvelope
			if rpcErr := applicationRPCCall(t, server, tc.method, map[string]any{
				"session_id": "session-1", "action": "demo.open", "frame_revision": 12,
				"transport": "tui", "input": map[string]any{"item_id": "item-1"},
			}, &outcome); rpcErr != nil {
				t.Fatal(rpcErr)
			}
			if outcome.Receipt.Transport != tc.transport {
				t.Fatalf("receipt transport = %q, want %q", outcome.Receipt.Transport, tc.transport)
			}
		})
	}
}

func TestApplicationRPCSessionNoneStarlarkAndSessionCreate(t *testing.T) {
	server, _, _ := applicationRPCFixture(t)
	var none appplatform.OutcomeEnvelope
	if rpcErr := applicationRPCCall(t, server, "runstatus.application.call", map[string]any{
		"session_id": "session-1", "handler": "demo.lookup",
		"input": map[string]any{"item_id": "item-1"},
	}, &none); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if none.Receipt.SessionID != "" || none.Frame != nil ||
		string(none.Output) != `{"item_id":"item-1","outcome":"ok"}` {
		t.Fatalf("session:none outcome = %#v", none)
	}

	var created appplatform.OutcomeEnvelope
	if rpcErr := applicationRPCCall(t, server, "runstatus.application.call", map[string]any{
		"session_id": "session-1", "handler": "demo.create",
		"input": map[string]any{"item_id": "item-2"},
	}, &created); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if created.Receipt.SessionID != "created-session" || created.Frame == nil ||
		created.Frame.SessionID != "created-session" {
		t.Fatalf("session:create outcome = %#v", created)
	}
}

func TestApplicationRPCRejectsStaleFrameAndSharesRegistryWithEvents(t *testing.T) {
	server, driver, live := applicationRPCFixture(t)
	stale := map[string]any{
		"session_id": "session-1", "action": "demo.open", "frame_revision": 11,
		"input": map[string]any{"item_id": "item-1"},
	}
	rpcErr := applicationRPCCall(t, server, "runstatus.application.action", stale, nil)
	if rpcErr == nil || rpcErr.Message == "" {
		t.Fatalf("stale action error = %#v", rpcErr)
	}

	var outcome appplatform.OutcomeEnvelope
	event := map[string]any{
		"session_id": "session-1", "event": "item-updated",
		"input": map[string]any{"item_id": "item-2"},
	}
	if rpcErr := applicationRPCCall(t, server, "runstatus.application.event", event, &outcome); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if outcome.Receipt.EventID != "item-updated" || outcome.Receipt.Transport != appplatform.TransportEvent {
		t.Fatalf("event outcome = %#v", outcome)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := live.applicationEventSched.WaitIdle(waitCtx); err != nil {
		t.Fatal(err)
	}
	if got := driver.lastSlots["item_id"]; got != "item-2" {
		t.Fatalf("event slots = %#v", driver.lastSlots)
	}

	var interrupted appplatform.OutcomeEnvelope
	interrupt := map[string]any{
		"session_id": "session-1", "event": "item-interrupted",
		"input": map[string]any{"item_id": "item-3"},
	}
	if rpcErr := applicationRPCCall(t, server, "runstatus.application.event", interrupt, &interrupted); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if interrupted.Handler != "event-intent:item-interrupted" ||
		interrupted.Receipt.EventMode != appplatform.EventInterrupt ||
		driver.lastSlots["item_id"] != "item-3" {
		t.Fatalf("interrupt intent event outcome=%#v slots=%#v", interrupted, driver.lastSlots)
	}
}

func TestApplicationRPCDoesNotReportRejectedTurnAsSuccess(t *testing.T) {
	server, driver, _ := applicationRPCFixture(t)
	driver.outcome = &orchestrator.TurnOutcome{
		Mode: orchestrator.ModeRejected, ErrorCode: "GUARD_FAILED",
		ErrorMessage: "not available in the current state",
	}
	params := map[string]any{
		"session_id": "session-1", "handler": "demo.open",
		"input": map[string]any{"item_id": "item-1"},
	}
	rpcErr := applicationRPCCall(t, server, "runstatus.application.call", params, nil)
	if rpcErr == nil || rpcErr.Message == "" {
		t.Fatalf("rejected handler error = %#v", rpcErr)
	}
}
