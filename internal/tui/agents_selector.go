package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"kitsoki/internal/agentroot"
)

// AgentOption is one entry in the TUI agent selector. The CLI owns discovery
// (agentroot.List) and passes these values in; the TUI only renders and
// returns the selected row.
type AgentOption struct {
	Name        string
	Source      string // builtin|library|project
	Description string
	Effect      string // pure|read|write|external
}

// StoryPath returns the virtual story path (`agent:<name>`) that launching
// this agent hands back to the CLI's story-switch outer loop.
func (a AgentOption) StoryPath() string {
	return agentroot.Scheme + a.Name
}

type agentSelectorChoiceMsg struct {
	agent AgentOption
}

type agentSelectorModel struct {
	active   bool
	agents   []AgentOption
	err      string
	selected int
}

func newAgentSelectorModel() agentSelectorModel {
	return agentSelectorModel{}
}

func (m *agentSelectorModel) SetAgents(agents []AgentOption, errText string) {
	m.agents = append([]AgentOption(nil), agents...)
	m.err = strings.TrimSpace(errText)
	if m.selected >= len(m.agents) {
		m.selected = 0
	}
}

func (m *agentSelectorModel) Open(currentPath string) {
	m.active = true
	m.selected = 0
	for i, agent := range m.agents {
		if sameAgentPath(currentPath, agent.StoryPath()) {
			m.selected = i
			break
		}
	}
}

func (m *agentSelectorModel) Close() {
	m.active = false
	m.selected = 0
}

func (m agentSelectorModel) IsActive() bool { return m.active }

func (m agentSelectorModel) Update(msg tea.Msg) (agentSelectorModel, tea.Cmd) {
	if !m.active {
		return m, nil
	}
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch keyMsg.String() {
	case "esc", "q":
		m.Close()
		return m, nil
	case "up", "k":
		if m.selected > 0 {
			m.selected--
		}
		return m, nil
	case "down", "j":
		if m.selected < len(m.agents)-1 {
			m.selected++
		}
		return m, nil
	case "enter":
		if len(m.agents) == 0 {
			return m, nil
		}
		chosen := m.agents[m.selected]
		m.Close()
		return m, func() tea.Msg { return agentSelectorChoiceMsg{agent: chosen} }
	}
	for i := 1; i <= len(m.agents) && i <= 9; i++ {
		if keyMsg.String() == fmt.Sprintf("%d", i) {
			chosen := m.agents[i-1]
			m.selected = i - 1
			m.Close()
			return m, func() tea.Msg { return agentSelectorChoiceMsg{agent: chosen} }
		}
	}
	return m, nil
}

func (m agentSelectorModel) View() string {
	if !m.active {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("agents (up/down to move, Enter to launch, Esc to close)\n\n")
	if m.err != "" {
		sb.WriteString(choiceErrorStyle.Render("(" + m.err + ")"))
		sb.WriteString("\n\n")
	}
	if len(m.agents) == 0 {
		sb.WriteString(choiceHintStyle.Render(agentSelectorEmptyHint))
		return sb.String()
	}
	sb.WriteString(strings.Join(m.rows(), "\n"))
	return strings.TrimRight(sb.String(), "\n")
}

// ChromeView renders the selector through the shared normal-screen row
// budget. Logical row numbers remain absolute while the visible window follows
// selected.
func (m agentSelectorModel) ChromeView(width, maxRows int) string {
	if !m.active {
		return ""
	}
	header := []string{"agents (up/down to move, Enter to launch, Esc to close)"}
	if m.err != "" {
		header = append(header, choiceErrorStyle.Render("("+m.err+")"))
	}
	if len(m.agents) == 0 {
		return renderLiveOverlay(header, []string{
			choiceHintStyle.Render(agentSelectorEmptyHint),
		}, 0, width, maxRows)
	}
	return renderLiveOverlay(header, m.rows(), m.selected, width, maxRows)
}

const agentSelectorEmptyHint = "No agents discovered. Check .kitsoki/agents, ~/.codex/agents, or the builtin library."

func (m agentSelectorModel) rows() []string {
	rows := make([]string, 0, len(m.agents))
	for i, agent := range m.agents {
		marker := "  "
		label := agentSelectorLabel(agent)
		if i == m.selected {
			marker = "> "
			label = menuItemSelectedStyle.Render(label)
		} else {
			label = menuItemStyle.Render(label)
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("%s[%d] %s", marker, i+1, label))
		if hint := agentSelectorHint(agent); hint != "" {
			sb.WriteString(" - ")
			sb.WriteString(menuItemBlockedStyle.Render(hint))
		}
		rows = append(rows, sb.String())
	}
	return rows
}

func agentSelectorLabel(agent AgentOption) string {
	if agent.Name != "" {
		return agent.Name
	}
	return "(unnamed agent)"
}

func agentSelectorHint(agent AgentOption) string {
	var parts []string
	if agent.Effect != "" {
		parts = append(parts, agent.Effect)
	}
	if agent.Source != "" {
		parts = append(parts, agent.Source)
	}
	if desc := firstDescriptionLine(agent.Description); desc != "" {
		parts = append(parts, desc)
	}
	return strings.Join(parts, " | ")
}

// firstDescriptionLine flattens a multi-line agent description to its first
// non-empty line so the single-row selector hint stays a single row.
func firstDescriptionLine(desc string) string {
	for _, line := range strings.Split(desc, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// sameAgentPath compares two agent-scheme story paths. Unlike sameStoryPath
// there is no filesystem shape to normalize — the scheme string is the
// identity — so this is a plain trimmed comparison.
func sameAgentPath(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	return a == b
}
