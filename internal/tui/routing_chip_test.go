package tui_test

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/require"

	tuipkg "kitsoki/internal/tui"
)

// forceTrueColour pins lipgloss to truecolour output for the lifetime
// of the test, then restores the prior profile. Tests in headless CI
// runs without a TTY otherwise see lipgloss flatten to Ascii and emit
// no escape sequences, so colour-assertion lines need this gate.
func forceTrueColour(t *testing.T) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}

// flagUpdateRouting controls golden-file regeneration. Run
//
//	go test -run TestRoutingChipGolden ./internal/tui/ -update-routing
//
// to refresh the testdata/routing_*.golden files after intentional
// render-shape changes. Defined here rather than reusing
// tui_test.go:flagUpdate so a stray `-update` from a different test
// run can't accidentally regenerate the routing goldens.
var flagUpdateRouting = flag.Bool("update-routing", false, "update routing_*.golden files")

// runChipUpdate is a tiny helper that processes a tea.Msg through the
// chip and unwraps the resulting model. We don't drain the returned
// tea.Cmd because none of the chip's commands (except the spinner
// tick) emit user-visible state — the chip resolves synchronously
// on RoutingTierHitMsg etc.
func runChipUpdate(t *testing.T, c tuipkg.RoutingChip, msg tea.Msg) tuipkg.RoutingChip {
	t.Helper()
	c, _ = c.Update(msg)
	return c
}

// stripANSI is provided by tui_test.go; we reuse it here for the
// routing-chip assertions.

// ─── Unit: per-tier icon + colour ────────────────────────────────────────────

func TestRoutingChipPerTierRender(t *testing.T) {
	forceTrueColour(t)
	cases := []struct {
		name     string
		tier     tuipkg.RoutingTier
		icon     string
		colour   string // expected #RRGGBB hex (lowercase)
		intent   string
		reason   string
		conf     float64
		hits     int
		latency  time.Duration
		slots    map[string]any
		wantTail string // substring expected somewhere in the rendered line (post-ANSI-strip)
	}{
		{
			name: "deterministic", tier: tuipkg.TierDeterministic, icon: "▣", colour: "#5fff5f",
			intent: "ford", wantTail: "▣ ford",
		},
		{
			name: "semantic bare", tier: tuipkg.TierSemantic, icon: "⌁", colour: "#5fd7ff",
			intent: "ford", reason: "synonym:wade", conf: 0.90,
			wantTail: "⌁ ford]  (synonym:wade · 0.90)",
		},
		{
			name: "template with slot", tier: tuipkg.TierTemplate, icon: "◐", colour: "#5fafff",
			intent: "propose_purchase", reason: "template:0", conf: 0.80,
			slots:    map[string]any{"items": "6 oxen and 200 lbs food", "total_cost": 240},
			wantTail: `◐ propose_purchase{items="6 oxen and 200 lbs food", total_cost=240}]  (template:0 · 0.80)`,
		},
		{
			name: "turncache", tier: tuipkg.TierTurncache, icon: "⟲", colour: "#ffd75f",
			intent: "hunt", reason: "cache", conf: 0.92, hits: 14,
			wantTail: "⟲ hunt]  (cache · 0.92 · 14 hits)",
		},
		{
			name: "llm", tier: tuipkg.TierLLM, icon: "✦", colour: "#d787ff",
			intent: "ask_question", reason: "claude-haiku", conf: 0.81, latency: 2400 * time.Millisecond,
			slots:    map[string]any{"question": "any advice for South Pass"},
			wantTail: `✦ ask_question{question="any advice for South Pass"}]  (claude-haiku · 0.81 · 2.4s)`,
		},
		{
			name: "offpath", tier: tuipkg.TierOffpath, icon: "◇", colour: "#878787",
			intent: "oracle", wantTail: "◇ oracle",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := tuipkg.NewRoutingChip("ignored input")
			c = runChipUpdate(t, c, tuipkg.RoutingTierHitMsg{
				Tier: tc.tier, Intent: tc.intent, Reason: tc.reason,
				Confidence: tc.conf, Hits: tc.hits, Latency: tc.latency, Slots: tc.slots,
			})
			require.True(t, c.Resolved(), "chip must resolve after RoutingTierHitMsg")
			raw := c.View()
			stripped := stripANSI(raw)
			require.Contains(t, stripped, tc.icon, "icon must appear in render: %q", stripped)
			require.Contains(t, stripped, tc.wantTail, "render tail mismatch: %q", stripped)
			// Colour: every tier except None should emit its hex in the
			// raw (ANSI) output when colour is enabled.
			if os.Getenv("NO_COLOR") == "" && os.Getenv("KITSOKI_NO_COLOR") == "" {
				// lipgloss renders foreground as "38;2;R;G;B". Convert hex
				// to that triplet so the assertion isn't fragile against
				// lipgloss internals.
				r, g, b := hexToRGB(t, tc.colour)
				wantAnsi := fmt.Sprintf("38;2;%d;%d;%d", r, g, b)
				require.Contains(t, raw, wantAnsi,
					"expected ANSI foreground %s for tier %s, got:\n%q",
					tc.colour, tc.name, raw)
			}
		})
	}
}

