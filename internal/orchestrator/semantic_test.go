// Integration tests for the semantic-routing orchestrator wiring
// (semantic-routing proposal §1, Phase 2). The unit tests for the
// matcher itself live in internal/semroute/*_test.go; these tests
// exercise the orchestrator-side glue:
//
//   - With app.routing.enabled = true, an input that maps to a
//     declared synonym resolves WITHOUT calling the harness.
//   - With app.routing.enabled = false, the same input falls through
//     to the harness.
//   - A miss (no synonym matches) falls through to the harness.
//   - A tie (two intents matched the same synonym) surfaces an
//     AMBIGUOUS_INTENT outcome and the harness is NOT called.
//
// The counting harness lets us assert "harness was not called" by
// reading a counter after Turn returns.
package orchestrator_test

import (
	"context"
	"sync/atomic"
	"testing"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/harness"
	"kitsoki/internal/machine"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/store"
)

// countingHarness records how many times RunTurn was called and
// returns a static intent call from the recorded fallback. Used by
// the semantic-routing tests to assert "the LLM was not called."
type countingHarness struct {
	calls atomic.Int64
	fall  staticHarness
}

func (h *countingHarness) RunTurn(ctx context.Context, in harness.TurnInput) (mcp.CallToolParams, error) {
	h.calls.Add(1)
	return h.fall.RunTurn(ctx, in)
}

func (h *countingHarness) Close() error { return h.fall.Close() }

// newSemanticTestApp builds an in-memory AppDef with three intents
// (north / south / east) and a single state that allows all three.
// Each intent declares one synonym that the matcher should resolve
// without the LLM.
func newSemanticTestApp(t *testing.T, routingEnabled bool) (*orchestrator.Orchestrator, *countingHarness, app.SessionID) {
	t.Helper()
	const appYAML = `
app:
  id: semroute-test
  version: 0.1.0

world: {}

routing:
  enabled: %t

intents:
  go_north:
    title: "Go north"
    examples: ["go north"]
    synonyms: ["head north"]
  go_south:
    title: "Go south"
    examples: ["go south"]
    synonyms: ["head south"]
  go_east:
    title: "Go east"
    examples: ["go east"]
    synonyms: ["wander east"]

root: start

states:
  start:
    view: "compass rose"
    on:
      go_north:
        - target: ended
      go_south:
        - target: ended
      go_east:
        - target: ended

  ended:
    terminal: true
    view: "done"
`
	body := []byte(rendered(appYAML, routingEnabled))
	def, err := app.LoadBytes(body)
	require.NoError(t, err)

	m, err := machine.New(def)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	// Fallback harness routes to go_north on any LLM call so that
	// when the test EXPECTS the harness to be hit we have a sane
	// outcome to assert on. Tests that want a different fallback
	// can rewrite the .fall field directly.
	h := &countingHarness{fall: staticHarness{intentName: "go_north"}}

	orch := orchestrator.New(def, m, s, h)
	ctx := context.Background()
	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)
	return orch, h, sid
}

// rendered is a tiny helper that fills the `%t` in the test YAML
// without dragging text/template into the test binary.
func rendered(tmpl string, b bool) string {
	out := make([]byte, 0, len(tmpl))
	v := "true"
	if !b {
		v = "false"
	}
	for i := 0; i < len(tmpl); i++ {
		if i+1 < len(tmpl) && tmpl[i] == '%' && tmpl[i+1] == 't' {
			out = append(out, v...)
			i++
			continue
		}
		out = append(out, tmpl[i])
	}
	return string(out)
}

// TestSemantic_SynonymResolvesWithoutHarness — the canonical happy
// path. With routing enabled, "head north" routes to go_north and
// the harness is never called.
func TestSemantic_SynonymResolvesWithoutHarness(t *testing.T) {
	t.Parallel()
	orch, h, sid := newSemanticTestApp(t, true)
	ctx := context.Background()

	out, err := orch.Turn(ctx, sid, "head north")
	require.NoError(t, err)
	require.Equal(t, orchestrator.ModeCompleted, out.Mode,
		"want ModeCompleted (go_north transitions to terminal ended), got %v", out.Mode)
	require.Equal(t, app.StatePath("ended"), out.NewState)
	require.EqualValues(t, 0, h.calls.Load(),
		"harness must NOT be called when semantic routing resolves the turn")
}

