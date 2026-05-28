package orchestrator_test

// Regression: orchestrator.SubmitDirect must carry the typed-view
// payload (TypedView, RenderEnv, Renderer) on its success path, the
// same way Orchestrator.Turn does. The TUI's choice-widget auto-focus
// at handleTurnOutcome runs `findChoiceElement(out.TypedView)`, so a
// nil TypedView from SubmitDirect left the next state's interactive
// picker unopened — the user landed in a state that LOOKED right but
// could not advance because the widget never seized focus.
//
// The fix populated TypedView / RenderEnv / Renderer at the
// submitDirect success-path return (mirrors the Turn-path return at
// orchestrator.go:893-904).

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/machine"
	"kitsoki/internal/orchestrator"
)

// TestSubmitDirectPropagatesTypedView pins the fix so a future change
// to submitDirect cannot drop the typed-view payload again. Loads the
// choice_smoke fixture (intro → single_basic on `begin`), submits the
// `begin` intent directly, and asserts the resulting outcome carries
// a typed view with the next state's choice element exposed.
func TestSubmitDirectPropagatesTypedView(t *testing.T) {
	t.Parallel()

	def, err := app.Load("../../testdata/apps/choice_smoke/app.yaml")
	require.NoError(t, err)

	m, err := machine.New(def)
	require.NoError(t, err)

	s, err := newTestStore(t)
	require.NoError(t, err)

	// SubmitDirect doesn't use the harness, so a nil harness is safe.
	orch := orchestrator.New(def, m, s, nil)
	ctx := context.Background()

	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)

	// intro is the root state; "begin" routes to single_basic.
	out, err := orch.SubmitDirect(ctx, sid, "begin", nil)
	require.NoError(t, err)
	require.Equal(t, orchestrator.ModeTransitioned, out.Mode)
	require.Equal(t, app.StatePath("single_basic"), out.NewState)

	// Pin the regression: typed-view payload must be populated.
	require.NotNil(t, out.TypedView,
		"SubmitDirect success path must populate TypedView "+
			"(otherwise the TUI choice-widget auto-focus stays closed)")
	require.NotNil(t, out.Renderer,
		"SubmitDirect success path must populate Renderer")
	// RenderEnv is a value type (expr.Env); presence is satisfied
	// implicitly. Sanity check that the typed view's elements made
	// it through — single_basic carries a heading, several prose
	// elements, and exactly one choice element.
	var sawChoice bool
	for _, el := range out.TypedView.Elements {
		if el.Kind == "choice" {
			sawChoice = true
			break
		}
	}
	require.True(t, sawChoice,
		"single_basic's typed view must surface its choice element to the TUI")
}
