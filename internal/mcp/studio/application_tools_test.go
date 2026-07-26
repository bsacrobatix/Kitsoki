package studio_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	appplatform "kitsoki/internal/application"
	"kitsoki/internal/mcp/studio"
)

func writeStudioApplicationStory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	source := `
app: {id: studio-app, version: 1.0.0}
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
  name: Studio application
  description: Exercise the MCP application adapter.
  semantic_ref: studio-app.application
  shell: {entry: home}
  pages:
    home:
      name: Home
      description: Show the current application work.
      semantic_ref: studio-app.page.home
      regions: {}
  actions:
    studio-app.open:
      name: Open
      description: Open the selected item.
      semantic_ref: studio-app.action.open
      handler: studio-app.open
exports:
  handlers:
    studio-app.open:
      name: Open
      description: Open the selected item.
      semantic_ref: studio-app.handler.open
      input_schema: schemas/open.json
      output_schema: schemas/frame.json
      session: required
      effect: read
      routing_mode: exact
      outcomes: [ok]
      dispatch: {intent: open, state: ready, slots_from: input}
      expose: [mcp, jsonrpc, cli]
    studio-app.lookup:
      name: Lookup
      description: Look up an item without a session.
      semantic_ref: studio-app.handler.lookup
      input_schema: schemas/open.json
      output_schema: schemas/lookup.json
      session: none
      effect: pure
      routing_mode: exact
      outcomes: [ok]
      starlark: {script: scripts/lookup.star}
      expose: [mcp, jsonrpc]
    studio-app.create:
      name: Create
      description: Create a session and open the item.
      semantic_ref: studio-app.handler.create
      input_schema: schemas/open.json
      output_schema: schemas/frame.json
      session: create
      effect: read
      routing_mode: exact
      outcomes: [ok]
      dispatch: {intent: open, state: ready, slots_from: input}
      expose: [mcp, jsonrpc]
events:
  item-interrupted:
    source: studio-app.item.interrupted
    input_schema: schemas/open.json
    session: required
    mode: interrupt
    dispatch: {intent: open}
`
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "schemas"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "schemas", "open.json"), []byte(`{
		"type":"object",
		"required":["item_id"],
		"properties":{"item_id":{"type":"string"}}
	}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "schemas", "frame.json"), []byte(`{"type":"object"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "schemas", "lookup.json"), []byte(`{
		"type":"object",
		"required":["outcome","item_id"],
		"properties":{"outcome":{"const":"ok"},"item_id":{"type":"string"}}
	}`), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "scripts"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "scripts", "lookup.star"), []byte(`
def main(ctx):
    return {"outcome": "ok", "item_id": ctx.inputs["item_id"]}
`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "scripts", "lookup.star.yaml"), []byte(`
inputs:
  item_id: {type: string, required: true}
outputs:
  outcome: {type: string}
  item_id: {type: string}
`), 0o600))
	return path
}

