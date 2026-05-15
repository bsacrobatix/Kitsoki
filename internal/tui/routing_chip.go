// Package tui — RoutingChip is the progressive resolution badge described
// in the semantic-routing proposal §8.
//
// A turn passes through up to four routing tiers (deterministic →
// semantic synonym/template → turncache → LLM) plus an off-path side
// exit. Each tier emits a structured slog event (see
// internal/trace/routing_events.go). The chip is a small Bubbletea
// sub-model that receives these events as tea.Msg values and renders
// a single styled line: an `⋯` spinner while resolving, a colour-coded
// icon + canonical intent when a tier resolves, and a row of faded
// prior icons for each tier the resolver fell through.
//
// The chip is deliberately decoupled from the orchestrator: it never
// imports orchestrator and never calls back into one. Its only inputs
// are the typed tea.Msg events below; its output is a styled string
// rendered alongside the user's echoed input.
//
// Event → icon mapping (§8 table):
//
//	turn.deterministic_hit  → ▣  bright green   (#5fff5f)
//	turn.semantic_hit       → ⌁  cyan           (#5fd7ff)   bare synonym
//	                        → ◐  sky blue       (#5fafff)   reason starts "template:"
//	turn.turncache_hit      → ⟲  yellow         (#ffd75f)
//	turn.llm_routed         → ✦  magenta        (#d787ff)
//	turn.offpath_routed     → ◇  grey           (#878787)
//	turn.cancelled          → ✕  grey           (#878787)
//
// NO_COLOR / KITSOKI_NO_COLOR fall back to monochrome — icons remain
// distinct.
//
// The chip does NOT itself subscribe to slog: the surrounding TUI
// translates trace events into chip messages. This keeps the chip
// pure-Bubbletea and trivially unit-testable.
package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// RoutingTier identifies one of the routing tiers. Order matters: a
// tier with a higher numeric value is "later" in the resolver and
// implies every earlier tier missed.
type RoutingTier int

const (
	// TierNone is the zero state — nothing has resolved or missed yet.
	// The chip renders `⋯ resolving…` in this state.
	TierNone RoutingTier = iota
	// TierDeterministic — matched the menu / synonym pre-pass (`▣`).
	TierDeterministic
	// TierSemantic — matched the bare-synonym tier (`⌁`).
	TierSemantic
	// TierTemplate — matched a synonym template with captured slots (`◐`).
	TierTemplate
	// TierTurncache — served from the per-(app,state,signature) cache (`⟲`).
	TierTurncache
	// TierLLM — resolved by the LLM harness (`✦`).
	TierLLM
	// TierOffpath — routed to the off-path side-channel (`◇`).
	TierOffpath
	// TierCancelled — the user pressed ESC mid-flight (`✕`).
	TierCancelled
	// TierAmbiguous — the semantic matcher returned ≥2 candidates in
	// the tie band; inline 2-way prompt or modal card.
	TierAmbiguous
)

// tierIcon returns the single-rune badge for a tier.
func tierIcon(t RoutingTier) string {
	switch t {
	case TierDeterministic:
		return "▣"
	case TierSemantic:
		return "⌁"
	case TierTemplate:
		return "◐"
	case TierTurncache:
		return "⟲"
	case TierLLM:
		return "✦"
	case TierOffpath:
		return "◇"
	case TierCancelled:
		return "✕"
	case TierAmbiguous:
		return "?"
	default:
		return "⋯"
	}
}

// tierColour returns the lipgloss colour for a tier. Empty string means
// "no colour" (NO_COLOR mode).
func tierColour(t RoutingTier) lipgloss.Color {
	switch t {
	case TierDeterministic:
		return lipgloss.Color("#5fff5f") // bright green
	case TierSemantic, TierAmbiguous:
		return lipgloss.Color("#5fd7ff") // cyan
	case TierTemplate:
		return lipgloss.Color("#5fafff") // sky blue
	case TierTurncache:
		return lipgloss.Color("#ffd75f") // yellow
	case TierLLM:
		return lipgloss.Color("#d787ff") // magenta
	case TierOffpath, TierCancelled:
		return lipgloss.Color("#878787") // grey
	default:
		return lipgloss.Color("#878787")
	}
}

// slotMaxStringRuneLen caps an individual string slot value at this many
// runes in the chip's render. Longer values truncate with `…`. The full
// value is visible via the route-trace overlay (`ctrl+r`).
const slotMaxStringRuneLen = 32

// ─── Messages (tea.Msg) ──────────────────────────────────────────────────────

