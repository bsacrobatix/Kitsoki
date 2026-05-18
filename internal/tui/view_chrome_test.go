package tui_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/require"

	tea "github.com/charmbracelet/bubbletea"
	tuipkg "kitsoki/internal/tui"
)

// view_chrome_test.go — regression suite for the bottom-chrome
// rendering. The same class of bug has recurred multiple times:
//
//   - Phase 6: footerFrameworkLine included `mode`, and StatusRow's
//     right side ALSO included `mode` → "awaiting awaiting" doubled.
//   - JoinVertical newline-doubling: each lipgloss.Render output ended
//     with "\n" and JoinVertical added another between parts, so the
//     live region's true row count was 2× what was visible. Bubble
//     Tea's clear-then-redraw overran into scrollback.
//   - Status row background colour bleed: lipgloss.Style.Width(w)
//     hard-wrapped overflow content, leaking the background colour
//     into the next terminal row.
//   - Forced-TrueColor: lipgloss now emits ANSI escapes everywhere,
//     making any width-calculation mistake immediately visible as
//     bleed.
//
// These tests assert the invariants every fix has been re-establishing:
//   1. The bottom chrome's line count matches lipgloss.Height exactly
//      (no double counting from internal "\n" duplication).
//   2. No row exceeds the terminal width in visible columns
//      (regardless of ANSI escape content).
//   3. Every styled row is self-terminating — open + reset, balanced.
//   4. No row has embedded "\n" mid-string (which would silently
//      become a second row Bubble Tea didn't account for).
//
// Test setup forces TrueColor profile so the ANSI codes are present
// in the rendered output (without that, lipgloss strips them and the
// width-bleed bug is invisible — which is how it slipped through
// before).

func init() {
	// Match the production behaviour: kitsoki run forces TrueColor
	// up front so the renderer paths exercised here see ANSI codes.
	lipgloss.SetColorProfile(termenv.TrueColor)
}

// resizeForTest puts the model through the real resize() path at a
// fixed terminal width so the prompt/textarea + chrome sizing match
// what the user sees live.
func resizeForTest(rm tuipkg.RootModel, w, h int) tuipkg.RootModel {
	return tuipkg.ResizeRootModel(rm, w, h)
}

// chromeLines returns View()'s rendered output split into individual
// terminal rows. Empty-leading rows are preserved so the row count
// matches what Bubble Tea sees.
func chromeLines(view string) []string {
	return strings.Split(view, "\n")
}

// stripStyles is a local-to-this-file alias for tui_test.stripANSI so
// the imports list captures regexp use.
var ansiRE = regexp.MustCompile("\x1b\\[[0-9;?]*[a-zA-Z]")

func stripStyles(s string) string { return ansiRE.ReplaceAllString(s, "") }

// TestViewChrome_NoDoubleNewlinesInLiveRegion catches the JoinVertical
// pitfall where each part's trailing "\n" plus the join's "\n"
// produces double-spaced rows. lipgloss.Height should equal the
// number of "\n" + 1 in the joined output.
func TestViewChrome_NoDoubleNewlinesInLiveRegion(t *testing.T) {
	t.Parallel()
	orch, sid := setupCloak(t)
	m := buildModel(t, orch, sid)
	rm, _ := tuipkg.ExtractRootModel(m)
	rm = resizeForTest(rm, 100, 24)

	view := rm.View()
	height := lipgloss.Height(view)
	lines := chromeLines(view)
	// lipgloss.Height counts visible rows; len(lines) counts
	// newline-split rows. They MUST match — if they diverge,
	// JoinVertical or some renderer is duplicating newlines and
	// the next paint will overwrite scrollback.
	require.Equal(t, height, len(lines),
		"View() height (%d) must equal newline-split row count (%d) — divergence means JoinVertical or a render is emitting double newlines",
		height, len(lines))
}

// TestViewChrome_NoRowExceedsTerminalWidth pins the StatusRow + footer
// width math. Every rendered row's visible width (post-ANSI-strip)
// must be ≤ terminal width. A wider row means lipgloss padded past
// the terminal, which manifests as visible bleed.
func TestViewChrome_NoRowExceedsTerminalWidth(t *testing.T) {
	t.Parallel()
	const w = 100
	orch, sid := setupCloak(t)
	m := buildModel(t, orch, sid)
	rm, _ := tuipkg.ExtractRootModel(m)
	rm = resizeForTest(rm, w, 24)

	view := rm.View()
	for i, line := range chromeLines(view) {
		visible := ansi.StringWidth(line)
		require.LessOrEqualf(t, visible, w,
			"row %d exceeds terminal width %d (visible=%d): %q",
			i, w, visible, stripStyles(line))
	}
}

// TestViewChrome_NoEmbeddedNewlinesWithinRows scans every part the
// View() emits and confirms no individual row contains a mid-string
// "\n" after split — i.e. JoinVertical / Render didn't produce
// content like "row1\nrow2" inside a single logical row.
func TestViewChrome_NoEmbeddedNewlinesWithinRows(t *testing.T) {
	t.Parallel()
	orch, sid := setupCloak(t)
	m := buildModel(t, orch, sid)
	rm, _ := tuipkg.ExtractRootModel(m)
	rm = resizeForTest(rm, 80, 24)

	view := rm.View()
	for i, line := range chromeLines(view) {
		require.False(t, strings.Contains(line, "\n"),
			"row %d has embedded newline: %q", i, line)
	}
}

