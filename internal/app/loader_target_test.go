package app

// Load-time validation tests for Effect.Target (on_complete: target:).
//
// Runtime dispatch is covered by internal/orchestrator/oncomplete_target_test.go.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOnCompleteTarget_MissingTargetLoadError: a target that doesn't resolve
// to a declared state must be rejected at load time.
func TestOnCompleteTarget_MissingTargetLoadError(t *testing.T) {
	const yamlSrc = `
app:
  id: missing-target
  version: 0.1.0
hosts:
  - host.test.echo
world:
  last_job_id: { type: string, default: "" }
root: lobby
states:
  lobby:
    on_enter:
      - invoke: host.test.echo
        background: true
        bind: { last_job_id: job_id }
        on_complete:
          - target: nope_does_not_exist
`
	_, err := LoadBytes([]byte(yamlSrc))
	require.Error(t, err)
	require.True(t,
		strings.Contains(err.Error(), "target") &&
			strings.Contains(err.Error(), "does not exist"),
		"error should mention target validation and existence: %v", err)
}

// TestOnCompleteTarget_MixedWithSetLoadError: combining Target with another
// mutation (e.g. Set) on the same effect must be rejected at load time so
// authors are forced to split mutation and transition into separate effects.
func TestOnCompleteTarget_MixedWithSetLoadError(t *testing.T) {
	const yamlSrc = `
app:
  id: mixed-target
  version: 0.1.0
hosts:
  - host.test.echo
world:
  x:            { type: string, default: "" }
  last_job_id:  { type: string, default: "" }
root: lobby
states:
  lobby:
    on_enter:
      - invoke: host.test.echo
        background: true
        bind: { last_job_id: job_id }
        on_complete:
          - set: { x: "boom" }
            target: end
  end: {}
`
	_, err := LoadBytes([]byte(yamlSrc))
	require.Error(t, err)
	require.True(t,
		strings.Contains(err.Error(), "target") &&
			strings.Contains(err.Error(), "set"),
		"error should mention target validation and conflicting set field: %v", err)
}

// TestOnCompleteTarget_MixedWithInvokeLoadError: target + invoke on the
// same effect is also rejected (the invoke needs to land in its own
// effect, so the chain reads "invoke, observe binds, then transition").
func TestOnCompleteTarget_MixedWithInvokeLoadError(t *testing.T) {
	const yamlSrc = `
app:
  id: mixed-target-invoke
  version: 0.1.0
hosts:
  - host.test.echo
  - host.test.noop
world:
  last_job_id:  { type: string, default: "" }
root: lobby
states:
  lobby:
    on_enter:
      - invoke: host.test.echo
        background: true
        bind: { last_job_id: job_id }
        on_complete:
          - invoke: host.test.noop
            target: end
  end: {}
`
	_, err := LoadBytes([]byte(yamlSrc))
	require.Error(t, err)
	require.True(t,
		strings.Contains(err.Error(), "target") &&
			strings.Contains(err.Error(), "invoke"),
		"error should mention target validation and conflicting invoke field: %v", err)
}

// TestOnCompleteTarget_OutsideOnComplete: declaring target: on a
// non-on_complete effect (e.g. on_enter, transition.effects) is rejected so
// authors don't accidentally believe a Target on a regular effect will
// fire a synthetic transition.
func TestOnCompleteTarget_OutsideOnComplete(t *testing.T) {
	const yamlSrc = `
app:
  id: bad-target-site
  version: 0.1.0
root: foo
states:
  foo:
    on_enter:
      - target: bar
  bar: {}
`
	_, err := LoadBytes([]byte(yamlSrc))
	require.Error(t, err)
	require.True(t,
		strings.Contains(err.Error(), "target") &&
			strings.Contains(err.Error(), "on_complete"),
		"error should mention target validation and the on_complete-only restriction: %v", err)
}

// TestOnCompleteTarget_HappyLoad: a well-formed on_complete: target:
// passes validation cleanly.  Sanity check that the new validation does
// not accidentally over-reject valid authoring patterns.
func TestOnCompleteTarget_HappyLoad(t *testing.T) {
	const yamlSrc = `
app:
  id: happy-target
  version: 0.1.0
hosts:
  - host.test.echo
world:
  x:            { type: string, default: "" }
  last_job_id:  { type: string, default: "" }
root: lobby
states:
  lobby:
    on_enter:
      - invoke: host.test.echo
        background: true
        bind: { last_job_id: job_id }
        on_complete:
          - set: { x: "applied" }
          - target: done
  done: {}
`
	def, err := LoadBytes([]byte(yamlSrc))
	require.NoError(t, err, "valid on_complete target should load without error")
	require.NotNil(t, def)
}