// ─── Unit: slot-bag formatting ───────────────────────────────────────────────

func TestRoutingChipSlotBagFormatting(t *testing.T) {
	cases := []struct {
		name  string
		slots map[string]any
		want  string // post-ANSI-strip, with surrounding "[⌁ X{...}]" trimmed to just the slot bag
	}{
		{name: "empty", slots: nil, want: ""},
		{name: "single int", slots: map[string]any{"n": 3}, want: "{n=3}"},
		{name: "single string", slots: map[string]any{"q": "hello"}, want: `{q="hello"}`},
		{name: "single bool", slots: map[string]any{"flag": true}, want: "{flag=true}"},
		{name: "multi sorted", slots: map[string]any{"z": 1, "a": 2, "m": 3}, want: "{a=2, m=3, z=1}"},
		{
			name:  "long string truncates",
			slots: map[string]any{"q": strings.Repeat("x", 50)},
			want:  fmt.Sprintf(`{q="%s…"}`, strings.Repeat("x", 32)),
		},
		{
			name:  "money float roundtrip",
			slots: map[string]any{"total_cost": float64(240)},
			want:  "{total_cost=240}",
		},
		{
			name:  "fractional float",
			slots: map[string]any{"pi": float64(3.14)},
			want:  "{pi=3.14}",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := tuipkg.NewRoutingChip("")
			c = runChipUpdate(t, c, tuipkg.RoutingTierHitMsg{
				Tier:       tuipkg.TierSemantic,
				Intent:     "intent_x",
				Slots:      tc.slots,
				Reason:     "synonym:x",
				Confidence: 0.9,
			})
			stripped := stripANSI(c.View())
			if tc.want == "" {
				// "intent_x" must NOT be followed by a "{".
				require.NotContains(t, stripped, "intent_x{",
					"empty slot bag should omit braces, got %q", stripped)
				return
			}
			require.Contains(t, stripped, tc.want,
				"slot-bag render mismatch in %q", stripped)
		})
	}
}

// ─── Unit: progressive transitions (faded prior icons) ───────────────────────

func TestRoutingChipProgressiveTransitions(t *testing.T) {
	c := tuipkg.NewRoutingChip("any advice for South Pass")
	// deterministic misses → semantic misses → cache misses → llm hits
	c = runChipUpdate(t, c, tuipkg.RoutingTierMissMsg{Tier: tuipkg.TierDeterministic})
	c = runChipUpdate(t, c, tuipkg.RoutingTierMissMsg{Tier: tuipkg.TierSemantic})
	c = runChipUpdate(t, c, tuipkg.RoutingTierMissMsg{Tier: tuipkg.TierTurncache})
	c = runChipUpdate(t, c, tuipkg.RoutingTierHitMsg{
		Tier: tuipkg.TierLLM, Intent: "ask_question",
		Slots:  map[string]any{"question": "any advice for South Pass"},
		Reason: "claude-haiku", Confidence: 0.81, Latency: 2400 * time.Millisecond,
	})
	require.True(t, c.Resolved())

	stripped := stripANSI(c.View())
	// Order check: faded ▣, faded ⌁, faded ⟲, then current ✦.
	idxDet := strings.Index(stripped, "▣")
	idxSem := strings.Index(stripped, "⌁")
	idxCache := strings.Index(stripped, "⟲")
	idxLLM := strings.Index(stripped, "✦")
	require.True(t, idxDet >= 0 && idxSem > idxDet && idxCache > idxSem && idxLLM > idxCache,
		"icons not in progressive order: %q", stripped)
}

