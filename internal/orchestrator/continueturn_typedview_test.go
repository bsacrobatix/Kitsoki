package orchestrator_test

// Regression: orchestrator.ContinueTurn must carry the typed-view
// payload (TypedView, RenderEnv, Renderer) on its success path, the
// same way SubmitDirect and Turn do.
//
// Backstory: a prior fix added TypedView/RenderEnv/Renderer to the
// SubmitDirect success return (orchestrator.go:1978-1989) so the TUI's
// choice-widget auto-focus could find the next state's choice element.
// The sibling ContinueTurn success return (orchestrator.go:2476-2483)
// was missed — any state reached via clarify-completion that declared
// a choice widget landed the user in a non-interactive room because
// handleTurnOutcome's findChoiceElement(out.TypedView) ran against nil.
// The fix copies the same TypedView/RenderEnv/Renderer fields off
// result onto the outcome at the ContinueTurn success seam.

import (
	"context"
	"testing"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/harness"
	"kitsoki/internal/machine"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/store"
)

// continueHarness returns a single fixed intent call. Used to drive
// the clarify-then-continue path: the first Turn fires the intent
// without the required slot (orchestrator suspends → ModeClarify);
// ContinueTurn then supplies the missing slot and finalises the turn.
type continueHarness struct {
	intentName string
	slots      map[string]any
}

func (h *continueHarness) RunTurn(_ context.Context, _ harness.TurnInput) (mcp.CallToolParams, error) {
	args := map[string]any{"intent": h.intentName}
	if h.slots != nil {
		args["slots"] = h.slots
	}
	return mcp.CallToolParams{Name: "transition", Arguments: args}, nil
}

func (h *continueHarness) Close() error { return nil }

// TestContinueTurnPropagatesTypedView pins the fix so a future change
// to the ContinueTurn success seam cannot drop the typed-view payload
// again. Builds a tiny inline app whose `move` intent requires a
// `direction` slot, suspends on the first Turn (clarify), then
// finishes via ContinueTurn — the destination state carries a typed
// choice element, which is what the TUI's auto-focus inspects.
func TestContinueTurnPropagatesTypedView(t *testing.T) {
	t.Parallel()

	const appYAML = `
app:
  id: continue-typedview-test
  version: 0.1.0

world: {}

intents:
  move:
    title: "Move"
    slots:
      direction:
        type: enum
        values: [north, south]
        required: true
        prompt: "Which direction?"
  pick:
    title: "Pick"

root: start

states:
  start:
    view: "You are at the start."
    on:
      move:
        - when: "slots.direction == 'north'"
          target: picker
        - default: true
          target: start

  picker:
    description: "Destination room with a choice widget."
    view:
      - heading: "Pick one"
      - choice:
          mode: single
          prompt: "Pick"
          items:
            - { label: "A", intent: pick }
            - { label: "B", intent: pick }
    on:
      pick: [ { target: start } ]
`

	def, err := app.LoadBytes([]byte(appYAML))
	require.NoError(t, err)

	m, err := machine.New(def)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	// Harness returns `move` with NO slots → first Turn lands in clarify.
	h := &continueHarness{intentName: "move", slots: nil}

	orch := orchestrator.New(def, m, s, h)
	ctx := context.Background()

	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)

	// Turn → ModeClarify (missing direction slot).
	out, err := orch.Turn(ctx, sid, "go somewhere")
	require.NoError(t, err)
	require.Equal(t, orchestrator.ModeClarify, out.Mode, "first Turn should suspend on missing slot")
	require.Equal(t, "move", out.PendingIntent)

	// ContinueTurn → success-path return. THIS is the seam under test.
	cont, err := orch.ContinueTurn(ctx, sid, map[string]any{"direction": "north"})
	require.NoError(t, err)
	require.Equal(t, orchestrator.ModeTransitioned, cont.Mode)
	require.Equal(t, app.StatePath("picker"), cont.NewState)

	// Pin the regression: typed-view payload must be populated on the
	// ContinueTurn outcome, mirroring SubmitDirect / Turn.
	require.NotNil(t, cont.TypedView,
		"ContinueTurn success path must populate TypedView "+
			"(otherwise the TUI choice-widget auto-focus stays closed)")
	require.NotNil(t, cont.Renderer,
		"ContinueTurn success path must populate Renderer")
	// RenderEnv is a value type (expr.Env); existence is implicit. Verify
	// the typed view's elements made it through and surface the choice
	// element the TUI expects to find on the new room.
	var sawChoice bool
	for _, el := range cont.TypedView.Elements {
		if el.Kind == "choice" {
			sawChoice = true
			break
		}
	}
	require.True(t, sawChoice,
		"picker's typed view must surface its choice element to the TUI")
}
