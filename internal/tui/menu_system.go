package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// menuSystemAction identifies a chosen row in the system menu.
type menuSystemAction int

const (
	menuActionNone menuSystemAction = iota
	menuActionExit
	menuActionReportBug
	// menuActionMetaStory opens the first declared meta mode (default
	// = /meta with no name). Present when the AppDef declares any
	// meta_modes; otherwise the row is omitted entirely.
	menuActionMetaStory
)

// menuSystemChoiceMsg is emitted when the user selects a row.
type menuSystemChoiceMsg struct {
	action menuSystemAction
}

// menuSystemEntry describes one row in the overlay.
type menuSystemEntry struct {
	action menuSystemAction
	label  string
	hint   string
}

// menuSystemModel is the Esc-activated overlay that exposes session-level
// actions (exit, report bug, and any declared meta mode). It follows
// the same Open/Close + Update/View shape as disambiguationModel.
type menuSystemModel struct {
	active   bool
	entries  []menuSystemEntry
	selected int
}

// newMenuSystemModel builds the overlay's static entry list. metaLabel
// is the human-readable label for the meta-mode row; pass "" to omit
// the row (when the AppDef declares no meta_modes).
func newMenuSystemModel(metaLabel string) menuSystemModel {
	entries := []menuSystemEntry{
		{action: menuActionExit, label: "Exit", hint: "quit this session"},
		{action: menuActionReportBug, label: "Report bug", hint: "coming soon"},
	}
	if metaLabel != "" {
		entries = append(entries, menuSystemEntry{
			action: menuActionMetaStory,
			label:  metaLabel,
			hint:   "/meta — sidebar conversation",
		})
	}
	return menuSystemModel{entries: entries}
}

// Open activates the overlay with the selection reset to the first row.
func (m *menuSystemModel) Open() {
	m.active = true
	m.selected = 0
}

// Close deactivates the overlay.
func (m *menuSystemModel) Close() {
	m.active = false
	m.selected = 0
}

// IsActive reports whether the overlay is currently visible.
func (m menuSystemModel) IsActive() bool { return m.active }

func (m menuSystemModel) Init() tea.Cmd { return nil }

func (m menuSystemModel) Update(msg tea.Msg) (menuSystemModel, tea.Cmd) {
	if !m.active {
		return m, nil
	}

	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch keyMsg.String() {
	case "esc", "q":
		m.active = false
		return m, nil

	case "up", "k":
		if m.selected > 0 {
			m.selected--
		}
		return m, nil

	case "down", "j":
		if m.selected < len(m.entries)-1 {
			m.selected++
		}
		return m, nil

	case "enter":
		chosen := m.entries[m.selected].action
		m.active = false
		return m, func() tea.Msg { return menuSystemChoiceMsg{action: chosen} }
	}

	// Numeric hotkeys 1..N.
	for i := 1; i <= len(m.entries) && i <= 9; i++ {
		if keyMsg.String() == fmt.Sprintf("%d", i) {
			chosen := m.entries[i-1].action
			m.active = false
			m.selected = i - 1
			return m, func() tea.Msg { return menuSystemChoiceMsg{action: chosen} }
		}
	}

	return m, nil
}

// View renders the overlay. Returns an empty string when inactive.
func (m menuSystemModel) View() string {
	if !m.active {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("menu (↑/↓ to move, Enter to pick, Esc to close)\n\n")
	for i, e := range m.entries {
		marker := "  "
		label := menuItemStyle.Render(e.label)
		if i == m.selected {
			marker = "▸ "
			label = menuItemSelectedStyle.Render(e.label)
		}
		sb.WriteString(fmt.Sprintf("%s[%d] %s", marker, i+1, label))
		if e.hint != "" {
			sb.WriteString(" — ")
			sb.WriteString(menuItemBlockedStyle.Render(e.hint))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