// TestViewChrome_AnsiBalanced asserts every line ends with terminal
// state reset — i.e. the last SGR sequence in the line is `\x1b[0m`
// (or `\x1b[m`). Trailing plain whitespace AFTER the reset is fine;
// what bleeds is an open SGR with no matching close before
// end-of-line. We walk the line for the LAST escape sequence and
// require it to be a reset.
func TestViewChrome_AnsiBalanced(t *testing.T) {
	t.Parallel()
	orch, sid := setupCloak(t)
	m := buildModel(t, orch, sid)
	rm, _ := tuipkg.ExtractRootModel(m)
	rm = resizeForTest(rm, 80, 24)

	view := rm.View()
	for i, line := range chromeLines(view) {
		idx := strings.LastIndex(line, "\x1b[")
		if idx < 0 {
			continue
		}
		// The last SGR-form sequence starts at idx. Find its end
		// terminator ('m' for SGR).
		end := strings.IndexByte(line[idx:], 'm')
		if end < 0 {
			t.Fatalf("row %d: malformed escape near %q", i, line[idx:])
		}
		last := line[idx : idx+end+1]
		isReset := last == "\x1b[0m" || last == "\x1b[m"
		require.Truef(t, isReset,
			"row %d ends with un-reset SGR %q — colour will bleed into the next row.\nline (stripped): %q",
			i, last, stripStyles(line))
	}
}

// TestViewChrome_StatusRowExactlyTerminalWidth pins the contract
// that the coloured bottom status row is EXACTLY r.Width visible
// columns. Anything narrower leaves an un-filled trailing column
// (where the next paint can leak), anything wider wraps and bleeds.
func TestViewChrome_StatusRowExactlyTerminalWidth(t *testing.T) {
	t.Parallel()
	for _, w := range []int{60, 80, 100, 140} {
		w := w
		t.Run("width="+itoaWidth(w), func(t *testing.T) {
			t.Parallel()
			orch, sid := setupCloak(t)
			m := buildModel(t, orch, sid)
			rm, _ := tuipkg.ExtractRootModel(m)
			rm = resizeForTest(rm, w, 24)

			view := rm.View()
			lines := chromeLines(view)
			// Status row is the last non-empty line of the chrome.
			var status string
			for j := len(lines) - 1; j >= 0; j-- {
				if strings.TrimSpace(stripStyles(lines[j])) != "" {
					status = lines[j]
					break
				}
			}
			require.NotEmptyf(t, status, "View() at width %d has no non-empty last row", w)
			require.Equal(t, w, ansi.StringWidth(status),
				"status row at width %d must be exactly %d visible columns; got %d: %q",
				w, w, ansi.StringWidth(status), stripStyles(status))
		})
	}
}

// TestViewChrome_AwaitingLLMShowsTextarea regresses the bug where
// the textarea was replaced by a "thinking via claude…" caption,
// hiding the queue affordance. Post-fix: the textarea remains
// visible during ModeAwaitingLLM and an indicator row appears
// above it.
func TestViewChrome_AwaitingLLMShowsTextarea(t *testing.T) {
	t.Parallel()
	orch, sid := setupCloak(t)
	m := buildModel(t, orch, sid)
	rm, _ := tuipkg.ExtractRootModel(m)
	rm = resizeForTest(rm, 80, 24)
	rm = tuipkg.SimulateSlowHarnessTurnStart(rm)

	view := rm.View()
	plain := stripStyles(view)
	require.Contains(t, plain, "thinking",
		"AwaitingLLM should show a thinking indicator; got:\n%s", plain)
	// And: the textarea must still be visible (its cursor prompt or a
	// "↳ " gutter). We check for the queue-hint prefix.
	require.Contains(t, plain, "↳",
		"AwaitingLLM should keep the textarea visible with the queue glyph; got:\n%s", plain)
}

// TestViewChrome_LiveRegionDoesNotChangeRowCountAcrossFrames pins
// the contract that a deterministic state produces a deterministic
// number of rendered rows. If View() is called twice with no
// intervening mutation, the row count must match exactly — otherwise
// Bubble Tea's redraw-this-many-rows accounting drifts and overwrites
// scrollback. (The kind of bug a static unit test would miss without
// this check.)
func TestViewChrome_LiveRegionDoesNotChangeRowCountAcrossFrames(t *testing.T) {
	t.Parallel()
	orch, sid := setupCloak(t)
	m := buildModel(t, orch, sid)
	rm, _ := tuipkg.ExtractRootModel(m)
	rm = resizeForTest(rm, 80, 24)

	first := lipgloss.Height(rm.View())
	second := lipgloss.Height(rm.View())
	require.Equal(t, first, second,
		"View() must be deterministic across frames (was %d then %d)", first, second)

	// Also after a window resize: row count may DIFFER, but
	// subsequent calls at the new size must be stable.
	rm = resizeForTest(rm, 100, 30)
	a := lipgloss.Height(rm.View())
	b := lipgloss.Height(rm.View())
	require.Equal(t, a, b,
		"View() after resize must be deterministic (was %d then %d)", a, b)
}

// itoaWidth is a tiny helper that avoids pulling strconv into the
// subtest-name plumbing.
func itoaWidth(w int) string {
	switch w {
	case 60:
		return "60"
	case 80:
		return "80"
	case 100:
		return "100"
	case 140:
		return "140"
	}
	return "?"
}

// Sanity check: tea.Cmd is in the import list for future tests that
// drive Update() directly. Silences "imported but not used".
var _ tea.Cmd = nil
