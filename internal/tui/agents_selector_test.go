package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"kitsoki/internal/app"
)

// The /agents selector mirrors the /stories selector: the CLI owns discovery
// and passes AgentOption rows in; selection hands `agent:<name>` back through
// the same story-switch outer loop. These tests mirror the /stories model
// tests in commands_test.go.

func TestAgentsSlashOpensSelector(t *testing.T) {
	orch := testCloakOrchestrator(t)
	m := NewRootModel(orch, app.SessionID("session-123"), "../../testdata/apps/cloak/app.yaml", "",
		WithAgentSelector([]AgentOption{
			{Name: "demo", Source: "project", Description: "Demo helper agent", Effect: "read"},
			{Name: "story-bug-reporter", Source: "builtin", Effect: "external"},
		}, nil, nil),
	)

	next, cmd := m.RunSlashCommand("/agents")
	if cmd != nil {
		t.Fatal("/agents should open synchronously")
	}
	if next.mode != ModeAgentSelector {
		t.Fatalf("mode = %v, want ModeAgentSelector", next.mode)
	}
	view := next.View()
	for _, want := range []string{"agents", "demo", "story-bug-reporter"} {
		if !strings.Contains(view, want) {
			t.Fatalf("selector view missing %q\n---\n%s", want, view)
		}
	}
}

func TestAgentsSlashNoAgentsDiscovered(t *testing.T) {
	orch := testCloakOrchestrator(t)
	m := NewRootModel(orch, app.SessionID("session-123"), "../../testdata/apps/cloak/app.yaml", "")

	next, cmd := m.RunSlashCommand("/agents")
	if cmd != nil {
		t.Fatal("/agents should answer synchronously")
	}
	if next.mode != ModeOnPath {
		t.Fatalf("mode = %v, want ModeOnPath", next.mode)
	}
	if !strings.Contains(next.transcript.LastBody(), "no agents discovered") {
		t.Fatalf("transcript missing empty-catalog notice:\n%s", next.transcript.LastBody())
	}
}

func TestAgentSelectorSelectionInvokesCallbackAndQuits(t *testing.T) {
	orch := testCloakOrchestrator(t)
	var selected AgentOption
	m := NewRootModel(orch, app.SessionID("session-123"), "../../testdata/apps/cloak/app.yaml", "",
		WithAgentSelector([]AgentOption{
			{Name: "demo", Source: "project", Effect: "read"},
			{Name: "reviewer", Source: "library", Effect: "write"},
		}, nil, func(agent AgentOption) { selected = agent }),
	)

	next, _ := m.RunSlashCommand("/agents")
	model, _ := tea.Model(next).Update(tea.KeyMsg{Type: tea.KeyDown})
	model, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should emit an agent selection command")
	}
	model, cmd = model.Update(runBatch(t, cmd))
	if selected.Name != "reviewer" {
		t.Fatalf("selected agent = %q, want reviewer", selected.Name)
	}
	if selected.StoryPath() != "agent:reviewer" {
		t.Fatalf("selected story path = %q, want agent:reviewer", selected.StoryPath())
	}
	if cmd == nil {
		t.Fatal("selection should return tea.Quit")
	}
	rm, ok := model.(RootModel)
	if !ok {
		t.Fatalf("model = %T, want RootModel", model)
	}
	if !rm.quitting {
		t.Fatal("model should be marked quitting after agent selection")
	}
}

func TestAgentSelectorCurrentAgentDoesNotRestart(t *testing.T) {
	orch := testCloakOrchestrator(t)
	called := false
	m := NewRootModel(orch, app.SessionID("session-123"), "agent:demo", "",
		WithAgentSelector([]AgentOption{
			{Name: "demo", Source: "project", Effect: "read"},
		}, nil, func(AgentOption) { called = true }),
	)

	next, _ := m.RunSlashCommand("/agents")
	model, cmd := tea.Model(next).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should emit an agent selection command")
	}
	model, _ = model.Update(runBatch(t, cmd))
	if called {
		t.Fatal("current-agent selection should not invoke restart callback")
	}
	rm, ok := model.(RootModel)
	if !ok {
		t.Fatalf("model = %T, want RootModel", model)
	}
	if rm.quitting {
		t.Fatal("current-agent selection should leave the model running")
	}
	if !strings.Contains(rm.transcript.LastBody(), "already running") {
		t.Fatalf("transcript missing already-running notice:\n%s", rm.transcript.LastBody())
	}
}

func TestAgentSelectorEscCloses(t *testing.T) {
	orch := testCloakOrchestrator(t)
	m := NewRootModel(orch, app.SessionID("session-123"), "../../testdata/apps/cloak/app.yaml", "",
		WithAgentSelector([]AgentOption{
			{Name: "demo", Source: "project", Effect: "read"},
		}, nil, nil),
	)

	next, _ := m.RunSlashCommand("/agents")
	model, _ := tea.Model(next).Update(tea.KeyMsg{Type: tea.KeyEsc})
	rm, ok := model.(RootModel)
	if !ok {
		t.Fatalf("model = %T, want RootModel", model)
	}
	if rm.mode != ModeOnPath {
		t.Fatalf("mode after Esc = %v, want ModeOnPath", rm.mode)
	}
	if rm.agentSelector.IsActive() {
		t.Fatal("selector should be closed after Esc")
	}
}

func TestAgentSelectorHintFlattensMultilineDescription(t *testing.T) {
	t.Parallel()
	hint := agentSelectorHint(AgentOption{
		Name:        "demo",
		Source:      "project",
		Effect:      "read",
		Description: "\nFirst line of the description.\nSecond line never shows.",
	})
	if !strings.Contains(hint, "First line of the description.") {
		t.Fatalf("hint missing first description line: %q", hint)
	}
	if strings.Contains(hint, "Second line") {
		t.Fatalf("hint leaked later description lines: %q", hint)
	}
	if !strings.HasPrefix(hint, "read | project") {
		t.Fatalf("hint = %q, want effect | source prefix", hint)
	}
}
