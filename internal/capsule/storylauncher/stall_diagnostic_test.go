// stall_diagnostic_test.go — covers the self-diagnosing stall error (Fix 2).
//
// When a worker drive settles without ever emitting a ci_verdict, the old path
// fell through to `capsule ci: verdict schema ""` — a bare, causeless error that
// forced an SSH-to-worker to debug. stalledVerdictError instead names the stuck
// room, the outcome, and (critically) the swallowed settle error that DriveToRest
// folded into outcome=resolved, plus the last agent verdict the story bound.
package storylauncher

import (
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/orchestrator"
)

// TestStalledVerdictErrorNamesRoomAndSettleError reconstructs the exact
// vmpool-62ceb14e84ad DriveOutcome (resolved at the NON-terminal room
// bf.reproducing, with the swallowed "no transition arm matched" settle error on
// Last.HarnessError) and asserts the produced error is self-diagnosing rather
// than the bare verdict-schema error.
func TestStalledVerdictErrorNamesRoomAndSettleError(t *testing.T) {
	out := orchestrator.DriveOutcome{
		FinalState: app.StatePath("bf.reproducing"),
		Outcome:    "resolved",
		Rounds:     3,
		Last: &orchestrator.TurnOutcome{
			HarnessError: `emit_intent "accept" at "verdict_not_reproducible": no transition arm matched (intent has no on: handler, or all guards failed)`,
		},
		WorldAfter: map[string]any{
			"bf__llm_verdict": map[string]any{
				"verdict":    "accept",
				"intent":     "accept",
				"confidence": 0.95,
			},
			"session_cost_usd": 1.48,
		},
	}

	err := stalledVerdictError("stories/dev-story/app.yaml", out)
	require.Error(t, err)
	msg := err.Error()

	// Names the stuck room and outcome.
	require.Contains(t, msg, "bf.reproducing", "must name the stuck room")
	require.Contains(t, msg, "outcome resolved", "must report the drive outcome")
	require.Contains(t, msg, "stalled without emitting ci_verdict")

	// Surfaces the swallowed settle error — the real "why nothing advanced".
	require.Contains(t, msg, "no transition arm matched",
		"must include the swallowed settle error, not hide it")
	require.Contains(t, msg, `emit_intent "accept"`,
		"must name the failing emit")

	// Surfaces the last agent verdict (a verdict WAS accepted, yet no advance).
	require.Contains(t, msg, "bf__llm_verdict")
	require.Contains(t, msg, "intent=accept")
	require.Contains(t, msg, "confidence=0.95")

	// It is NOT the bare, causeless failure the worker used to emit.
	require.NotContains(t, msg, `verdict schema ""`,
		"must not degrade to the bare verdict-schema error")
}

// TestStallDiagnosticsWithoutSettleErrorOrVerdict covers the defensive path: a
// stall with no recorded HarnessError and no bound verdict still yields a
// non-empty, room-naming diagnostic rather than an empty tail.
func TestStallDiagnosticsWithoutSettleErrorOrVerdict(t *testing.T) {
	out := orchestrator.DriveOutcome{
		FinalState: app.StatePath("bf.testing"),
		Outcome:    "resolved",
		Rounds:     5,
		WorldAfter: map[string]any{"session_cost_usd": 0.1},
	}
	diag := stallDiagnostics(out)
	require.Contains(t, diag, "5 round(s)")
	require.Contains(t, diag, "no agent verdict bound")

	err := stalledVerdictError("stories/dev-story/app.yaml", out)
	require.Contains(t, err.Error(), "bf.testing")
	require.NotContains(t, err.Error(), `verdict schema ""`)
}
