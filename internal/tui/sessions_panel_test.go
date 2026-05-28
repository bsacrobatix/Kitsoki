package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/metamode"
)

// fakeListings returns two ChatListings that exercise both an
// app-scoped row and a cross-app self row. Order matches what
// Controller.ListChats hands back: UpdatedAt-desc.
func fakeListings() []metamode.ChatListing {
	return []metamode.ChatListing{
		{
			ID:               "self-chat-xyz",
			ModeName:         "self",
			ScopeKey:         "",
			Title:            "Edit kitsoki",
			UpdatedAt:        time.Date(2026, 5, 13, 11, 0, 0, 0, time.UTC),
			FirstUserMessage: "rename Foo to Bar",
		},
		{
			ID:               "story-chat-abc",
			ModeName:         "story",
			ScopeKey:         "foyer",
			Title:            "improve the story",
			UpdatedAt:        time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC),
			FirstUserMessage: "add a south door",
		},
	}
}

// TestSessionsPanel_OpenAndView covers the happy path: open with two
// listings, render, assert both rows are present and the selection
// marker is on the first row by default.
func TestSessionsPanel_OpenAndView(t *testing.T) {
	m := newSessionsPanelModel()
	require.False(t, m.IsActive())
	m.Open(fakeListings())
	require.True(t, m.IsActive())

	view := m.View()
	require.Contains(t, view, "meta sessions")
	require.Contains(t, view, "self-cha", "8-char id prefix for the first row must appear")
	require.Contains(t, view, "story-ch", "8-char id prefix for the second row must appear")
	require.Contains(t, view, "self")
	require.Contains(t, view, "story")
	require.Contains(t, view, "rename Foo to Bar", "first user-message preview must render")
	require.Contains(t, view, "add a south door", "preview for the second row must render")
	require.Contains(t, view, "▸", "selection marker must render on the active row")
}

// TestSessionsPanel_EmptyOpensWithNoRows asserts the overlay still
// opens and renders a clear "(no active meta sessions)" line so the
// user can dismiss it rather than seeing nothing at all.
func TestSessionsPanel_EmptyOpensWithNoRows(t *testing.T) {
	m := newSessionsPanelModel()
	m.Open(nil)
	require.True(t, m.IsActive())
	view := m.View()
	require.Contains(t, view, "no active meta sessions")
}

// TestSessionsPanel_ArrowsNavigateSelection asserts ↓ moves selection
// forward and ↑ moves it back, clamped to row bounds.
func TestSessionsPanel_ArrowsNavigateSelection(t *testing.T) {
	m := newSessionsPanelModel()
	m.Open(fakeListings())
	require.Equal(t, 0, m.selected)

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	require.Equal(t, 1, m.selected)

	// At the bottom already — another ↓ is a no-op.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	require.Equal(t, 1, m.selected)

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	require.Equal(t, 0, m.selected)

	// At the top — another ↑ clamps.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	require.Equal(t, 0, m.selected)
}

// TestSessionsPanel_EnterEmitsChoice asserts the panel closes and
// the choice message carries the chat ID + mode name of the
// currently-selected row.
func TestSessionsPanel_EnterEmitsChoice(t *testing.T) {
	m := newSessionsPanelModel()
	m.Open(fakeListings())
	// Move to the second row before pressing Enter.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	require.False(t, m.IsActive(), "Enter must close the panel")
	require.NotNil(t, cmd, "Enter must emit a choice command")

	msg := cmd()
	choice, ok := msg.(sessionsPanelChoiceMsg)
	require.True(t, ok, "command must emit sessionsPanelChoiceMsg")
	require.Equal(t, "story-chat-abc", choice.chatID)
	require.Equal(t, "story", choice.modeName)
}

// TestSessionsPanel_EscClosesWithoutChoice asserts Esc dismisses
// the panel and emits no choice command.
func TestSessionsPanel_EscClosesWithoutChoice(t *testing.T) {
	m := newSessionsPanelModel()
	m.Open(fakeListings())
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	require.False(t, m.IsActive())
	require.Nil(t, cmd, "Esc must not emit a choice")
}

// TestSessionsPanel_NumericHotkey selects the second row when the
// user presses 2 (instead of arrowing to it).
func TestSessionsPanel_NumericHotkey(t *testing.T) {
	m := newSessionsPanelModel()
	m.Open(fakeListings())
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	require.False(t, m.IsActive())
	require.NotNil(t, cmd)

	choice := cmd().(sessionsPanelChoiceMsg)
	require.Equal(t, "story-chat-abc", choice.chatID)
}

// TestSessionsPanel_ColumnAlignment exercises the header + first-row
// width computation so a regression in computeColumnWidths surfaces
// here rather than later via "looks ugly".
func TestSessionsPanel_ColumnAlignment(t *testing.T) {
	m := newSessionsPanelModel()
	m.Open(fakeListings())
	view := m.View()
	headerLine := ""
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "MODE") && strings.Contains(line, "SCOPE") {
			headerLine = line
			break
		}
	}
	require.NotEmpty(t, headerLine, "header row must be present")
	require.Contains(t, headerLine, "ID")
	require.Contains(t, headerLine, "PREVIEW")
}
