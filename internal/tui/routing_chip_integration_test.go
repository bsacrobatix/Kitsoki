package tui_test

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	tuipkg "kitsoki/internal/tui"
)

// TestRootModel_RoutingChipWiring drives the RootModel through one
// "Enter pressed → routing events arrive → chip resolves" cycle and
// asserts the chip transitions through the right tiers. The
// orchestrator is the real cloak orchestrator (via setupCloak) so
// the routing events come from real wiring, but we drive them
// directly through Update() instead of waiting for the slog →
// observer → tea.Program.Send round-trip (which is the unit covered
// by TestRoutingObserver_DispatchToProgram).
func TestRootModel_RoutingChipWiring(t *testing.T) {
	cases := []struct {
		name     string
		messages []tea.Msg
		wantTier tuipkg.RoutingTier
		wantView string // substring expected in the chip's rendered line
	}{
		{
			name: "deterministic_miss then semantic_hit",
			messages: []tea.Msg{
				tuipkg.RoutingChipReset{Input: "wade across"},
				tuipkg.RoutingTierMissMsg{Tier: tuipkg.TierDeterministic},
				tuipkg.RoutingTierHitMsg{
					Tier:       tuipkg.TierSemantic,
					Intent:     "ford",
					Reason:     "synonym:wade",
					Confidence: 0.9,
				},
			},
			wantTier: tuipkg.TierSemantic,
			wantView: "⌁ ford",
		},
		{
			name: "miss-miss then llm_routed",
			messages: []tea.Msg{
				tuipkg.RoutingChipReset{Input: "any advice"},
				tuipkg.RoutingTierMissMsg{Tier: tuipkg.TierDeterministic},
				tuipkg.RoutingTierMissMsg{Tier: tuipkg.TierSemantic},
				tuipkg.RoutingTierHitMsg{
					Tier:       tuipkg.TierLLM,
					Intent:     "ask_question",
					Reason:     "claude-haiku",
					Confidence: 0.81,
				},
			},
			wantTier: tuipkg.TierLLM,
			wantView: "✦ ask_question",
		},
		{
			name: "cancel",
			messages: []tea.Msg{
				tuipkg.RoutingChipReset{Input: "anything"},
				tuipkg.RoutingTierMissMsg{Tier: tuipkg.TierDeterministic},
				tuipkg.RoutingCancelMsg{},
			},
			wantTier: tuipkg.TierCancelled,
			wantView: "[✕ cancelled]",
		},
		{
			name: "ambiguous 2-way",
			messages: []tea.Msg{
				tuipkg.RoutingChipReset{Input: "cross"},
				tuipkg.RoutingAmbiguousMsg{Candidates: []string{"ford", "wade"}},
			},
			wantTier: tuipkg.TierAmbiguous,
			wantView: "[? ford | wade]",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Not parallel: setupCloak shares t.Setenv state in some
			// of the other tests; keeping serial isolates failures.
			orch, sid := setupCloak(t)
			m := buildModel(t, orch, sid)

			for _, msg := range tc.messages {
				m, _ = m.Update(msg)
			}

			rm, ok := tuipkg.ExtractRootModel(m)
			require.True(t, ok)
			require.Equal(t, tc.wantTier, tuipkg.RoutingChipTier(rm),
				"chip should have resolved to tier %v", tc.wantTier)

			rendered := tuipkg.RoutingChipView(rm)
			stripped := stripANSI(rendered)
			require.Contains(t, stripped, tc.wantView,
				"chip render mismatch: %q", stripped)
		})
	}
}

