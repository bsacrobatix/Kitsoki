package app

// Load-time validation tests for Effect.EmitIntent.
//
// Runtime dispatch is covered by internal/machine/emit_intent_test.go.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEmitIntent_TargetConflictRejected: emit_intent and target on the
// same effect are mutually exclusive — the loader rejects at load
// time so the author isn't surprised by which one "wins" at runtime.
func TestEmitIntent_TargetConflictRejected(t *testing.T) {
	const yamlSrc = `
app:
  id: emit-target-conflict
  version: 0.1.0
world: {}
intents:
  enter:  {}
  proceed: {}
root: start
states:
  start:
    on:
      enter:
        - target: middle
  middle:
    on_enter:
      - emit_intent: proceed
        target: start
    on:
      proceed:
        - target: end
  end: {}
`
	_, err := LoadBytes([]byte(yamlSrc))
	require.Error(t, err)
	require.True(t,
		strings.Contains(err.Error(), "emit_intent") &&
			strings.Contains(err.Error(), "target"),
		"error should reject emit_intent + target on same effect: %v", err)
}

// TestEmitIntent_UndeclaredIntentRejected: emit_intent referencing an
// intent the owning state doesn't handle (and no ancestor handles)
// must fail at load time. Authoring typos go through the loader.
func TestEmitIntent_UndeclaredIntentRejected(t *testing.T) {
	const yamlSrc = `
app:
  id: emit-undeclared
  version: 0.1.0
world: {}
intents:
  enter:  {}
  proceed: {}
root: start
states:
  start:
    on:
      enter:
        - target: middle
  middle:
    on_enter:
      - emit_intent: not_a_real_intent
    on:
      proceed:
        - target: end
  end: {}
`
	_, err := LoadBytes([]byte(yamlSrc))
	require.Error(t, err)
	require.True(t,
		strings.Contains(err.Error(), "emit_intent") &&
			strings.Contains(err.Error(), "not_a_real_intent"),
		"error should name the missing intent: %v", err)
}

// TestEmitIntent_AncestorIntentAllowed: emit_intent against an intent
// declared on an ancestor compound's on: arcs is allowed (the same
// rule the runtime uses for findTransitionTraced's leaf→root walk).
func TestEmitIntent_AncestorIntentAllowed(t *testing.T) {
	const yamlSrc = `
app:
  id: emit-ancestor
  version: 0.1.0
world: {}
intents:
  enter:    {}
  go_home:  {}
root: top
states:
  top:
    type: compound
    initial: inner
    on:
      go_home:
        - target: ../home
    states:
      inner:
        on_enter:
          - emit_intent: go_home
  home: {}
`
	_, err := LoadBytes([]byte(yamlSrc))
	require.NoError(t, err, "ancestor's on: arc should make emit_intent valid: %v", err)
}

// TestEmitIntent_TemplateValueSkipsStaticCheck: when emit_intent is a
// `{{ ... }}` expression the validator cannot statically resolve the
// name, so it must skip the check. Runtime dispatch still validates
// the resolved name against the state's on: arcs.
func TestEmitIntent_TemplateValueSkipsStaticCheck(t *testing.T) {
	const yamlSrc = `
app:
  id: emit-template
  version: 0.1.0
world:
  intent_name: { type: string, default: "accept" }
intents:
  enter:  {}
  accept: {}
root: start
states:
  start:
    on:
      enter:
        - target: checkpoint
  checkpoint:
    on_enter:
      - emit_intent: "{{ world.intent_name }}"
    on:
      accept:
        - target: end
  end: {}
`
	_, err := LoadBytes([]byte(yamlSrc))
	require.NoError(t, err, "template emit_intent value must skip static check: %v", err)
}

// TestEmitIntent_OnTransitionEffectValidated: emit_intent on a
// transition's effects is also validated against the owning state's
// on: arcs (not the target's).
func TestEmitIntent_OnTransitionEffectValidated(t *testing.T) {
	const yamlSrc = `
app:
  id: emit-trans-validated
  version: 0.1.0
world: {}
intents:
  enter:   {}
  follow:  {}
root: start
states:
  start:
    on:
      enter:
        - target: middle
          effects:
            - emit_intent: ghost
  middle: {}
`
	_, err := LoadBytes([]byte(yamlSrc))
	require.Error(t, err, "emit_intent ghost on start's transition must reference start's on: arcs")
	require.True(t,
		strings.Contains(err.Error(), "ghost"),
		"error should name the missing intent: %v", err)
}
