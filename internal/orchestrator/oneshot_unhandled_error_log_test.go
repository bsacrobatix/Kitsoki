package orchestrator_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/host"
	"kitsoki/internal/machine"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/store"
)

// TestOrchestrator_OneShot_UnhandledHostErrorAppendsErrorLogAndBanners closes
// the parity gap the troubleshooting-agent-and-error-integrity design doc's
// Part 1, Leak 1 left open on the OneShot RPC path
// (dispatchHostCallsDetailed): a host call that fails with NO on_error: arc
// declared must still append a world.error_log entry, set world.error_origin,
// log the WARN-level trace.EvHostErrorUnhandled event, and surface the
// never-silent banner in View — exactly like dispatchHostCalls already does
// for the Turn/ContinueTurn path (error_log.go,
// TestOrchestrator_MachineFatalErrorAppendsErrorLog's sibling for the
// host_infra/host_domain classes instead of "machine").
func TestOrchestrator_OneShot_UnhandledHostErrorAppendsErrorLogAndBanners(t *testing.T) {
	def, err := app.Load("testdata/hosterror_unhandled/app.yaml")
	require.NoError(t, err)

	m, err := machine.New(def)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	reg := host.NewRegistry()
	reg.Register("host.fail", func(ctx context.Context, args map[string]any) (host.Result, error) {
		return host.Result{Error: "deliberate unhandled failure"}, nil
	})

	orch := orchestrator.New(def, m, s, noopHarness{}, orchestrator.WithHostRegistry(reg))

	out, err := orch.OneShot(context.Background(), orchestrator.OneShotInput{
		State:  app.StatePath("idle"),
		World:  map[string]any{"marker": ""},
		Intent: "ask",
		Slots:  map[string]any{},
	})
	require.NoError(t, err)
	require.Equal(t, orchestrator.ModeTransitioned, out.Mode)
	// No on_error: arc on the invoke, so the resolved state stays `probe`
	// (Leak 1 shape — dispatch does NOT redirect on an unhandled failure).
	require.Equal(t, app.StatePath("probe"), out.NextState)

	// The never-silent banner must surface in View even though nothing
	// redirected — this is the OneShot-path sibling of
	// TestOrchestrator_OneShot_AppliesErrorBannerOnRedirect.
	require.Contains(t, out.View, "⚠ Action failed:", "OneShot must banner an unhandled host failure even with no on_error: redirect")
	require.Contains(t, out.View, "deliberate unhandled failure")

	// world.error_log / world.error_origin must be visible in WorldAfter —
	// OneShot has no session/store to persist into, so WorldAfter is the
	// only place a caller (kitsoki turn, an MCP client) can observe them.
	require.Equal(t, "probe", out.WorldAfter[string(app.ErrorOriginWorldKey)],
		"error_origin must record the state the unhandled host failure occurred in")

	logAny, ok := out.WorldAfter[app.ErrorLogWorldKey]
	require.True(t, ok, "world.error_log must be set after an unhandled OneShot host failure")
	log, ok := logAny.([]any)
	require.True(t, ok, "world.error_log must be a list, got %T", logAny)
	require.Len(t, log, 1)

	entry, ok := log[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "probe", entry["state"])
	require.Equal(t, "host_domain", entry["class"])
	require.Equal(t, false, entry["handled"])
	require.Equal(t, "deliberate unhandled failure", entry["message"])
	require.Equal(t, "host.fail", entry["namespace"])
	require.NotEmpty(t, entry["ts"])

	// host_error's single-slot convenience view is also populated for
	// consistency with the Turn path (dispatchHostCalls).
	herrAny, ok := out.WorldAfter["host_error"]
	require.True(t, ok, "world.host_error should be set on this path too, for parity with dispatchHostCalls")
	herr, ok := herrAny.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "host.fail", herr["namespace"])
	require.Equal(t, "deliberate unhandled failure", herr["message"])
}
