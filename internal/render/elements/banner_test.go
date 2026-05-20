package elements

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"kitsoki/internal/app"
	"kitsoki/internal/expr"
)

// stripANSI removes ANSI SGR escapes so tests can assert on visible
// content without knowing the active colour profile.
func stripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < '@' || s[j] > '~') {
				j++
			}
			if j < len(s) {
				i = j
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func init() {
	// Pin lipgloss to TrueColor so the styled-output tests get a stable
	// ANSI envelope across run environments (CI sometimes detects no TTY
	// and Render returns plain bytes; this keeps the colour tests
	// reproducible). Mirrors the helper in internal/tui/view_chrome_test.go.
	lipgloss.SetColorProfile(termenv.TrueColor)
}

// TestBanner_RendersThreeLines covers the happy path: divider, title,
// divider — three lines total, no figlet art, no markdown reformatting
// hazards.
func TestBanner_RendersThreeLines(t *testing.T) {
	out, err := Banner{Source: "REPRODUCING"}.Render(80, expr.Env{}, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	plain := stripANSI(out)
	lines := strings.Split(plain, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d:\n%s", len(lines), plain)
	}
	// Top and bottom are pure dividers.
	for _, idx := range []int{0, 2} {
		line := lines[idx]
		if line == "" {
			t.Errorf("divider line %d is empty", idx)
		}
		for _, r := range line {
			if r != bannerDividerRune {
				t.Errorf("divider line %d contains non-divider rune %q: %q", idx, r, line)
				break
			}
		}
	}
	// Middle carries the title (with the left-pad).
	if !strings.Contains(lines[1], "REPRODUCING") {
		t.Errorf("middle line missing source text: %q", lines[1])
	}
	if !strings.HasPrefix(lines[1], bannerLeftPad) {
		t.Errorf("title should start with leftPad %q; got: %q", bannerLeftPad, lines[1])
	}
}

// TestBanner_AppliesColor asserts the Color field flows through to
// lipgloss: a known hex value lands in the output as a TrueColor SGR
// sequence.
func TestBanner_AppliesColor(t *testing.T) {
	out, err := Banner{Source: "REPRODUCING", Color: "#06B6D4"}.Render(80, expr.Env{}, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// 0x06=6, 0xB6=182, 0xD4≈212. Anchor on R+G; lipgloss/termenv may
	// quantize the B channel by ±1.
	if !strings.Contains(out, "38;2;6;182;") {
		t.Errorf("expected #06B6D4 TrueColor escape in output; got: %q", out)
	}
}

// TestBanner_DefaultColorWhenColorEmpty asserts the bannerDefaultColor
// fallback fires when Color is unset.
func TestBanner_DefaultColorWhenColorEmpty(t *testing.T) {
	out, err := Banner{Source: "DONE"}.Render(80, expr.Env{}, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// bannerDefaultColor = #10B981 → 16, 185, 129. Anchor on R+G.
	if !strings.Contains(out, "38;2;16;185;") {
		t.Errorf("expected default colour escape; got: %q", out)
	}
}

// TestBanner_AppendsSubtitleInline asserts the subtitle folds onto the
// title line with the bannerSeparator rather than its own line —
// keeps the strip at 3 lines total.
func TestBanner_AppendsSubtitleInline(t *testing.T) {
	out, err := Banner{
		Source:   "DONE",
		Subtitle: "Phase 7 / 7  ·  close-out artifact",
	}.Render(80, expr.Env{}, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	plain := stripANSI(out)
	lines := strings.Split(plain, "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 lines with subtitle inline; got %d:\n%s", len(lines), plain)
	}
	if !strings.Contains(lines[1], "DONE") || !strings.Contains(lines[1], "Phase 7 / 7") {
		t.Errorf("title line should carry both source and subtitle:\n%q", lines[1])
	}
	if !strings.Contains(lines[1], bannerSeparator) {
		t.Errorf("title line should join source + subtitle with %q; got: %q", bannerSeparator, lines[1])
	}
}

// TestBanner_DividerCapsToWidth asserts the divider extends to (at
// most) the dispatcher's width so a long title doesn't push the strip
// past the viewport into a wrap.
func TestBanner_DividerCapsToWidth(t *testing.T) {
	out, err := Banner{
		Source:   "VALIDATING",
		Subtitle: "Phase 6 / 7  ·  full-environment validation of the fix",
	}.Render(80, expr.Env{}, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	plain := stripANSI(out)
	for _, line := range strings.Split(plain, "\n") {
		if w := len([]rune(line)); w > 80 {
			t.Errorf("line wider than dispatcher width 80 (got %d): %q", w, line)
		}
	}
}

// TestBanner_DividerFloorAtMinWidth asserts a very narrow dispatcher
// still gets a banner readable enough to skim.
func TestBanner_DividerFloorAtMinWidth(t *testing.T) {
	out, err := Banner{Source: "X"}.Render(10, expr.Env{}, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	plain := stripANSI(out)
	lines := strings.Split(plain, "\n")
	// Floor is bannerMinWidth — confirm the divider is at least that wide.
	if w := len([]rune(lines[0])); w < bannerMinWidth {
		t.Errorf("divider %d wide; expected ≥ bannerMinWidth (%d)", w, bannerMinWidth)
	}
}

// TestBanner_EmptyTextRendersEmpty asserts the dispatcher contract:
// an empty source skips the element entirely (so a guard'd-off banner
// doesn't leak whitespace).
func TestBanner_EmptyTextRendersEmpty(t *testing.T) {
	out, err := Banner{Source: ""}.Render(80, expr.Env{}, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if out != "" {
		t.Errorf("empty text should render empty, got %q", out)
	}
}

// TestBanner_DispatchedFromTypedView is the integration test: a View
// with a banner element flows through RenderAll and lands as a 3-line
// strip in the composed output.
func TestBanner_DispatchedFromTypedView(t *testing.T) {
	view := app.View{
		Elements: []app.ViewElement{
			{
				Kind:     "banner",
				Source:   "TESTING",
				Subtitle: "Phase 4 / 7",
				Color:    "#F59E0B",
			},
			{Kind: "prose", Source: "body text"},
		},
	}
	out, err := RenderAll(view, expr.Env{}, 80, IdentityGlamour, nil)
	if err != nil {
		t.Fatalf("RenderAll: %v", err)
	}
	plain := stripANSI(out)
	if !strings.Contains(plain, "TESTING") {
		t.Errorf("source missing from dispatcher output:\n%s", plain)
	}
	if !strings.Contains(plain, "Phase 4 / 7") {
		t.Errorf("subtitle missing from dispatcher output:\n%s", plain)
	}
	if !strings.Contains(plain, "body text") {
		t.Errorf("subsequent prose element missing from output:\n%s", plain)
	}
	// Amber colour escape present.
	if !strings.Contains(out, "38;2;245;158;") {
		t.Errorf("expected #F59E0B amber escape in dispatched output")
	}
}
