package elements

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"kitsoki/internal/expr"
)

// Banner is a phase-marker block. Renders as a 3-line strip — a top
// divider, a centred title line, a bottom divider — all in the
// supplied Color. Compact enough to scan past, distinct enough that an
// operator skimming a transcript top-to-bottom finds phase boundaries
// at a glance.
//
// We previously rendered the phase name as figlet-style ASCII art via
// go-figure, but glamour's markdown layer (run over the full composite
// in the TUI's typed-extends path) reinterprets the figlet output —
// backslashes start an escape, underscores become italic, pipe-runs
// become table separators — and the art arrives at the terminal
// corrupted. A plain text line bracketed by box-drawing dividers
// survives glamour because U+2550 ("═") isn't a markdown token.
type Banner struct {
	// Source is the phase title that goes in the centre line. Required.
	Source string
	// Subtitle is the optional caption appended after a "·" separator
	// (e.g. "Phase 1 / 7  ·  verify the bug; produce reproduction
	// artifact"). Folds into the same line as the title so a phase
	// banner stays compact at 3 lines total.
	Subtitle string
	// Color is an optional CSS-style hex foreground for the whole
	// strip. Defaults to bannerDefaultColor when empty.
	Color string
}

// bannerDefaultColor is the foreground used when the author leaves
// `color:` unset. Emerald — matches the heading element so a
// banner-less older room still reads as the same visual family.
const bannerDefaultColor = "#10B981"

// bannerDividerRune is the character used for the top + bottom strips.
// U+2550 (BOX DRAWINGS DOUBLE HORIZONTAL) is heavier than a single
// dash so the banner reads as a "title plate" rather than a casual
// separator. Not a markdown token: glamour passes it through
// untouched.
const bannerDividerRune = '═'

// bannerLeftPad is the indent applied to the title line so it doesn't
// sit flush against the left edge.
const bannerLeftPad = "  "

// bannerSeparator is the dot inserted between Source and Subtitle.
// Pre/post-padded with two spaces so the line reads as three beats
// (title · phase counter · description) without crowding.
const bannerSeparator = "  ·  "

// bannerMinWidth floors the divider length so the banner stays
// readable in narrow viewports (e.g. a split-pane TUI at 50 cols).
const bannerMinWidth = 40

// Render builds the 3-line banner: divider, title (source + optional
// subtitle), divider. The whole strip is wrapped in lipgloss with
// Color so the eye lands on it immediately.
//
// Width arg sizes the divider — the dividers fit the title line plus a
// small overflow, capped to the viewport so they never wrap. The
// caller's width is the dispatcher width; the TUI passes its actual
// viewport, the orchestrator passes blockRenderWidth=80.
func (b Banner) Render(width int, env expr.Env, rr ViewRenderer) (string, error) {
	source, err := renderLeaf(rr, b.Source, env)
	if err != nil {
		return "", err
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return "", nil
	}
	subtitle, err := renderLeaf(rr, b.Subtitle, env)
	if err != nil {
		return "", err
	}
	subtitle = strings.TrimSpace(subtitle)

	title := bannerLeftPad + source
	if subtitle != "" {
		title += bannerSeparator + subtitle
	}

	// Divider matches the title's visible width plus a 2-char trailing
	// margin so the strip extends just past the text on each side. Capped
	// to width so the strip never wraps; floored to bannerMinWidth so a
	// short title still reads as a banner rather than a stub.
	titleRunes := len([]rune(title))
	divWidth := titleRunes + 2
	if divWidth > width && width >= bannerMinWidth {
		divWidth = width
	}
	if divWidth < bannerMinWidth {
		divWidth = bannerMinWidth
	}
	divider := strings.Repeat(string(bannerDividerRune), divWidth)

	colour := b.Color
	if colour == "" {
		colour = bannerDefaultColor
	}
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(colour))

	return style.Render(divider) + "\n" + style.Render(title) + "\n" + style.Render(divider), nil
}
