package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"kitsoki/internal/app"
	appplatform "kitsoki/internal/application"
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

func applicationRPCFixture(t *testing.T) (*httptest.Server, *captureDriver) {
	t.Helper()
	def, err := app.LoadBytes([]byte(`
app: {id: demo, version: 1.0.0}
root: ready
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
                actions: [demo.open]
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
      dispatch: {intent: open, state: ready, slots_from: input}
      expose: [jsonrpc, web, cli, mcp]
events:
  item-updated:
    source: demo.item.updated
    input_schema: schemas/open.json
    session: required
    mode: background
    dispatch: {handler: demo.open}
`))
	if err != nil {
		t.Fatal(err)
	}
	driver := &captureDriver{}
	source := applicationTestSource{
		def: def,
		header: runstatus.SessionHeader{
			SessionID: "session-1", AppID: "demo", CurrentState: "ready", Turn: 12,
		},
	}
	server := httptest.NewServer(NewWithSource(source, WithDriver(driver)).Handler())
	t.Cleanup(server.Close)
	return server, driver
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
	server, driver := applicationRPCFixture(t)
	params := map[string]any{"session_id": "session-1"}

	var frame appplatform.Frame
	if rpcErr := applicationRPCCall(t, server, "runstatus.application.frame", params, &frame); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if frame.Schema != appplatform.FrameSchema || frame.Revision != 12 || frame.Page != "home" {
		t.Fatalf("frame = %#v", frame)
	}

	var handlers []appplatform.HandlerDefinition
	if rpcErr := applicationRPCCall(t, server, "runstatus.application.discover", params, &handlers); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if len(handlers) != 1 || handlers[0].ID != "demo.open" {
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
	if outcome.Schema != appplatform.OutcomeSchema || outcome.Receipt.Transport != appplatform.TransportWeb {
		t.Fatalf("outcome = %#v", outcome)
	}
	if got := driver.lastSlots["item_id"]; got != "item-1" {
		t.Fatalf("dispatched slots = %#v", driver.lastSlots)
	}
	if got := driver.lastSlots[authorSlot]; got != "operator-1" || outcome.Receipt.Actor != "operator-1" {
		t.Fatalf("actor slots=%#v receipt=%#v", driver.lastSlots, outcome.Receipt)
	}
}

func TestApplicationRPCRejectsStaleFrameAndSharesRegistryWithEvents(t *testing.T) {
	server, driver := applicationRPCFixture(t)
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
	if got := driver.lastSlots["item_id"]; got != "item-2" {
		t.Fatalf("event slots = %#v", driver.lastSlots)
	}
}

func TestApplicationRPCDoesNotReportRejectedTurnAsSuccess(t *testing.T) {
	server, driver := applicationRPCFixture(t)
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
