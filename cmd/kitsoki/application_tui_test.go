package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"kitsoki/internal/app"
	appplatform "kitsoki/internal/application"
	"kitsoki/internal/harness"
	"kitsoki/internal/machine"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/store"
)

type tuiApplicationHarness struct{}

func (tuiApplicationHarness) RunTurn(context.Context, harness.TurnInput) (mcp.CallToolParams, error) {
	return mcp.CallToolParams{}, nil
}

func (tuiApplicationHarness) Close() error { return nil }

func TestTUIApplicationDispatcherUsesSharedHandlerServiceAndDurableReceipt(t *testing.T) {
	dir := t.TempDir()
	writeTUIApplicationFixture(t, dir)
	def, err := app.Load(filepath.Join(dir, "app.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	mach, err := machine.New(def)
	if err != nil {
		t.Fatal(err)
	}
	sessionStore, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessionStore.Close() })
	orch := orchestrator.New(def, mach, sessionStore, tuiApplicationHarness{})
	sid, err := orch.NewSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tracePath := filepath.Join(dir, "session.jsonl")
	dispatcher, err := newTUIApplicationActionDispatcher(orch, sid, tracePath, "operator")
	if err != nil {
		t.Fatal(err)
	}

	result, err := dispatcher(context.Background(), appplatform.ActionEnvelope{
		Action: "demo.open", Input: []byte(`{"item_id":"item-1"}`),
		SessionID: string(sid), FrameRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.View == nil || result.View.NewState != "done" {
		t.Fatalf("settled view = %#v", result.View)
	}
	receipt := result.Outcome.Receipt
	if receipt.HandlerID != "demo.open-handler" ||
		receipt.SemanticRef != "demo.handler.open" ||
		receipt.FrameRevision != 0 ||
		receipt.Transport != appplatform.TransportTUI ||
		receipt.Actor != "operator" {
		t.Fatalf("receipt = %#v", receipt)
	}
	journalPath := filepath.Join(dir, "session.application.jsonl")
	if raw, readErr := os.ReadFile(journalPath); readErr != nil || len(raw) == 0 {
		t.Fatalf("durable application journal = %q, err=%v", raw, readErr)
	}

	_, err = dispatcher(context.Background(), appplatform.ActionEnvelope{
		Action: "demo.open", SessionID: string(sid), FrameRevision: 0,
	})
	if !errors.Is(err, appplatform.ErrStaleFrame) {
		t.Fatalf("stale action error = %v", err)
	}
}

func writeTUIApplicationFixture(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "schemas"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"input.json", "output.json"} {
		if err := os.WriteFile(
			filepath.Join(dir, "schemas", name),
			[]byte(`{"type":"object","additionalProperties":true}`),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
	const definition = `
app:
  id: demo
  version: 1.0.0
root: ready
intents:
  open:
    title: Open
    description: Open the item.
states:
  ready:
    view: Ready
    on:
      open:
        - target: done
  done:
    terminal: true
    view: Done
application:
  schema: application/v1
  name: Demo
  description: Demo application.
  semantic_ref: demo.application
  shell: {entry: home}
  pages:
    home:
      name: Home
      description: Home page.
      semantic_ref: demo.page.home
      regions:
        main:
          name: Main
          description: Main region.
          semantic_ref: demo.region.main
          items:
            - card:
                id: item
                name: Item
                description: Current item.
                semantic_ref: demo.card.item
                actions: [demo.open]
  actions:
    demo.open:
      name: Open
      description: Open the item.
      semantic_ref: demo.action.open
      handler: demo.open-handler
  surfaces:
    tui: {projection: cards}
exports:
  handlers:
    demo.open-handler:
      name: Open
      description: Open the item.
      semantic_ref: demo.handler.open
      input_schema: schemas/input.json
      output_schema: schemas/output.json
      session: required
      effect: read
      routing_mode: exact
      outcomes: [ok]
      dispatch: {intent: open, state: ready, slots_from: input}
      expose: [tui]
`
	if err := os.WriteFile(filepath.Join(dir, "app.yaml"), []byte(definition), 0o600); err != nil {
		t.Fatal(err)
	}
}
