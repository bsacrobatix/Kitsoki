package app_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"hally/internal/app"
)

// TestPhases_LoadAndExpand verifies that phase templates expand into the
// expected state names and transition shape.
func TestPhases_LoadAndExpand(t *testing.T) {
	def, err := app.Load(filepath.Join("testdata", "phases", "three-phase.yaml"))
	require.NoError(t, err)

	wantStates := []string{
		"phase_a_executing", "phase_a_awaiting_reply", "phase_a_error",
		"phase_b_executing", "phase_b_awaiting_reply", "phase_b_error",
		"phase_c_executing", "phase_c_awaiting_reply", "phase_c_error",
		"terminated",
	}
	for _, name := range wantStates {
		assert.Contains(t, def.States, name, "expanded state %q must exist", name)
	}
	assert.Len(t, def.States, len(wantStates))
}

// TestPhases_TplSubstitution verifies that {{ tpl.X }} is substituted into
// state body strings.
func TestPhases_TplSubstitution(t *testing.T) {
	def, err := app.Load(filepath.Join("testdata", "phases", "three-phase.yaml"))
	require.NoError(t, err)

	exec := def.States["phase_b_executing"]
	require.NotNil(t, exec)
	assert.Equal(t, "Executing Phase B", exec.Description)
	assert.Equal(t, "Phase Phase B running", exec.View)
}

// TestPhases_NextRewrite verifies {{ phase.next.continue }} rewrites to
// <phase>_executing.
func TestPhases_NextRewrite(t *testing.T) {
	def, err := app.Load(filepath.Join("testdata", "phases", "three-phase.yaml"))
	require.NoError(t, err)

	// phase_a → phase_b.
	doneA := def.States["phase_a_executing"].On["done"]
	require.NotEmpty(t, doneA)
	// First entry has the checkpoint when-guard ("tpl.checkpoint == true"),
	// which is "false" for phase_a — but the loader still emits it. The
	// default fall-through must point at phase_b_executing.
	var fellThrough bool
	for _, tr := range doneA {
		if tr.Default {
			assert.Equal(t, "phase_b_executing", tr.Target)
			fellThrough = true
		}
	}
	require.True(t, fellThrough, "phase_a must have a default fall-through to phase_b_executing")

	// phase_c.continue → terminated (no rewriting because it's a literal).
	doneC := def.States["phase_c_executing"].On["done"]
	for _, tr := range doneC {
		if tr.Default {
			assert.Equal(t, "terminated", tr.Target)
		}
	}
}

// TestPhases_CycleBudgetsSynthesis verifies that:
//   - the arc transitions get an Increment effect
//   - the arc's `when:` is tightened with the cycle bound
//   - a default fall-through to <phase>_error is appended
func TestPhases_CycleBudgetsSynthesis(t *testing.T) {
	def, err := app.Load(filepath.Join("testdata", "phases", "three-phase.yaml"))
	require.NoError(t, err)

	exec := def.States["phase_c_executing"]
	require.NotNil(t, exec)

	arc := exec.On["on_failure"]
	require.NotEmpty(t, arc, "phase_c.on_failure must be synthesized from cycle_budgets")

	// First entry: the synthesized increment + guard.
	first := arc[0]
	require.False(t, first.Default)
	assert.Contains(t, first.When, "cycle__phase_c__on_failure")
	assert.Contains(t, first.When, " < 2")
	require.NotEmpty(t, first.Effects)
	assert.Equal(t, 1, first.Effects[0].Increment["cycle__phase_c__on_failure"])

	// Last entry: the fall-through to phase_c_error.
	last := arc[len(arc)-1]
	assert.True(t, last.Default, "trailing transition must be the default fall-through")
	assert.Equal(t, "phase_c_error", last.Target)
	assert.NotEmpty(t, last.GuardHint)
}

// TestPhases_CheckpointIntentsMerged verifies that checkpoint_intents
// are merged into every {id}_awaiting_reply state's intents.
func TestPhases_CheckpointIntentsMerged(t *testing.T) {
	def, err := app.Load(filepath.Join("testdata", "phases", "three-phase.yaml"))
	require.NoError(t, err)

	for _, name := range []string{"phase_a_awaiting_reply", "phase_b_awaiting_reply", "phase_c_awaiting_reply"} {
		s := def.States[name]
		require.NotNil(t, s, name)
		require.Contains(t, s.Intents, "continue", name)
		require.Contains(t, s.Intents, "refine", name)
	}
	// _executing states must NOT receive checkpoint_intents.
	exec := def.States["phase_a_executing"]
	require.NotNil(t, exec)
	_, hasContinue := exec.Intents["continue"]
	require.False(t, hasContinue, "_executing must not receive checkpoint_intents")
}

// TestPhases_CycleBudgetSynthesisMissingArcCreatesErrorRoute verifies that
// declaring cycle_budgets for an arc not present in the template synthesizes
// a fall-through-to-error transition (rather than silently dropping the
// budget).
func TestPhases_CycleBudgetSynthesisMissingArcCreatesErrorRoute(t *testing.T) {
	def, err := app.Load(filepath.Join("testdata", "phases", "three-phase.yaml"))
	require.NoError(t, err)

	// `on_failure` is not declared in the template's `on:` block — only
	// `done` is. The cycle_budgets entry should synthesize a transition.
	exec := def.States["phase_c_executing"]
	arc := exec.On["on_failure"]
	require.NotEmpty(t, arc)
}
