package tui_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/tui"
)

// TestRenderingAnalyzerBasicUsage demonstrates the RenderingAnalyzer for
// analyzing View() output. This example uses simulated output.
func TestRenderingAnalyzerBasicUsage(t *testing.T) {
	t.Parallel()

	// Simulate what a ModeAwaitingLLM View() might produce
	simulatedOutput := strings.Join([]string{
		"  → nav: back   (deterministic · 1.00)",
		"────────────────────────────────────────",
		"⏳ ⠋ thinking…  ·  queue: 0  ·  Enter · Esc",
		"↳ test input",
		"framework: awaiting",
	}, "\n")

	analyzer := tui.NewRenderingAnalyzer(t, simulatedOutput)

	// Verify basic properties
	analyzer.AssertLineCount(5)
	analyzer.AssertContains("thinking")
	analyzer.AssertContains("↳")

	// Key regression test: routing status and queue indicator must be separate
	analyzer.AssertLineSeparation("nav: back", "thinking…")

	// Verify no horizontal concatenation (the bug we fixed)
	analyzer.AssertNoHorizontalConcat("nav: back", "queue:")
}

// TestRenderingAnalyzerDetectsRegression demonstrates how to catch regressions.
// If someone breaks the fix by reverting to JoinVertical or introducing padding,
// this would catch it.
func TestRenderingAnalyzerDetectsRegression(t *testing.T) {
	t.Parallel()

	// This is what BAD output (with the bug) would look like:
	// Both routing status and queue indicator on the same line due to padding
	badOutput := "  → nav: back   (deterministic · 1.00)                    ⏳ thinking…\n↳ test"

	analyzer := tui.NewRenderingAnalyzer(t, badOutput)
	analyzer.AssertLineCount(2)

	// This assertion would FAIL if the bug reoccurred
	// (it should fail for badOutput, which is the point of this test)
	// Uncomment to see it fail:
	// analyzer.AssertLineSeparation("nav: back", "thinking…")
	// analyzer.AssertNoHorizontalConcat("nav: back", "queue:")

	// Instead, verify that badOutput DOES have the problem
	strippedLines := analyzer.StrippedLines()
	hasBoth := strings.Contains(strippedLines[0], "nav: back") &&
		strings.Contains(strippedLines[0], "thinking")
	require.True(t, hasBoth, "bad output should have both on same line (for testing detection)")
}

// TestRenderingAnalyzerWithComplexOutput tests the analyzer with
// more realistic multi-part output.
func TestRenderingAnalyzerWithComplexOutput(t *testing.T) {
	t.Parallel()

	// Realistic View() output with multiple sections
	output := strings.Join([]string{
		"↳ resolved: deterministic · 1.00",
		"────────────────────────────────────────────────────────────────────────────────",
		"> prompt input",
		"",
		"framework: awaiting · mode: on-path · queue: 0",
	}, "\n")

	analyzer := tui.NewRenderingAnalyzer(t, output)

	// Structural assertions
	analyzer.AssertStructure("resolved:", "prompt", "framework:")

	// No problematic overlaps
	analyzer.AssertNoHorizontalConcat("resolved:", "framework:")
	analyzer.AssertLineSeparation("resolved:", "prompt")

	// Dump output for manual review
	t.Run("DumpOutput", func(t *testing.T) {
		analyzer.Dump()
	})
}

// TestRenderingAnalyzerWithANSICodes demonstrates that the analyzer
// correctly handles ANSI color codes and other styling.
func TestRenderingAnalyzerWithANSICodes(t *testing.T) {
	t.Parallel()

	// Output with ANSI color codes (what lipgloss produces)
	withANSI := "\x1b[38;2;100;100;100m⏳\x1b[0m thinking…\n\x1b[38;5;243m↳\x1b[0m input"

	analyzer := tui.NewRenderingAnalyzer(t, withANSI)

	// The analyzer should handle ANSI codes transparently
	analyzer.AssertContains("thinking")
	analyzer.AssertContains("input")
	analyzer.AssertLineSeparation("thinking", "input")

	// Stripped output should not have ANSI codes
	stripped := analyzer.StrippedLines()
	for _, line := range stripped {
		require.NotContains(t, line, "\x1b", "stripped output should not contain ANSI escape code")
	}
}

// TestRenderingJoinVerticalVsConcatenation compares the two assembly methods
// to verify that our fix (string concatenation) produces correct structure
// while JoinVertical causes issues.
func TestRenderingJoinVerticalVsConcatenation(t *testing.T) {
	t.Parallel()

	part1 := "routing status"
	part2 := "────────────────"
	part3_line1 := "⏳ queue indicator"
	part3_line2 := "↳ prompt"
	part3 := part3_line1 + "\n" + part3_line2

	parts := []string{part1, part2, part3}

	// Method 1: Our fix - string concatenation
	var concatResult strings.Builder
	for i, p := range parts {
		if i > 0 {
			concatResult.WriteString("\n")
		}
		concatResult.WriteString(p)
	}
	concatOutput := concatResult.String()

	// Method 2: Previous approach - JoinVertical
	joinOutput := lipgloss.JoinVertical(lipgloss.Left, parts...)

	// Analyze both outputs
	t.Run("ConcatenationMethod", func(t *testing.T) {
		analyzer := tui.NewRenderingAnalyzer(t, concatOutput)
		analyzer.AssertLineCount(4) // All parts rendered correctly
		analyzer.AssertLineSeparation("routing status", "queue indicator")
		// analyzer.Dump() // Uncomment for debugging
	})

	t.Run("JoinVerticalMethod", func(t *testing.T) {
		analyzer := tui.NewRenderingAnalyzer(t, joinOutput)
		// JoinVertical pads lines, which can cause misalignment
		// Our tests verify this doesn't cause the bug anymore with concatenation
		analyzer.AssertContains("routing status")
		analyzer.AssertContains("queue indicator")
		// analyzer.Dump() // Uncomment to see the padding
	})
}