// TestRoutingChipDeterministicMissThenSemanticHit verifies the specific
// shape the proposal §8 calls out: deterministic_miss → semantic_hit
// produces "[▣· ⌁ ford]".
func TestRoutingChipDeterministicMissThenSemanticHit(t *testing.T) {
	c := tuipkg.NewRoutingChip("wade across the river")
	c = runChipUpdate(t, c, tuipkg.RoutingTierMissMsg{Tier: tuipkg.TierDeterministic})
	c = runChipUpdate(t, c, tuipkg.RoutingTierHitMsg{
		Tier: tuipkg.TierSemantic, Intent: "ford",
		Reason: "synonym:wade", Confidence: 0.9,
	})
	stripped := stripANSI(c.View())
	require.Contains(t, stripped, "▣")
	require.Contains(t, stripped, "·")
	require.Contains(t, stripped, "⌁ ford")
}

// ─── Unit: cancelled state ───────────────────────────────────────────────────

func TestRoutingChipCancelled(t *testing.T) {
	c := tuipkg.NewRoutingChip("hunt")
	c = runChipUpdate(t, c, tuipkg.RoutingTierMissMsg{Tier: tuipkg.TierDeterministic})
	c = runChipUpdate(t, c, tuipkg.RoutingCancelMsg{})
	require.True(t, c.Resolved())
	stripped := stripANSI(c.View())
	require.Contains(t, stripped, "[✕ cancelled]",
		"cancelled state must render as [✕ cancelled], got %q", stripped)
}

// ─── Unit: ambiguous (inline) ────────────────────────────────────────────────

func TestRoutingChipAmbiguousTwoWay(t *testing.T) {
	c := tuipkg.NewRoutingChip("cross")
	c = runChipUpdate(t, c, tuipkg.RoutingAmbiguousMsg{Candidates: []string{"ford", "wade"}})
	stripped := stripANSI(c.View())
	require.Contains(t, stripped, "[? ford | wade]",
		"two-way disambig must render inline, got %q", stripped)
	require.Contains(t, stripped, "you: cross")
	// Candidates accessor returns both.
	require.Equal(t, []string{"ford", "wade"}, c.Candidates())
	require.Equal(t, "ford", c.ResolveCandidate(0))
	require.Equal(t, "wade", c.ResolveCandidate(1))
	require.Equal(t, "", c.ResolveCandidate(2))
}

func TestRoutingChipAmbiguousThreeWayFallbackToModal(t *testing.T) {
	c := tuipkg.NewRoutingChip("go")
	c = runChipUpdate(t, c, tuipkg.RoutingAmbiguousMsg{
		Candidates: []string{"north", "south", "east"},
	})
	stripped := stripANSI(c.View())
	// ≥3 ways: chip prints a stub; the modal disambiguation card
	// handles the actual pick. The stub must NOT pipe-separate three
	// names (that's the inline path).
	require.NotContains(t, stripped, "north | south | east",
		"≥3-way disambig must not render inline, got %q", stripped)
	require.Contains(t, stripped, "3 ways", "expected fallback indicator, got %q", stripped)
}

// ─── Unit: NO_COLOR fallback ─────────────────────────────────────────────────

func TestNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	c := tuipkg.NewRoutingChip("hunt")
	c = runChipUpdate(t, c, tuipkg.RoutingTierHitMsg{
		Tier: tuipkg.TierTurncache, Intent: "hunt",
		Reason: "cache", Confidence: 0.92, Hits: 14,
	})
	raw := c.View()
	// No ESC byte (0x1b) means no ANSI escape sequences.
	require.False(t, strings.ContainsRune(raw, 0x1b),
		"NO_COLOR=1 render must contain no ANSI escapes, got %q", raw)
	require.Contains(t, raw, "⟲ hunt")
	require.Contains(t, raw, "(cache · 0.92 · 14 hits)")
}

func TestNoColorKitsokiVariant(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("KITSOKI_NO_COLOR", "yes")
	c := tuipkg.NewRoutingChip("hunt")
	c = runChipUpdate(t, c, tuipkg.RoutingTierHitMsg{
		Tier: tuipkg.TierLLM, Intent: "ask_question",
		Reason: "claude-haiku", Confidence: 0.81, Latency: 1500 * time.Millisecond,
	})
	raw := c.View()
	require.False(t, strings.ContainsRune(raw, 0x1b),
		"KITSOKI_NO_COLOR render must contain no ANSI escapes, got %q", raw)
}

