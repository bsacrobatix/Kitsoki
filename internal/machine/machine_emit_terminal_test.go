// machine_emit_terminal_test.go — regression for the intermittent worker flake
// where a bugfix drive stopped at the non-terminal room bf.reproducing and the
// worker exited `capsule ci: verdict schema ""` (outcome 'missing').
//
// Root cause (observed on exec vmpool-62ceb14e84ad): the reproducing room's
// on_enter queued TWO sibling emit_intents in one settle batch — the
// deterministic GREEN-gate's `not_reproducible` (which routes to the terminal
// @exit verdict_not_reproducible) AND the LLM judge's `accept`. dispatchEmittedIntents
// fired `not_reproducible` first, transitioning into the TERMINAL @exit, then
// kept iterating the batch and tried to dispatch the queued `accept` AT that
// terminal state — which has no forward `on:` arm — producing
// `emit_intent "accept" at "...": no transition arm matched`. That settle error
// stranded the whole synchronous DriveToRest at the pre-emit (non-terminal)
// room with no ci_verdict.
//
// The fix: once a sibling emit has transitioned into a terminal @exit, the
// remaining queued siblings are stale (the room has been left) and must be
// dropped, not dispatched. A terminal state can never host a forward intent, so
// this only ever replaces a guaranteed error with clean termination.
package machine_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/intent"
	"kitsoki/internal/world"
)

// TestEmitIntent_SiblingAfterTerminalExitIsDropped is the minimal reproduction
// of the dispatchEmittedIntents collision. `reproducing` on_enter queues two
// sibling emits: `exit_now` (→ terminal @exit `not_reproducible`) then `accept`
// (which has an arm HERE at reproducing, so load-validation is satisfied, but
// NONE at the terminal state). Before the fix the queued `accept` is dispatched
// at the terminal state and errors ("no transition arm matched"); after the fix
// the drive terminates cleanly at `not_reproducible`.
func TestEmitIntent_SiblingAfterTerminalExitIsDropped(t *testing.T) {
	def := &app.AppDef{
		App:  app.AppMeta{ID: "emit-terminal-collision"},
		Root: "start",
		Intents: map[string]app.Intent{
			"go":       {},
			"exit_now": {},
			"accept":   {},
		},
		States: map[string]*app.State{
			"start": {
				On: map[string][]app.Transition{
					"go": {{Target: "reproducing"}},
				},
			},
			"reproducing": {
				// The exact failing shape: the GREEN-gate exit emit is authored
				// (and thus queued) BEFORE the judge's accept emit.
				OnEnter: []app.Effect{
					{EmitIntent: "exit_now"},
					{EmitIntent: "accept"},
				},
				On: map[string][]app.Transition{
					"exit_now": {{Target: "not_reproducible"}},
					"accept":   {{Target: "proposing"}},
				},
			},
			// The terminal @exit reached first. It has NO forward on: arm — so
			// the queued `accept` behind exit_now has nowhere to land.
			"not_reproducible": {Terminal: true, View: app.LegacyView("not reproducible")},
			"proposing":        {View: app.LegacyView("proposing")},
		},
	}

	m := mustNew(t, def)
	res, err := m.Turn(context.Background(), "start", world.New(), intent.IntentCall{Intent: "go"})

	// Without the fix this errors with `emit_intent "accept" at
	// "not_reproducible": no transition arm matched` and the settle is stranded.
	require.NoError(t, err,
		"a sibling emit queued behind one that reached a terminal @exit must be dropped, not dispatched at the terminal state")
	require.Nil(t, res.ValidationError)
	require.Equal(t, app.StatePath("not_reproducible"), res.NewState,
		"the drive must rest cleanly at the terminal @exit rather than erroring out or landing at proposing")
}
