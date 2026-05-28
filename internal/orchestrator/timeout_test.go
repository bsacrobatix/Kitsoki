package orchestrator_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/clock"
	"kitsoki/internal/harness"
	"kitsoki/internal/inbox"
	"kitsoki/internal/machine"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/store"
)

// timeoutNoopHarness satisfies harness.Harness for Timeout: tests that drive
// state changes through RunIntent or Teleport — the harness is never invoked.
type timeoutNoopHarness struct{}

func (h *timeoutNoopHarness) RunTurn(ctx context.Context, in harness.TurnInput) (mcp.CallToolParams, error) {
	return mcp.CallToolParams{}, fmt.Errorf("timeoutNoopHarness: RunTurn called")
}
func (h *timeoutNoopHarness) Close() error { return nil }

// buildTimeoutRig builds an orchestrator backed by the testdata/timeout app
// driven by a fake clock so Timeout: firings are deterministic.
func buildTimeoutRig(t *testing.T) (*orchestrator.Orchestrator, *clock.Fake, store.Store) {
	t.Helper()
	def, err := app.Load("testdata/timeout/app.yaml")
	require.NoError(t, err)
	m, err := machine.New(def)
	require.NoError(t, err)
	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	clk := clock.NewFake(time.Unix(0, 0))
	orch := orchestrator.New(def, m, s, &timeoutNoopHarness{}, orchestrator.WithClock(clk))
	return orch, clk, s
}

// ── TestTimeout_FiresOnClockAdvance verifies that advancing the fake clock
// past the declared duration fires the synthetic transition. ───────────────
func TestTimeout_FiresOnClockAdvance(t *testing.T) {
	t.Parallel()
	orch, clk, _ := buildTimeoutRig(t)
	ctx := context.Background()

	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)

	// Teleport into waiting so on_enter runs and the timeout is armed.
	_, err = orch.Teleport(ctx, sid, inbox.TeleportTarget{State: "waiting"})
	require.NoError(t, err)

	require.NotEmpty(t, orch.TimeoutPendingStates(sid),
		"timeout should be armed after entering waiting")

	// Advance the clock past 10 days; the timer fires and the synthetic
	// transition lands the session in traveled.
	clk.Advance(11 * 24 * time.Hour)

	// The firing happens on a goroutine; wait for the dispatcher to drain.
	require.Eventually(t, func() bool {
		j, lerr := orch.LoadJourney(sid)
		if lerr != nil {
			return false
		}
		return j.State == app.StatePath("traveled")
	}, 2*time.Second, 5*time.Millisecond, "session should have moved to traveled")

	// The dispatcher should have removed the entry.
	require.Empty(t, orch.TimeoutPendingStates(sid),
		"timeout entry should be cleared after firing")
}

// ── TestTimeout_CancelledOnExit verifies that exiting the timeout state
// before the clock advances cancels the pending entry. ─────────────────────
func TestTimeout_CancelledOnExit(t *testing.T) {
	t.Parallel()
	orch, clk, _ := buildTimeoutRig(t)
	ctx := context.Background()

	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)

	_, err = orch.Teleport(ctx, sid, inbox.TeleportTarget{State: "waiting"})
	require.NoError(t, err)
	require.NotEmpty(t, orch.TimeoutPendingStates(sid))

	// Fire continue: normal exit; cancels the pending timeout.
	out, err := orch.RunIntent(ctx, sid, "continue", nil)
	require.NoError(t, err)
	require.Equal(t, app.StatePath("traveled"), out.NewState)

	require.Empty(t, orch.TimeoutPendingStates(sid),
		"cancellation should have removed the entry")

	// Advance the clock; nothing should fire (we're already terminal).
	clk.Advance(20 * 24 * time.Hour)

	j, err := orch.LoadJourney(sid)
	require.NoError(t, err)
	require.Equal(t, app.StatePath("traveled"), j.State)
}

// ── TestTimeout_PersistsAcrossOrchestratorRestart verifies that a pending
// entry is reloaded from the SQLite-backed store when a new orchestrator
// opens the same session. ─────────────────────────────────────────────────
func TestTimeout_PersistsAcrossOrchestratorRestart(t *testing.T) {
	t.Parallel()
	def, err := app.Load("testdata/timeout/app.yaml")
	require.NoError(t, err)
	m, err := machine.New(def)
	require.NoError(t, err)
	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	clk := clock.NewFake(time.Unix(0, 0))
	h := &timeoutNoopHarness{}
	orch1 := orchestrator.New(def, m, s, h, orchestrator.WithClock(clk))
	ctx := context.Background()

	sid, err := orch1.NewSession(ctx)
	require.NoError(t, err)
	_, err = orch1.Teleport(ctx, sid, inbox.TeleportTarget{State: "waiting"})
	require.NoError(t, err)
	require.NotEmpty(t, orch1.TimeoutPendingStates(sid))

	// Drop orch1 on the floor (simulating a crash).  Stopping its in-memory
	// dispatcher leaves the persisted row intact, mirroring what the OS sees
	// when the orchestrator process exits.
	orch1.ShutdownTimeoutsForTest()

	// A fresh orchestrator opens the same SQLite-backed store: it must
	// rearm the timeout from the persisted row.
	orch2 := orchestrator.New(def, m, s, h, orchestrator.WithClock(clk))
	require.NotEmpty(t, orch2.TimeoutPendingStates(sid),
		"new orchestrator should have rearmed the persisted timeout")

	// Advance past the deadline.  The rearmed timer must fire.
	clk.Advance(11 * 24 * time.Hour)
	require.Eventually(t, func() bool {
		j, lerr := orch2.LoadJourney(sid)
		if lerr != nil {
			return false
		}
		return j.State == app.StatePath("traveled")
	}, 2*time.Second, 5*time.Millisecond)
}

// ── TestTimeout_EmitsTimeoutFiredEvent verifies the synthetic turn carries
// a TimeoutFired annotation event so traces can distinguish from a
// user-driven transition. ──────────────────────────────────────────────────
func TestTimeout_EmitsTimeoutFiredEvent(t *testing.T) {
	t.Parallel()
	orch, clk, s := buildTimeoutRig(t)
	ctx := context.Background()

	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)
	_, err = orch.Teleport(ctx, sid, inbox.TeleportTarget{State: "waiting"})
	require.NoError(t, err)

	clk.Advance(11 * 24 * time.Hour)

	require.Eventually(t, func() bool {
		j, lerr := orch.LoadJourney(sid)
		if lerr != nil {
			return false
		}
		return j.State == app.StatePath("traveled")
	}, 2*time.Second, 5*time.Millisecond)

	hist, err := s.LoadHistory(sid)
	require.NoError(t, err)

	foundTimeout := false
	for _, ev := range hist {
		if ev.Kind == store.TimeoutFired {
			foundTimeout = true
			break
		}
	}
	require.True(t, foundTimeout, "history should contain TimeoutFired event; got %+v", hist)
}