// RoutingChipReset returns the chip to TierNone with `⋯ resolving…`.
// The TUI sends this the instant Enter is pressed.
type RoutingChipReset struct {
	Input string // the user-echoed input being resolved
}

// RoutingTierMissMsg advances the chip past a tier without resolving.
// Sent on turn.deterministic_miss / turn.semantic_miss / equivalent.
type RoutingTierMissMsg struct {
	Tier RoutingTier
}

// RoutingTierHitMsg resolves the chip at a tier with full result detail.
// The chip stops here and waits for RoutingChipReset.
type RoutingTierHitMsg struct {
	Tier       RoutingTier
	Intent     string
	Slots      map[string]any
	Confidence float64
	// Reason carries the originating tier detail. Convention:
	//   synonym:wade      — bare synonym match
	//   template:0        — template index N
	//   cache             — turncache hit
	//   claude-haiku      — model name
	// The chip prints this verbatim in the detail parens after the icon.
	Reason string
	// Hits is the cache hit count, when Tier==TierTurncache (zero otherwise).
	Hits int
	// Latency is the resolver wall-time, when Tier==TierLLM (zero otherwise).
	Latency time.Duration
}

// RoutingAmbiguousMsg signals a ≥2-way tie. Candidates is the canonical
// intent name list. The chip renders an inline prompt when len==2 and
// defers to the existing modal disambiguation card otherwise.
type RoutingAmbiguousMsg struct {
	Candidates []string
}

// RoutingCancelMsg resolves the chip to `[✕ cancelled]`.
type RoutingCancelMsg struct{}

// RoutingChipResolved is fired by the chip itself when a tier resolves.
// The surrounding TUI listens for this to promote the line into the
// recent-turns view.
type RoutingChipResolved struct {
	Tier  RoutingTier
	Final string // the chip's final rendered line (without the input echo)
}

// ─── Model ───────────────────────────────────────────────────────────────────

// RoutingChip is the progressive resolution badge sub-model.
type RoutingChip struct {
	// Input is the user line being resolved; echoed above the chip.
	input string

	// current is the last tier the chip resolved at (or TierNone while
	// in flight).
	current RoutingTier

	// missed records every tier the resolver fell through. Used to draw
	// the faded prior-icon trail [▣· ⌁· ⟲· ✦ …].
	missed []RoutingTier

	// resolved is true once the chip has reached a terminal state
	// (hit / cancelled). Further messages are ignored until Reset.
	resolved bool

	// Hit-state detail fields, populated by RoutingTierHitMsg.
	intent     string
	slots      map[string]any
	confidence float64
	reason     string
	hits       int
	latency    time.Duration

	// Ambiguous-state detail. When len(candidates)==2 the chip renders
	// the inline two-way prompt; ≥3 leaves it to the modal flow.
	candidates []string

	// spinner is shown while current==TierNone (initial frame only).
	spinner spinner.Model

	// noColour, when true, suppresses lipgloss colour styling. Computed
	// once from env vars (NO_COLOR / KITSOKI_NO_COLOR) at construction.
	noColour bool
}

// NewRoutingChip constructs a fresh chip in the TierNone state with the
// supplied user input echoed.
func NewRoutingChip(input string) RoutingChip {
	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#878787"))
	return RoutingChip{
		input:    input,
		current:  TierNone,
		spinner:  sp,
		noColour: noColourEnabled(),
	}
}

// noColourEnabled returns true when NO_COLOR or KITSOKI_NO_COLOR is set
// to a non-empty value in the environment.
func noColourEnabled() bool {
	if v := os.Getenv("NO_COLOR"); v != "" && v != "0" {
		return true
	}
	if v := os.Getenv("KITSOKI_NO_COLOR"); v != "" && v != "0" {
		return true
	}
	return false
}

// Init satisfies tea.Model. Starts the spinner.
func (c RoutingChip) Init() tea.Cmd { return c.spinner.Tick }

