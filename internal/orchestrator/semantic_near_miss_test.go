// Tests for the near_miss confidence band added to TrySemantic's
// confidence-band switch (see docs/proposals/never-silent-runtime.md,
// Task 1.2/1.4). A near-miss is a verdict that scored above the reject
// floor (0 — a genuine miss never reaches the switch, see semantic.go)
// but below the accept floor (SemanticMidBar). It must never resolve to
// the nearest authored intent by itself; until S1's workbench exists it
// falls back to today's off-ramp / no-match handling (the next routing
// tier / harness), and the decision is recorded on the trace as
// turn.near_miss with the confidence + threshold fields.
//
// The test manufactures a deterministic near-miss WITHOUT any new
// tunable: it raises app.routing.semantic_mid_bar above
// ConfidenceWholeSynonym (0.90), so an ordinary bare-synonym hit that
// would normally clear the mid-bar now falls strictly between 0 and
// the (raised) mid-bar — exactly the near_miss band — using only the
// existing SemanticHighBar/SemanticMidBar knobs authors already have.
package orchestrator_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/machine"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/store"
	"kitsoki/internal/trace"
)

// TestSemantic_NearMissFallsThroughWithoutNearestIntent asserts that an
// input scored in the near-miss band (0.90 confidence against a
// deliberately-raised 0.95 mid-bar) never auto-resolves to the intent
// its synonym matched (go_north). Instead it falls through to the next
// routing tier — here the mocked harness — which is configured to
// resolve a DIFFERENT intent (go_east). If TrySemantic ever shortcut
// the near_miss band to "nearest authored intent," this test would
// observe go_north instead of go_east and fail.
func TestSemantic_NearMissFallsThroughWithoutNearestIntent(t *testing.T) {
	t.Parallel()
	const appYAML = `
app:
  id: semroute-near-miss
  version: 0.1.0

world: {}

routing:
  enabled: true
  semantic_high_bar: 0.99
  semantic_mid_bar: 0.95

intents:
  go_north:
    title: "Go north"
    synonyms: ["head north"]
  go_east:
    title: "Go east"
    synonyms: ["wander east"]

root: start

states:
  start:
    view: "compass rose"
    on:
      go_north:
        - target: north_result
      go_east:
        - target: east_result

  north_result:
    terminal: true
    view: "you went north"

  east_result:
    terminal: true
    view: "you went east"
`
	def, err := app.LoadBytes([]byte(appYAML))
	require.NoError(t, err)

	m, err := machine.New(def)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	handler := newCapturingHandler(slog.LevelDebug)
	logger := slog.New(handler)

	// Fallback harness resolves to go_east — a DIFFERENT intent than the
	// one "head north" matched via synonym — so a pass proves the
	// near-miss band did not shortcut to the nearest authored intent.
	h := &countingHarness{fall: staticHarness{intentName: "go_east"}}

	orch := orchestrator.New(def, m, s, h, orchestrator.WithLogger(logger))
	ctx := context.Background()
	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)

	out, err := orch.Turn(ctx, sid, "head north")
	require.NoError(t, err)

	require.EqualValues(t, 1, h.calls.Load(),
		"near-miss band must fall through to the next routing tier (today's off-ramp/no-match handling), not resolve directly")
	require.Equal(t, app.StatePath("east_result"), out.NewState,
		"near-miss must never resolve to the nearest authored intent (go_north); the actual outcome is whatever the next tier decided")

	// The trace must show the near_miss verdict with the same
	// confidence-vs-threshold shape the other semantic-tier events
	// record (Task 1.4).
	var found bool
	for _, r := range handler.allRecords() {
		if r.Message != trace.EvTurnNearMiss {
			continue
		}
		found = true
		r.Attrs(func(a slog.Attr) bool {
			switch a.Key {
			case "confidence":
				require.InDelta(t, 0.90, a.Value.Float64(), 0.0001, "near_miss must record the verdict's confidence")
			case "threshold":
				require.InDelta(t, 0.95, a.Value.Float64(), 0.0001, "near_miss must record the mid-bar threshold it missed")
			}
			return true
		})
	}
	require.True(t, found, "expected a turn.near_miss trace event")
}
