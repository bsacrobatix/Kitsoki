package tui_test

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/machine"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/store"
	tuipkg "kitsoki/internal/tui"
)

// TestDevStoryLandingQuickActionsFitTerminalWidth reproduces bug 64: the
// dev-story landing room emits over-wide quick-action rows, so a normal terminal
// wraps hints at arbitrary columns (for example the "prd" row splits "author a
// PRD, then..." awkwardly on a 60-column TUI).
func TestDevStoryLandingQuickActionsFitTerminalWidth(t *testing.T) {
	const terminalWidth = 60

	def, err := app.Load("../../stories/dev-story/app.yaml")
	require.NoError(t, err)
	mach, err := machine.New(def)
	require.NoError(t, err)
	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	orch := orchestrator.New(def, mach, s, nil)
	sid, err := orch.NewSession(context.Background())
	require.NoError(t, err)

	initialText, typed, env, rr, err := orch.InitialViewTyped(orch.InitialWorld())
	require.NoError(t, err)
	root := tuipkg.NewRootModel(
		orch,
		sid,
		"../../stories/dev-story/app.yaml",
		initialText,
		tuipkg.WithInitialTypedView(typed, env, rr),
	)
	model, _ := root.Update(tea.WindowSizeMsg{Width: terminalWidth, Height: 30})
	rm, ok := tuipkg.ExtractRootModel(model)
	require.True(t, ok)

	frame := tuipkg.ComposeFrame(&rm, terminalWidth, 30)
	overWide := quickActionRowsWiderThan(frame.Text, terminalWidth)
	require.Empty(t, overWide,
		"dev-story landing quick-action rows should fit the terminal width so the terminal does not wrap hints awkwardly; frame:\n%s",
		frame.Text)
}

func quickActionRowsWiderThan(frame string, terminalWidth int) []string {
	quickActionLabels := []string{
		"tickets", "drive", "bugfix", "implement", "cypilot", "git",
		"✗ pr (no open PR yet)", "code review", "demo video", "prd",
		"idea", "model eval", "onboard", "inbox", "look",
	}
	var out []string
	foundPRD := false
	for _, line := range strings.Split(frame, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		trimmed = strings.TrimPrefix(trimmed, "▸ ")
		for _, label := range quickActionLabels {
			if trimmed == label || strings.HasPrefix(trimmed, label+"  ") {
				if label == "prd" {
					foundPRD = true
				}
				if w := ansi.StringWidth(line); w > terminalWidth {
					out = append(out, line)
				}
				break
			}
		}
	}
	if !foundPRD {
		out = append(out, "prd quick action row was not rendered")
	}
	return out
}