// ─── Unit: input-queue (latest-wins, esc-cancel semantics) ────────────────────

func TestPendingLineQueue(t *testing.T) {
	q := tuipkg.NewPendingLineForTest()
	require.False(t, q.HasPending())
	require.Equal(t, "", q.FooterIndicator())

	// First enqueue.
	q.Enqueue("first line")
	require.True(t, q.HasPending())
	require.Equal(t, "1 queued · esc to cancel", q.FooterIndicator())

	// Second enqueue replaces; dropped counter ticks.
	q.Enqueue("second line")
	require.True(t, q.HasPending())
	require.Contains(t, q.FooterIndicator(), "1 dropped",
		"footer must reflect displaced entries")

	// Third enqueue — dropped counter still ticks.
	q.Enqueue("third line")
	require.Contains(t, q.FooterIndicator(), "2 dropped")

	// Take returns the most recent only.
	line, ok := q.Take()
	require.True(t, ok)
	require.Equal(t, "third line", line)
	require.False(t, q.HasPending(), "queue must be empty after Take")

	// Empty Take is the zero case.
	line, ok = q.Take()
	require.False(t, ok)
	require.Equal(t, "", line)

	// Enqueue of empty string is a no-op.
	q.Enqueue("")
	require.False(t, q.HasPending())

	// Clear empties without returning.
	q.Enqueue("clear me")
	q.Clear()
	require.False(t, q.HasPending())
	require.Equal(t, "", q.FooterIndicator())
}

// ─── Unit: ESC-cancel semantics surfaced through the chip ────────────────────

func TestRoutingChipEscCancelClearsQueue(t *testing.T) {
	// This test asserts the integration contract documented in §8.2:
	// when ESC fires mid-flight, the chip resolves to cancelled and
	// the surrounding queue is cleared (no replay).
	q := tuipkg.NewPendingLineForTest()
	c := tuipkg.NewRoutingChip("test")
	q.Enqueue("queued during turn")

	// ESC fires → cancel + clear queue.
	c = runChipUpdate(t, c, tuipkg.RoutingCancelMsg{})
	q.Clear()

	require.Equal(t, tuipkg.TierCancelled, c.Tier())
	require.False(t, q.HasPending(),
		"ESC cancel must drop the queued line (no replay)")
}

// ─── Snapshot: per-tier golden ───────────────────────────────────────────────

