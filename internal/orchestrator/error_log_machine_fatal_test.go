package orchestrator_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/machine"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/store"
)

// TestOrchestrator_MachineFatalErrorAppendsErrorLog proves Task B's third
// case (.context/troubleshooting-agent-and-error-integrity.md Part 1, Leak
// 5): a machine-fatal error — here the same eval-failing `set:` expression
// TestOrchestrator_MachineTurnErrorIsTraced drives (`string + int`) — never
// reaches a host call, so on_error: can't catch it. journalTurnError must
// still append a world.error_log entry with class: "machine" (and set
// world.error_origin to the state the failure occurred in) so the failure
// is durably visible even though the session "does not move" (no
// TransitionApplied — the turn aborted).
func TestOrchestrator_MachineFatalErrorAppendsErrorLog(t *testing.T) {
	def, err := app.Load("testdata/turnerror/app.yaml")
	require.NoError(t, err)

	m, err := machine.New(def)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	orch := orchestrator.New(def, m, s, noopHarness{})

	ctx := context.Background()
	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)

	_, turnErr := orch.SubmitDirect(ctx, sid, "boom", map[string]any{})
	require.Error(t, turnErr, "machine.Turn should error on the eval-failing effect")

	history, err := s.LoadHistory(sid)
	require.NoError(t, err)

	js, err := store.BuildJourney(def, orch.InitialState(), orch.InitialWorld(), history)
	require.NoError(t, err)

	require.Equal(t, "idle", js.World.Vars["error_origin"],
		"error_origin must record the state the machine-fatal failure occurred in")

	logAny, ok := js.World.Vars["error_log"]
	require.True(t, ok, "world.error_log must be set after a machine-fatal Turn error")
	log, ok := logAny.([]any)
	require.True(t, ok, "world.error_log must be a list, got %T", logAny)
	require.Len(t, log, 1, "exactly one error_log entry for the one machine-fatal turn")

	entry, ok := log[0].(map[string]any)
	require.True(t, ok, "error_log entry must be a map, got %T", log[0])

	require.Equal(t, float64(1), entry["seq"], "first entry gets seq 1")
	require.Equal(t, "idle", entry["state"])
	require.Equal(t, "machine", entry["class"])
	require.Equal(t, false, entry["handled"])
	require.Contains(t, entry["message"], "invalid operation",
		"the entry's message must carry the underlying eval error")
	require.NotContains(t, entry, "namespace",
		"a machine-fatal error never reaches a host call, so namespace is omitted")
	require.NotEmpty(t, entry["ts"], "ts must be populated (from the orchestrator's injected clock)")

	// A second machine-fatal turn on the same session must APPEND, not
	// overwrite — proving error_log is a history, not a single slot like
	// last_error/host_error.
	_, turnErr2 := orch.SubmitDirect(ctx, sid, "boom", map[string]any{})
	require.Error(t, turnErr2)

	history2, err := s.LoadHistory(sid)
	require.NoError(t, err)
	js2, err := store.BuildJourney(def, orch.InitialState(), orch.InitialWorld(), history2)
	require.NoError(t, err)

	log2, ok := js2.World.Vars["error_log"].([]any)
	require.True(t, ok)
	require.Len(t, log2, 2, "a second machine-fatal turn appends a second entry")
	entry2, ok := log2[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(2), entry2["seq"], "seq is monotonic across entries")
}