// TestRootModel_RoutingChipResetOnSubmit verifies that submitting a
// new turn re-pumps the chip back to TierNone — even after a prior
// resolution. Without this, the second-turn chip would still show
// the previous turn's "✦ ford" line.
func TestRootModel_RoutingChipResetOnSubmit(t *testing.T) {
	orch, sid := setupCloak(t)
	m := buildModel(t, orch, sid)

	// Resolve once.
	m, _ = m.Update(tuipkg.RoutingChipReset{Input: "first"})
	m, _ = m.Update(tuipkg.RoutingTierHitMsg{
		Tier: tuipkg.TierSemantic, Intent: "ford",
		Reason: "synonym:wade", Confidence: 0.9,
	})
	rm, _ := tuipkg.ExtractRootModel(m)
	require.Equal(t, tuipkg.TierSemantic, tuipkg.RoutingChipTier(rm))

	// Re-reset (simulating a new turn submission).
	m, _ = m.Update(tuipkg.RoutingChipReset{Input: "second"})
	rm, _ = tuipkg.ExtractRootModel(m)
	require.Equal(t, tuipkg.TierNone, tuipkg.RoutingChipTier(rm),
		"second-turn reset must wipe the prior resolution")
	require.True(t, tuipkg.RoutingChipActive(rm),
		"reset must mark the chip as active (in flight)")
}

// TestRootModel_CtrlRTogglesOverlay drives the ctrl+r overlay
// keybind through the RootModel's Update and asserts:
//
//   - ctrl+r opens the overlay (routingTraceOpen=true)
//   - ctrl+r again closes it
//   - ESC also closes it
//   - the overlay body includes the captured trace
//
// Tests the full path: routeKey → routingTraceOpen → View overlay.
func TestRootModel_CtrlRTogglesOverlay(t *testing.T) {
	orch, sid := setupCloak(t)
	m := buildModel(t, orch, sid).(tuipkg.RootModel)

	// Without an observer attached, ctrl+r is a no-op.
	m2, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	rm, _ := tuipkg.ExtractRootModel(m2)
	require.False(t, tuipkg.RoutingTraceOpen(rm),
		"ctrl+r should be a no-op without a routing observer")

	// Install an observer and emit a routing event so the overlay
	// has something to render.
	obs := tuipkg.NewRoutingObserver(sid)
	tuipkg.SetRoutingObserverForTest(&m, obs)

	// Emit one routing event so the overlay isn't empty when toggled.
	emitRoutingEvent(t, obs, sid, 1, "turn.semantic_hit")

	// First ctrl+r: open.
	mModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	rm, _ = tuipkg.ExtractRootModel(mModel)
	require.True(t, tuipkg.RoutingTraceOpen(rm), "ctrl+r must open the overlay")

	body := tuipkg.RenderRoutingTraceOverlayForTest(rm)
	require.Contains(t, body, "routing trace",
		"overlay must include the header, got:\n%s", body)
	require.Contains(t, body, "turn.semantic_hit",
		"overlay must include the captured event, got:\n%s", body)

	// Second ctrl+r: close.
	mModel, _ = mModel.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	rm, _ = tuipkg.ExtractRootModel(mModel)
	require.False(t, tuipkg.RoutingTraceOpen(rm), "second ctrl+r must close the overlay")

	// Re-open then close via Esc.
	mModel, _ = mModel.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	rm, _ = tuipkg.ExtractRootModel(mModel)
	require.True(t, tuipkg.RoutingTraceOpen(rm))

	mModel, _ = mModel.Update(tea.KeyMsg{Type: tea.KeyEsc})
	rm, _ = tuipkg.ExtractRootModel(mModel)
	require.False(t, tuipkg.RoutingTraceOpen(rm),
		"Esc must close the overlay (without opening the system menu)")
}

// TestRootModel_RoutingChipNoColorInView is a regression guard for §8:
// the chip rendered through the RootModel.View() must obey NO_COLOR.
// The chip-level test (TestNoColor) covers the chip in isolation;
// this test verifies the wiring layer doesn't re-introduce escapes.
func TestRootModel_RoutingChipNoColorInView(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	orch, sid := setupCloak(t)
	m := buildModel(t, orch, sid)
	m, _ = m.Update(tuipkg.RoutingChipReset{Input: "test"})
	m, _ = m.Update(tuipkg.RoutingTierHitMsg{
		Tier: tuipkg.TierLLM, Intent: "ask",
		Reason: "claude-haiku", Confidence: 0.81,
	})
	rendered := m.View()
	require.False(t, strings.ContainsRune(rendered, 0x1b),
		"NO_COLOR=1 RootModel.View() must contain no ANSI escapes after chip wiring")
}