// TestSemantic_DisabledFallsThroughToHarness — same input, but with
// routing.enabled=false in the AppDef, the matcher is skipped and
// the harness fires.
func TestSemantic_DisabledFallsThroughToHarness(t *testing.T) {
	t.Parallel()
	orch, h, sid := newSemanticTestApp(t, false)
	ctx := context.Background()

	out, err := orch.Turn(ctx, sid, "head north")
	require.NoError(t, err)
	// Fallback harness routes everything to go_north, which is a
	// valid transition, so the outcome is still Completed — but the
	// load-bearing assertion is the call count.
	require.Equal(t, orchestrator.ModeCompleted, out.Mode)
	require.EqualValues(t, 1, h.calls.Load(),
		"harness MUST be called when routing.enabled=false")
}

// TestSemantic_MissFallsThroughToHarness — routing is enabled but
// the input shares no stems with any declared synonym; the harness
// MUST fire.
func TestSemantic_MissFallsThroughToHarness(t *testing.T) {
	t.Parallel()
	orch, h, sid := newSemanticTestApp(t, true)
	ctx := context.Background()

	out, err := orch.Turn(ctx, sid, "abracadabra please thank you")
	require.NoError(t, err)
	require.Equal(t, orchestrator.ModeCompleted, out.Mode)
	require.EqualValues(t, 1, h.calls.Load(),
		"semantic miss must fall through to the harness")
}

// TestSemantic_TieSurfacesAmbiguousIntent — two intents share a
// synonym; the orchestrator returns an AMBIGUOUS_INTENT outcome
// without calling the harness.
func TestSemantic_TieSurfacesAmbiguousIntent(t *testing.T) {
	t.Parallel()

	const appYAML = `
app:
  id: semroute-tie
  version: 0.1.0

world: {}

intents:
  leave_store:
    title: "Leave store"
    examples: ["leave the store"]
    synonyms: ["leave"]
  cancel_purchase:
    title: "Cancel purchase"
    examples: ["cancel"]
    synonyms: ["leave"]

root: start

states:
  start:
    view: "store"
    on:
      leave_store:
        - target: ended
      cancel_purchase:
        - target: ended

  ended:
    terminal: true
    view: "done"
`
	def, err := app.LoadBytes([]byte(appYAML))
	require.NoError(t, err)

	m, err := machine.New(def)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	h := &countingHarness{fall: staticHarness{intentName: "leave_store"}}
	orch := orchestrator.New(def, m, s, h)
	ctx := context.Background()
	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)

	out, err := orch.Turn(ctx, sid, "leave")
	require.NoError(t, err)
	require.Equal(t, orchestrator.ModeRejected, out.Mode,
		"tie verdict must surface as Rejected (orchestrator-side AMBIGUOUS_INTENT)")
	require.Equal(t, "AMBIGUOUS_INTENT", string(out.ErrorCode))
	require.EqualValues(t, 0, h.calls.Load(),
		"harness must NOT be called on a tie verdict — disambiguation comes first")
}

// TestSemantic_MatcherAccessor exposes the lazy-compiled *Matcher
// to outside callers (Matcher() / MatcherError()).
func TestSemantic_MatcherAccessor(t *testing.T) {
	t.Parallel()
	orch, _, _ := newSemanticTestApp(t, true)
	m := orch.Matcher()
	require.NotNil(t, m, "Matcher() must return non-nil when routing is enabled and synonyms exist")
	require.NoError(t, orch.MatcherError())
}

// TestSemantic_MatcherAccessorDisabled — routing.enabled=false
// yields a nil Matcher.
func TestSemantic_MatcherAccessorDisabled(t *testing.T) {
	t.Parallel()
	orch, _, _ := newSemanticTestApp(t, false)
	require.Nil(t, orch.Matcher(), "routing.enabled=false must yield nil Matcher")
}