// Update handles RoutingChip* messages and the spinner tick. Any other
// message is silently passed through.
func (c RoutingChip) Update(msg tea.Msg) (RoutingChip, tea.Cmd) {
	switch m := msg.(type) {
	case RoutingChipReset:
		c = NewRoutingChip(m.Input)
		return c, c.spinner.Tick

	case RoutingTierMissMsg:
		if c.resolved {
			return c, nil
		}
		c.missed = append(c.missed, m.Tier)
		return c, nil

	case RoutingTierHitMsg:
		if c.resolved {
			return c, nil
		}
		c.current = m.Tier
		c.intent = m.Intent
		c.slots = m.Slots
		c.confidence = m.Confidence
		c.reason = m.Reason
		c.hits = m.Hits
		c.latency = m.Latency
		c.resolved = true
		final := c.renderResolved()
		return c, func() tea.Msg {
			return RoutingChipResolved{Tier: m.Tier, Final: final}
		}

	case RoutingAmbiguousMsg:
		if c.resolved {
			return c, nil
		}
		c.current = TierAmbiguous
		c.candidates = m.Candidates
		// Two-way ties resolve inline; ≥3 leave to the modal flow but
		// the chip still marks itself resolved so it stops spinning.
		c.resolved = true
		final := c.renderResolved()
		return c, func() tea.Msg {
			return RoutingChipResolved{Tier: TierAmbiguous, Final: final}
		}

	case RoutingCancelMsg:
		if c.resolved {
			return c, nil
		}
		c.current = TierCancelled
		c.resolved = true
		final := c.renderResolved()
		return c, func() tea.Msg {
			return RoutingChipResolved{Tier: TierCancelled, Final: final}
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		c.spinner, cmd = c.spinner.Update(msg)
		return c, cmd
	}
	return c, nil
}

// Resolved reports whether the chip has reached a terminal state. The
// surrounding TUI uses this to know when to promote the line and clear
// the chip.
func (c RoutingChip) Resolved() bool { return c.resolved }

// Tier returns the chip's current tier (TierNone while in flight).
func (c RoutingChip) Tier() RoutingTier { return c.current }

// Candidates returns the ambiguity-band intent names (nil unless the
// chip received a RoutingAmbiguousMsg). The surrounding TUI reads this
// when handling 1/2 keypresses in the inline prompt.
func (c RoutingChip) Candidates() []string { return c.candidates }

// ResolveCandidate is called by the TUI when the user picks an inline
// ambiguity candidate (`1` or `2`). It returns the chosen intent name,
// or the empty string when idx is out of range.
func (c RoutingChip) ResolveCandidate(idx int) string {
	if idx < 0 || idx >= len(c.candidates) {
		return ""
	}
	return c.candidates[idx]
}

// View renders the chip line. Format depends on state:
//
//	in flight, no misses:   [⋯ resolving…]
//	in flight, with misses: [▣· ⌁· ⋯ resolving…]
//	resolved:               [▣· ⌁· ⟲· ✦ ask_question{…}]   (detail follows)
//	cancelled:              [✕ cancelled]
//	ambiguous (2-way):      [? ford | wade] you: cross
//	ambiguous (≥3):         [? 3 ways] you: cross    (modal still fires)
func (c RoutingChip) View() string {
	if !c.resolved {
		return c.renderInFlight()
	}
	return c.renderResolved()
}

// renderInFlight produces the chip while the resolver is still running.
// Each prior tier in c.missed is rendered faded, followed by a spinner
// glyph and the "resolving…" label.
func (c RoutingChip) renderInFlight() string {
	var sb strings.Builder
	sb.WriteString("[")
	for i, t := range c.missed {
		if i > 0 {
			sb.WriteString(" ")
		}
		sb.WriteString(c.fadedIcon(t))
		sb.WriteString("·")
	}
	if len(c.missed) > 0 {
		sb.WriteString(" ")
	}
	sb.WriteString(c.spinner.View())
	sb.WriteString(" resolving…")
	sb.WriteString("]")
	return sb.String()
}

// renderResolved produces the chip after a terminal state. The result
// is a single line; the surrounding TUI is responsible for hanging-indent
// wrapping when the intent + slot bag exceeds the viewport width (it
// can rely on the lipgloss WordWrap rendering applied at the recent-
// turns view layer).
func (c RoutingChip) renderResolved() string {
	switch c.current {
	case TierCancelled:
		return c.styled(TierCancelled, "[✕ cancelled]")
	case TierAmbiguous:
		return c.renderAmbiguous()
	}

	// "Standard" tier resolution: faded prior misses, then the current
	// tier's icon + intent + optional slot bag + detail parens.
	var sb strings.Builder
	sb.WriteString("[")
	for _, t := range c.missed {
		sb.WriteString(c.fadedIcon(t))
		sb.WriteString("· ")
	}
	sb.WriteString(c.styled(c.current, tierIcon(c.current)))
	sb.WriteString(" ")
	sb.WriteString(c.intent)
	if bag := c.formatSlots(); bag != "" {
		sb.WriteString(bag)
	}
	sb.WriteString("]")
	if detail := c.formatDetail(); detail != "" {
		sb.WriteString("  ")
		sb.WriteString(detail)
	}
	return sb.String()
}

// renderAmbiguous produces the inline two-way disambiguation prompt
// (§8.3). For ≥3 candidates the chip prints a "fall back to modal"
// stub — the existing disambiguation card handles the actual pick.
func (c RoutingChip) renderAmbiguous() string {
	var inner string
	switch len(c.candidates) {
	case 0:
		inner = "? ambiguous"
	case 1:
		inner = "? " + c.candidates[0]
	case 2:
		inner = "? " + c.candidates[0] + " | " + c.candidates[1]
	default:
		inner = fmt.Sprintf("? %d ways", len(c.candidates))
	}
	out := c.styled(TierAmbiguous, "["+inner+"]")
	if c.input != "" {
		out += " you: " + c.input
	}
	return out
}

// formatSlots renders the slot bag in compact form:
//
//	empty:          ""
//	single slot:    {k=v}
//	multiple slots: {k1=v1, k2=v2}                  (wrap deferred to caller)
//
// String values truncate at slotMaxStringRuneLen runes with `…`.
// Sort by key for deterministic output (essential for golden tests).
func (c RoutingChip) formatSlots() string {
	if len(c.slots) == 0 {
		return ""
	}
	keys := make([]string, 0, len(c.slots))
	for k := range c.slots {
		keys = append(keys, k)
	}
	// sort.Strings would do but pull in a one-import; this is small.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	var sb strings.Builder
	sb.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(k)
		sb.WriteString("=")
		sb.WriteString(formatSlotValue(c.slots[k]))
	}
	sb.WriteString("}")
	return sb.String()
}