func TestApplicationMCPFrameDiscoverAndCallShareDrivingRuntime(t *testing.T) {
	ctx := context.Background()
	srv, _ := newReplayServer(t)
	client := connectInProcess(ctx, t, srv)

	opened, err := callTool(ctx, client, "session.new", map[string]any{
		"story_path": writeStudioApplicationStory(t),
		"harness":    "replay",
		"trace":      filepath.Join(t.TempDir(), "trace.jsonl"),
	})
	require.NoError(t, err)
	require.False(t, opened.IsError, contentText(opened))
	var session studio.SessionOpenOK
	require.NoError(t, json.Unmarshal([]byte(contentText(opened)), &session))

	framed, err := callTool(ctx, client, "application.frame", map[string]any{"handle": session.Handle})
	require.NoError(t, err)
	require.False(t, framed.IsError, contentText(framed))
	var frame appplatform.Frame
	require.NoError(t, json.Unmarshal([]byte(contentText(framed)), &frame))
	require.Equal(t, appplatform.FrameSchema, frame.Schema)
	require.Equal(t, "home", frame.Page)

	discovered, err := callTool(ctx, client, "application.discover", map[string]any{"handle": session.Handle})
	require.NoError(t, err)
	require.False(t, discovered.IsError, contentText(discovered))
	var handlers []appplatform.HandlerDefinition
	require.NoError(t, json.Unmarshal([]byte(contentText(discovered)), &handlers))
	require.Len(t, handlers, 3)
	require.Equal(t, "studio-app.lookup", handlers[1].ID)

	called, err := callTool(ctx, client, "application.call", map[string]any{
		"handle": session.Handle, "handler": "studio-app.open",
		"input": map[string]any{"item_id": "item-1"},
	})
	require.NoError(t, err)
	require.False(t, called.IsError, contentText(called))
	var outcome appplatform.OutcomeEnvelope
	require.NoError(t, json.Unmarshal([]byte(contentText(called)), &outcome))
	require.Equal(t, appplatform.OutcomeSchema, outcome.Schema)
	require.Equal(t, appplatform.TransportMCP, outcome.Receipt.Transport)

	feedback, err := callTool(ctx, client, "application.feedback", map[string]any{
		"handle": session.Handle, "ref": "studio-app.page.home",
		"instruction": "The current work summary is unclear.",
	})
	require.NoError(t, err)
	require.False(t, feedback.IsError, contentText(feedback))
	var report struct {
		Schema string `json:"schema"`
		Anchor struct {
			SemanticElement struct {
				Plugin string `json:"plugin"`
				Ref    string `json:"ref"`
			} `json:"semantic_element"`
		} `json:"anchor"`
	}
	require.NoError(t, json.Unmarshal([]byte(contentText(feedback)), &report))
	require.Equal(t, "kitsoki.feedback.report.v1", report.Schema)
	require.Equal(t, "kitsoki.application", report.Anchor.SemanticElement.Plugin)
	require.Equal(t, "studio-app.page.home", report.Anchor.SemanticElement.Ref)
}

func TestApplicationMCPHonorsNoneAndCreateSessionPolicies(t *testing.T) {
	ctx := context.Background()
	srv, _ := newReplayServer(t)
	client := connectInProcess(ctx, t, srv)
	opened, err := callTool(ctx, client, "session.new", map[string]any{
		"story_path": writeStudioApplicationStory(t),
		"harness":    "replay",
		"trace":      filepath.Join(t.TempDir(), "trace.jsonl"),
	})
	require.NoError(t, err)
	require.False(t, opened.IsError, contentText(opened))
	var session studio.SessionOpenOK
	require.NoError(t, json.Unmarshal([]byte(contentText(opened)), &session))

	noneCall, err := callTool(ctx, client, "application.call", map[string]any{
		"handle": session.Handle, "handler": "studio-app.lookup",
		"input": map[string]any{"item_id": "item-1"},
	})
	require.NoError(t, err)
	require.False(t, noneCall.IsError, contentText(noneCall))
	var none appplatform.OutcomeEnvelope
	require.NoError(t, json.Unmarshal([]byte(contentText(noneCall)), &none))
	require.Empty(t, none.Receipt.SessionID)
	require.Nil(t, none.Frame)

	createCall, err := callTool(ctx, client, "application.call", map[string]any{
		"handle": session.Handle, "handler": "studio-app.create",
		"input": map[string]any{"item_id": "item-2"},
	})
	require.NoError(t, err)
	require.False(t, createCall.IsError, contentText(createCall))
	var created appplatform.OutcomeEnvelope
	require.NoError(t, json.Unmarshal([]byte(contentText(createCall)), &created))
	require.NotEmpty(t, created.Receipt.SessionID)
	require.NotEqual(t, session.Handle, created.Receipt.SessionID)
	require.NotNil(t, created.Frame)
	require.Equal(t, created.Receipt.SessionID, created.Frame.SessionID)

	eventCall, err := callTool(ctx, client, "application.event", map[string]any{
		"handle": session.Handle, "event": "item-interrupted",
		"input": map[string]any{"item_id": "item-3"},
	})
	require.NoError(t, err)
	require.False(t, eventCall.IsError, contentText(eventCall))
	var event appplatform.OutcomeEnvelope
	require.NoError(t, json.Unmarshal([]byte(contentText(eventCall)), &event))
	require.Equal(t, appplatform.EventInterrupt, event.Receipt.EventMode)
	require.Equal(t, "event-intent:item-interrupted", event.Handler)
}
