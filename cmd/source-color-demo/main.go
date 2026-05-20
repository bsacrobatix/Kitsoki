// Source-color demo: renders hand-crafted samples that mix templated
// and LLM-generated text, switching terminal background color at the
// boundary so the two are visually distinguishable.
//
// Run:
//
//	go run ./cmd/source-color-demo
//	go run ./cmd/source-color-demo -theme=high-contrast
//	go run ./cmd/source-color-demo -theme=light
//	go run ./cmd/source-color-demo -all
//	go run ./cmd/source-color-demo -fill-template
//
// This is a *static* preview to align on the visual scheme before the
// full pipeline (operator-side sentinel wrapping + render post-pass)
// is wired into the TUI. See docs/story-style.md §8.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// Sentinels mark LLM-sourced runs. Zero-width space + invisible
// separator + ascii tag keeps them invisible in any normal renderer
// and vanishingly unlikely to collide with real text. Only the final
// colorize pass interprets them.
const (
	llmOpen  = "​⁣LLM⁣"
	llmClose = "​⁣/LLM⁣"
)

// L wraps a string as LLM-sourced for the demo input.
func L(s string) string { return llmOpen + s + llmClose }

type theme struct {
	name  string
	tplBG string // ANSI background for templated text
	llmBG string // ANSI background for LLM-sourced text
	fg    string // foreground (set explicitly so light theme reads)
	reset string
}

var themes = map[string]theme{
	"dark": {
		name:  "dark",
		tplBG: "\x1b[48;2;42;53;80m", // #2a3550 — cool slate, visible vs black
		llmBG: "\x1b[48;2;92;62;40m", // #5c3e28 — warm bronze, slightly brighter
		fg:    "\x1b[38;2;232;232;232m",
		reset: "\x1b[0m",
	},
	"high-contrast": {
		name:  "high-contrast",
		tplBG: "\x1b[48;2;32;48;112m", // #203070 — saturated cool
		llmBG: "\x1b[48;2;128;72;24m", // #804818 — saturated warm
		fg:    "\x1b[38;2;255;255;255m",
		reset: "\x1b[0m",
	},
	"light": {
		name:  "light",
		tplBG: "\x1b[48;2;232;240;255m", // #e8f0ff
		llmBG: "\x1b[48;2;255;244;224m", // #fff4e0
		fg:    "\x1b[38;2;20;20;20m",
		reset: "\x1b[0m",
	},
}

func themeNames() []string {
	out := make([]string, 0, len(themes))
	for k := range themes {
		out = append(out, k)
	}
	return out
}

// colorize walks sentinel-laced text and emits ANSI bg switches.
//
// Strategy:
//   - Maintain a stack of active background escapes.
//   - On llmOpen: push warm bg, emit it.
//   - On llmClose: pop, emit the now-top bg (parent's) — never a bare
//     reset, so the outer bg is restored cleanly on exit.
//   - At end of each line: if currently inside an LLM span, pad with
//     spaces in the warm bg so the band is solid to the right margin.
//     This gives block-mode rendering for any multi-line LLM value
//     without the author having to mark it specially.
//   - Inline LLM (single line) is naturally not padded because the
//     bg has already popped back to template by line end.
//
// fillTpl optionally pads template lines too, so the entire view is a
// solid cool band. Off by default — template lines stay text-tight,
// which keeps the warm LLM band as the visually loud element.
func colorize(text string, t theme, width int, fillTpl bool) string {
	var out strings.Builder
	stack := []string{t.tplBG}

	lines := strings.Split(text, "\n")
	for li, line := range lines {
		out.WriteString(t.fg)
		out.WriteString(stack[len(stack)-1])

		visWidth := 0
		i := 0
		for i < len(line) {
			if strings.HasPrefix(line[i:], llmOpen) {
				stack = append(stack, t.llmBG)
				out.WriteString(t.llmBG)
				i += len(llmOpen)
				continue
			}
			if strings.HasPrefix(line[i:], llmClose) {
				if len(stack) > 1 {
					stack = stack[:len(stack)-1]
				}
				out.WriteString(stack[len(stack)-1])
				i += len(llmClose)
				continue
			}
			r, sz := utf8.DecodeRuneInString(line[i:])
			out.WriteRune(r)
			visWidth++
			i += sz
		}

		inLLM := len(stack) > 1
		if visWidth < width && (inLLM || fillTpl) {
			out.WriteString(strings.Repeat(" ", width-visWidth))
		}

		out.WriteString(t.reset)
		if li < len(lines)-1 {
			out.WriteByte('\n')
		}
	}
	return out.String()
}

type sample struct {
	title string
	body  string
}

func samples() []sample {
	return []sample{
		{
			"1. Pure template (cool only)",
			"Ticket #1234 — opened 3 hours ago by brad@acronis.com.\n" +
				"Severity: high.  Last activity: 12 minutes ago.",
		},
		{
			"2. Inline LLM inside template",
			"Ticket title is " + L("Database migration fails on backfill step") + ", reported in #ops.\n" +
				"Owner: " + L("ingest-team") + " — auto-assigned by the triage model.",
		},
		{
			"3. Block LLM inside template (multi-line warm band)",
			"Triage summary:\n" + L("\n"+
				"The migration creates a NOT NULL column before populating it.\n"+
				"Under concurrent writes the locking pattern blocks the backfill\n"+
				"and the deploy times out. Recommend reversing the order: add a\n"+
				"nullable column, backfill, then promote to NOT NULL once full.\n",
			) + "\n— end of summary —",
		},
		{
			"4. Nested LLM (LLM quoting earlier LLM output)",
			"The agent said " + L("I checked the prior plan, which stated: "+
				L("\"backfill before constraint\"")+" — confirmed.") +
				" So we proceed.",
		},
		{
			"5. Mixed: template scaffold with an LLM block inside a section",
			"## Incident notes\n" +
				"Date: 2026-05-20\n" +
				"Reporter: brad@acronis.com\n" +
				"\n" +
				"AI synthesis:\n" + L("\n"+
				"Root cause is the ordering of the schema change versus the\n"+
				"backfill job. The fix is two commits, not one: split the\n"+
				"migration and re-run the backfill before promoting the\n"+
				"constraint.\n",
			) + "\n" +
				"Next step: assign to the on-call.",
		},
	}
}

func render(w io.Writer, t theme, width int, fillTpl bool) {
	fmt.Fprintf(w, "\n=== theme=%s  width=%d  fill-template=%v ===\n",
		t.name, width, fillTpl)
	for _, s := range samples() {
		fmt.Fprintf(w, "\n%s\n\n", s.title)
		io.WriteString(w, colorize(s.body, t, width, fillTpl))
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w)
}

func main() {
	themeName := flag.String("theme", "dark",
		"theme: "+strings.Join(themeNames(), "|"))
	width := flag.Int("width", 72, "width for block padding (cols)")
	fillTpl := flag.Bool("fill-template", false,
		"also pad template lines (solid cool band)")
	all := flag.Bool("all", false, "render every theme back-to-back")
	flag.Parse()

	if *all {
		for _, name := range []string{"dark", "high-contrast", "light"} {
			render(os.Stdout, themes[name], *width, *fillTpl)
		}
		return
	}

	t, ok := themes[*themeName]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown theme: %s (have: %s)\n",
			*themeName, strings.Join(themeNames(), ", "))
		os.Exit(2)
	}
	render(os.Stdout, t, *width, *fillTpl)
}
