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

// newAckErrorTestOrchestrator builds an Orchestrator over
// testdata/hosterror_acked/app.yaml with host.fail always failing and
// host.ok always succeeding (binding "value": "reached-ok"), for the
// ack_error: runtime tests below.
func newAckErrorTestOrchestrator(t *testing.T) *orchestrator.Orchestrator {
	t.Helper()
	def, err := app.Load("testdata/hosterror_acked/app.yaml")
	require.NoError(t, err)

	m, err := machine.New(def)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	reg := host.NewRegistry()
	reg.Register("host.fail", func(ctx context.Context, args map[string]any) (host.Result, error) {
		return host.Result{Error: "deliberate acked failure"}, nil
	})
	reg.Register("host.ok", func(ctx context.Context, args map[string]any) (host.Result, error) {
		return host.Result{Data: map[string]any{"value": "reached-ok"}}, nil
	})

	return orchestrator.New(def, m, s, noopHarness{}, orchestrator.WithHostRegistry(reg))
}

// TestOrchestrator_Turn_AckedHostErrorAppendsHandledEntryAndContinues covers
// dispatchHostCalls (the Turn/SubmitDirect path): a failing invoke: that
// declares ack_error: must (a) append a world.error_log entry with
// handled: true and handled_reason set to the ack_error: string, (b) NOT
// redirect — the chain continues to the SECOND invoke in the same
// on_enter block, whose bind lands in world, and (c) NOT show the
// "⚠ Action failed:" banner in the rendered view. Durability (the entry
// itself) is required regardless; only visibility is suppressed.
func TestOrchestrator_Turn_AckedHostErrorAppendsHandledEntryAndContinues(t *testing.T) {
	orch := newAckErrorTestOrchestrator(t)
	ctx := context.Background()

	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)

	out, err := orch.SubmitDirect(ctx, sid, "ask", map[string]any{})
	require.NoError(t, err)
	require.Equal(t, app.StatePath("probe"), out.NewState)

	// No banner: a deliberate, acknowledged degrade must not read to the
	// user as "⚠ Action failed:".
	require.NotContains(t, out.View, "⚠ Action failed:", "an acked failure must not banner")

	// The chain continued past the acked failure to the second invoke.
	require.Contains(t, out.View, "reached-ok")

	journey, err := orch.LoadJourney(sid)
	require.NoError(t, err)

	logAny, ok := journey.World.Vars[app.ErrorLogWorldKey]
	require.True(t, ok, "world.error_log must still be set — ack_error: changes visibility, not durability")
	log, ok := logAny.([]any)
	require.True(t, ok)
	require.Len(t, log, 1)

	entry, ok := log[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "probe", entry["state"])
	require.Equal(t, "host_domain", entry["class"])
	require.Equal(t, true, entry["handled"], "an acked failure's entry must be marked handled: true")
	require.Equal(t, "non-critical backstop — safe to ignore in this fixture", entry["handled_reason"])
	require.Equal(t, "deliberate acked failure", entry["message"])
	require.Equal(t, "host.fail", entry["namespace"])
	require.NotEmpty(t, entry["ts"])

	require.Equal(t, "reached-ok", journey.World.Vars["reached"])
}

// TestOrchestrator_OneShot_AckedHostErrorAppendsHandledEntryAndDoesNotBanner
// is the dispatchHostCallsDetailed (OneShot RPC) sibling of the Turn test
// above — parity across both dispatch paths per requirement #4.
func TestOrchestrator_OneShot_AckedHostErrorAppendsHandledEntryAndDoesNotBanner(t *testing.T) {
	orch := newAckErrorTestOrchestrator(t)

	out, err := orch.OneShot(context.Background(), orchestrator.OneShotInput{
		State:  app.StatePath("idle"),
		World:  map[string]any{"marker": ""},
		Intent: "ask",
		Slots:  map[string]any{},
	})
	require.NoError(t, err)
	require.Equal(t, orchestrator.ModeTransitioned, out.Mode)
	require.Equal(t, app.StatePath("probe"), out.NextState)

	require.NotContains(t, out.View, "⚠ Action failed:", "an acked failure must not banner on the OneShot path either")
	require.Contains(t, out.View, "reached-ok", "the chain must continue past the acked call to the second invoke")

	logAny, ok := out.WorldAfter[app.ErrorLogWorldKey]
	require.True(t, ok, "world.error_log must be set after an acked OneShot host failure")
	log, ok := logAny.([]any)
	require.True(t, ok)
	require.Len(t, log, 1)

	entry, ok := log[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, entry["handled"])
	require.Equal(t, "non-critical backstop — safe to ignore in this fixture", entry["handled_reason"])
	require.Equal(t, "host_domain", entry["class"])
}

// TestOrchestrator_Turn_NonAckedHostErrorStillBanners is the control case:
// a failing invoke: with neither on_error: nor ack_error: still shows the
// never-silent banner — asserting that suppressing the banner is unique to
// ack_error:, not a general regression.
func TestOrchestrator_Turn_NonAckedHostErrorStillBanners(t *testing.T) {
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
	ctx := context.Background()

	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)

	out, err := orch.SubmitDirect(ctx, sid, "ask", map[string]any{})
	require.NoError(t, err)
	require.Equal(t, app.StatePath("probe"), out.NewState)

	require.Contains(t, out.View, "⚠ Action failed:", "a non-acked, non-redirected failure must still banner")
	require.Contains(t, out.View, "deliberate unhandled failure")

	journey, err := orch.LoadJourney(sid)
	require.NoError(t, err)
	logAny := journey.World.Vars[app.ErrorLogWorldKey]
	log, ok := logAny.([]any)
	require.True(t, ok)
	require.Len(t, log, 1)
	entry, ok := log[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, false, entry["handled"], "a non-acked failure's entry must stay handled: false")
	require.Nil(t, entry["handled_reason"], "handled_reason is only ever populated on a handled entry")
}
