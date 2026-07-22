package orchestrator_test

// Regression for the SECOND drive-halt (distinct from the sibling-emit
// collision): a long AUTONOMOUS pipeline stalls one hop short of its terminal
// @exit because settlePostBindEmits' anti-cycle recursion cap counts FORWARD
// host-bind waves, not just cycles.
//
// Observed on execs vmpool-dab6325d6205 / vmpool-bf8147525933: the full bugfix
// drive (idle -> ... -> tail.integrate -> tail.verify -> tail.cleanup ->
// tail.report -> @exit:shipped) halted at bf.tail.cleanup at rounds=9 with
//   "settlePostBindEmits: orchestrator recursion depth 5 exceeded cap 4"
// stranding the drive before cleanup->report->@exit:shipped (outcome 'missing').
// Each distinct tail room does (host.run -> bind -> deferred emit -> next room),
// so a linear pipeline of N rooms needs N recursion levels — the old cap of 4
// was simply below the full pipeline's length.
//
// This test reproduces the mechanism at the orchestrator layer with a synthetic
// LINEAR chain of 12 distinct host-gated rooms ending in a terminal @exit. Each
// room binds its OWN world key via a host call and emits the next hop gated on
// that freshly-bound key, so every hop is DEFERRED and consumes exactly one
// settlePostBindEmits wave (mirroring the real tail). With the cap at 4 the
// drive strands mid-chain with a HarnessError; with the raised cap it drives
// all the way to the terminal @exit with no error.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/host"
	"kitsoki/internal/machine"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/store"
)

// TestSettlePostBindEmits_LongLinearChainReachesTerminalExit drives a 12-room
// host-gated chain to a terminal @exit and asserts it arrives — the regression
// guard for the tail-stall depth-cap bug.
func TestSettlePostBindEmits_LongLinearChainReachesTerminalExit(t *testing.T) {
	const nRooms = 12 // > OrchestratorPostBindMaxDepth's old value of 4; needs the raise.

	// Build a linear story: ready --start--> r1 --hop--> r2 --hop--> ... --> done.
	// Each rN.on_enter binds world.kN via host.bump then emits `hop` gated on
	// world.kN == "go" (DEFERRED, because kN is unbound at machine time).
	var b strings.Builder
	b.WriteString(`
app:
  id: post-bind-long-chain
  version: 0.1.0
hosts:
  - host.bump
intents:
  start: {}
  hop: {}
root: ready
states:
  ready:
    on:
      start:
        - target: r1
`)
	for i := 1; i <= nRooms; i++ {
		target := "done"
		if i < nRooms {
			target = fmt.Sprintf("r%d", i+1)
		}
		fmt.Fprintf(&b, `  r%d:
    on_enter:
      - invoke: host.bump
        with: {}
        bind:
          k%d: "value"
      - emit_intent: hop
        when: "world.k%d == 'go'"
    on:
      hop:
        - target: %s
`, i, i, i, target)
	}
	// Terminal @exit the chain must reach.
	b.WriteString(`  done:
    terminal: true
    view: "shipped"
`)

	def, err := app.LoadBytes([]byte(b.String()))
	require.NoError(t, err)

	m, err := machine.New(def)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	// Single host handler: always binds {value: "go"} so each room's
	// deferred emit_intent when passes AFTER the bind (and only after).
	reg := host.NewRegistry()
	reg.Register("host.bump", func(ctx context.Context, args map[string]any) (host.Result, error) {
		return host.Result{Data: map[string]any{"value": "go"}}, nil
	})

	orch := orchestrator.New(def, m, s, noopOrchestratorHarness{}, orchestrator.WithHostRegistry(reg))

	ctx := context.Background()
	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)

	out, err := orch.SubmitDirect(ctx, sid, "start", nil)
	require.NoError(t, err)

	// With the old cap (4) the drive strands mid-chain: HarnessError is set to
	// the "exceeded cap" message and the session never reaches `done`.
	require.Empty(t, out.HarnessError,
		"a long linear host-gated pipeline must not trip the anti-cycle recursion cap; got: %s", out.HarnessError)

	journey, err := orch.LoadJourney(sid)
	require.NoError(t, err)
	require.Equal(t, app.StatePath("done"), journey.State,
		"the drive must reach the terminal @exit at the end of the %d-room chain, not stall mid-pipeline", nRooms)
}