func TestRoutingChipGolden(t *testing.T) {
	cases := []struct {
		name  string
		setup func() tuipkg.RoutingChip
	}{
		{
			name: "deterministic",
			setup: func() tuipkg.RoutingChip {
				c := tuipkg.NewRoutingChip("go south")
				c, _ = c.Update(tuipkg.RoutingTierHitMsg{
					Tier: tuipkg.TierDeterministic, Intent: "go_south",
				})
				return c
			},
		},
		{
			name: "semantic",
			setup: func() tuipkg.RoutingChip {
				c := tuipkg.NewRoutingChip("wade across the river")
				c, _ = c.Update(tuipkg.RoutingTierMissMsg{Tier: tuipkg.TierDeterministic})
				c, _ = c.Update(tuipkg.RoutingTierHitMsg{
					Tier:       tuipkg.TierSemantic,
					Intent:     "ford",
					Reason:     "synonym:wade",
					Confidence: 0.90,
				})
				return c
			},
		},
		{
			name: "template",
			setup: func() tuipkg.RoutingChip {
				c := tuipkg.NewRoutingChip("buy 6 oxen and 200 lbs food for $240")
				c, _ = c.Update(tuipkg.RoutingTierMissMsg{Tier: tuipkg.TierDeterministic})
				c, _ = c.Update(tuipkg.RoutingTierHitMsg{
					Tier:       tuipkg.TierTemplate,
					Intent:     "propose_purchase",
					Reason:     "template:0",
					Confidence: 0.80,
					Slots: map[string]any{
						"items":      "6 oxen and 200 lbs food",
						"total_cost": 240,
					},
				})
				return c
			},
		},
		{
			name: "turncache",
			setup: func() tuipkg.RoutingChip {
				c := tuipkg.NewRoutingChip("let's hunt")
				c, _ = c.Update(tuipkg.RoutingTierMissMsg{Tier: tuipkg.TierDeterministic})
				c, _ = c.Update(tuipkg.RoutingTierMissMsg{Tier: tuipkg.TierSemantic})
				c, _ = c.Update(tuipkg.RoutingTierHitMsg{
					Tier:       tuipkg.TierTurncache,
					Intent:     "hunt",
					Reason:     "cache",
					Confidence: 0.92,
					Hits:       14,
				})
				return c
			},
		},
		{
			name: "llm",
			setup: func() tuipkg.RoutingChip {
				c := tuipkg.NewRoutingChip("any advice for South Pass")
				c, _ = c.Update(tuipkg.RoutingTierMissMsg{Tier: tuipkg.TierDeterministic})
				c, _ = c.Update(tuipkg.RoutingTierMissMsg{Tier: tuipkg.TierSemantic})
				c, _ = c.Update(tuipkg.RoutingTierMissMsg{Tier: tuipkg.TierTurncache})
				c, _ = c.Update(tuipkg.RoutingTierHitMsg{
					Tier:       tuipkg.TierLLM,
					Intent:     "ask_question",
					Reason:     "claude-haiku",
					Confidence: 0.81,
					Latency:    2400 * time.Millisecond,
					Slots:      map[string]any{"question": "any advice for South Pass"},
				})
				return c
			},
		},
		{
			name: "offpath",
			setup: func() tuipkg.RoutingChip {
				c := tuipkg.NewRoutingChip("ask the oracle anything")
				c, _ = c.Update(tuipkg.RoutingTierHitMsg{
					Tier: tuipkg.TierOffpath, Intent: "oracle",
				})
				return c
			},
		},
		{
			name: "cancelled",
			setup: func() tuipkg.RoutingChip {
				c := tuipkg.NewRoutingChip("hunt")
				c, _ = c.Update(tuipkg.RoutingTierMissMsg{Tier: tuipkg.TierDeterministic})
				c, _ = c.Update(tuipkg.RoutingCancelMsg{})
				return c
			},
		},
		{
			name: "ambiguous_2way",
			setup: func() tuipkg.RoutingChip {
				c := tuipkg.NewRoutingChip("cross")
				c, _ = c.Update(tuipkg.RoutingAmbiguousMsg{
					Candidates: []string{"ford", "wade"},
				})
				return c
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.setup()
			got := stripANSI(c.View())
			goldenPath := filepath.Join("testdata", "routing_"+tc.name+".golden")
			if *flagUpdateRouting {
				require.NoError(t, os.MkdirAll(filepath.Dir(goldenPath), 0o755))
				require.NoError(t, os.WriteFile(goldenPath, []byte(got+"\n"), 0o644))
				t.Logf("updated %s", goldenPath)
				return
			}
			want, err := os.ReadFile(goldenPath)
			require.NoError(t, err, "missing golden — re-run with -update-routing")
			require.Equal(t, strings.TrimRight(string(want), "\n"), got,
				"render diverged from golden %s", goldenPath)
		})
	}
}

// ─── Example for godoc ───────────────────────────────────────────────────────

func ExampleRoutingChip() {
	os.Setenv("NO_COLOR", "1") // example output stays readable
	defer os.Unsetenv("NO_COLOR")
	c := tuipkg.NewRoutingChip("wade across the river")
	c, _ = c.Update(tuipkg.RoutingTierMissMsg{Tier: tuipkg.TierDeterministic})
	c, _ = c.Update(tuipkg.RoutingTierHitMsg{
		Tier:       tuipkg.TierSemantic,
		Intent:     "ford",
		Reason:     "synonym:wade",
		Confidence: 0.90,
	})
	fmt.Println(c.View())
	// Output: [▣· ⌁ ford]  (synonym:wade · 0.90)
}

// hexToRGB parses "#RRGGBB" into three ints. Used for asserting that
// lipgloss emitted the right truecolor sequence.
func hexToRGB(t *testing.T, hex string) (int, int, int) {
	t.Helper()
	require.Equal(t, 7, len(hex), "expected #RRGGBB, got %q", hex)
	var r, g, b int
	_, err := fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b)
	require.NoError(t, err)
	return r, g, b
}
