package app

// Unit tests for the nested effects: list on Effect (Effect.Effects).
//
// The Effect struct has an Effects []Effect field (yaml:"effects,omitempty")
// that authors use inside on_complete: target: blocks to attach world
// mutations that fire as the synthetic transition lands.  This file verifies
// that the YAML loader parses the nested list correctly and that the
// validator accepts valid configurations.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNestedEffects_ParsesFromYAML verifies that an on_complete: effect with
// a nested effects: list is loaded without error and the sub-effects are
// correctly populated.
func TestNestedEffects_ParsesFromYAML(t *testing.T) {
	const yamlSrc = `
app:
  id: nested-effects-test
  version: 0.1.0
hosts:
  - host.test.echo
world:
  status:  { type: string, default: "" }
  counter: { type: integer, default: 0 }
  job_id:  { type: string, default: "" }
root: lobby
states:
  lobby:
    on_enter:
      - invoke: host.test.echo
        background: true
        bind: { job_id: job_id }
        on_complete:
          - effects:
              - set: { status: "done" }
              - increment: { counter: 1 }
          - target: done_state
  done_state:
    terminal: true
`
	def, err := LoadBytes([]byte(yamlSrc))
	require.NoError(t, err)

	lobby := def.States["lobby"]
	require.NotNil(t, lobby)
	require.Len(t, lobby.OnEnter, 1)

	onComplete := lobby.OnEnter[0].OnComplete
	require.Len(t, onComplete, 2, "expected 2 on_complete effects")

	// First effect: nested effects: list.
	firstEff := onComplete[0]
	assert.Empty(t, firstEff.Target, "first effect should not have a target")
	require.Len(t, firstEff.Effects, 2, "expected 2 nested sub-effects")

	setEff := firstEff.Effects[0]
	assert.Equal(t, map[string]any{"status": "done"}, setEff.Set)

	incEff := firstEff.Effects[1]
	assert.Equal(t, map[string]int{"counter": 1}, incEff.Increment)

	// Second effect: the target transition.
	secondEff := onComplete[1]
	assert.Equal(t, "done_state", secondEff.Target)
	assert.Empty(t, secondEff.Effects, "target effect should have no nested sub-effects")
}

// TestNestedEffects_TransitionEffectsParseFromYAML verifies that a
// Transition.Effects list (the sibling field on Transition, not Effect) also
// deserialises correctly.  Included here for completeness alongside the
// Effect.Effects coverage above.
func TestNestedEffects_TransitionEffectsParseFromYAML(t *testing.T) {
	const yamlSrc = `
app:
  id: transition-effects-test
  version: 0.1.0
world:
  x: { type: string, default: "" }
  y: { type: string, default: "" }
intents:
  go:
    title: Go forward
root: start
states:
  start:
    on:
      go:
        - target: end
          effects:
            - set: { x: "hello" }
            - set: { y: "world" }
  end:
    terminal: true
`
	def, err := LoadBytes([]byte(yamlSrc))
	require.NoError(t, err)

	start := def.States["start"]
	require.NotNil(t, start)
	arcs := start.On["go"]
	require.Len(t, arcs, 1)

	effects := arcs[0].Effects
	require.Len(t, effects, 2, "expected 2 transition effects")
	assert.Equal(t, map[string]any{"x": "hello"}, effects[0].Set)
	assert.Equal(t, map[string]any{"y": "world"}, effects[1].Set)
}
