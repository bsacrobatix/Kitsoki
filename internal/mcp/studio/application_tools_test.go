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
	path := filepath.Join(t.TempDir(), "app.yaml")
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
`
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))
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
	require.Len(t, handlers, 1)
	require.Equal(t, "studio-app.open", handlers[0].ID)

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
}