// formatSlotValue stringifies one slot value, truncating strings.
func formatSlotValue(v any) string {
	switch s := v.(type) {
	case string:
		runes := []rune(s)
		if len(runes) > slotMaxStringRuneLen {
			return `"` + string(runes[:slotMaxStringRuneLen]) + `…"`
		}
		return `"` + s + `"`
	case int:
		return fmt.Sprintf("%d", s)
	case int64:
		return fmt.Sprintf("%d", s)
	case float64:
		// Prefer integer form when it's exact (money slots round-trip
		// as float64 from JSON).
		if s == float64(int64(s)) {
			return fmt.Sprintf("%d", int64(s))
		}
		return fmt.Sprintf("%g", s)
	case bool:
		return fmt.Sprintf("%t", s)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// formatDetail produces the parenthesised detail trailer (§8 examples).
// Different tiers use different shapes:
//
//	synonym/template: (synonym:wade · 0.90)
//	cache:            (cache · 0.92 · 14 hits)
//	llm:              (claude-haiku · 0.81 · 2.4s)
//	deterministic:    (no trailer — always 1.00, signal is redundant)
//	offpath:          (no trailer — not routed)
func (c RoutingChip) formatDetail() string {
	switch c.current {
	case TierDeterministic, TierOffpath, TierCancelled, TierAmbiguous, TierNone:
		return ""
	}
	var parts []string
	if c.reason != "" {
		parts = append(parts, c.reason)
	}
	if c.confidence > 0 {
		parts = append(parts, fmt.Sprintf("%.2f", c.confidence))
	}
	if c.current == TierTurncache && c.hits > 0 {
		parts = append(parts, fmt.Sprintf("%d hits", c.hits))
	}
	if c.current == TierLLM && c.latency > 0 {
		parts = append(parts, formatLatency(c.latency))
	}
	if len(parts) == 0 {
		return ""
	}
	return "(" + strings.Join(parts, " · ") + ")"
}

// formatLatency pretty-prints a duration in the style §8 mocks use
// ("2.4s", "850ms"). Seconds at 1 decimal place; sub-second in ms.
func formatLatency(d time.Duration) string {
	if d >= time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}

// fadedIcon renders a tier icon with Faint(true) styling so the
// progressive trail visually de-emphasises the missed tiers (§8.1).
func (c RoutingChip) fadedIcon(t RoutingTier) string {
	if c.noColour {
		return tierIcon(t)
	}
	return lipgloss.NewStyle().
		Foreground(tierColour(t)).
		Faint(true).
		Render(tierIcon(t))
}

// styled renders s in the tier's foreground colour, or leaves it
// untouched when NO_COLOR is set. Centralised so every tier render
// goes through the same gate.
func (c RoutingChip) styled(t RoutingTier, s string) string {
	if c.noColour {
		return s
	}
	return lipgloss.NewStyle().Foreground(tierColour(t)).Render(s)
}
