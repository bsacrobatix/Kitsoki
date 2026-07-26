package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	appplatform "kitsoki/internal/application"
	"kitsoki/internal/applicationconformance"
	"kitsoki/internal/machine"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/runstatus/server"
	"kitsoki/internal/store"
)

// conformance-consumer:tui
const tuiConformanceFixture = "application-conformance-v1.json"

func TestTUIApplicationConformanceFixture(t *testing.T) {
	fixture, err := applicationconformance.Load()
	if err != nil {
		t.Fatal(err)
	}
	def := applicationTUITestDefinition()
	mach, err := machine.New(def)
	if err != nil {
		t.Fatal(err)
	}
	sessionStore, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer sessionStore.Close()
	orch := orchestrator.New(def, mach, sessionStore, applicationHarness{})
	sid, err := orch.NewSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	source := applicationTUITestSource{orch: orch, sid: sid}
	driver := server.OrchestratorDriver{Orch: orch, SID: sid}
	service, err := server.NewSessionApplicationService(server.Entry{Source: source, Driver: driver}, "")
	if err != nil {
		t.Fatal(err)
	}
	var dispatched appplatform.OutcomeEnvelope
	dispatcher := func(ctx context.Context, envelope appplatform.ActionEnvelope) (ApplicationActionResult, error) {
		envelope.Actor = "operator"
		outcome, dispatchErr := service.DispatchAction(ctx, appplatform.TransportTUI, envelope)
		dispatched = outcome
		view, viewErr := driver.View(ctx)
		if dispatchErr != nil {
			return ApplicationActionResult{Outcome: outcome}, dispatchErr
		}
		return ApplicationActionResult{Outcome: outcome, View: view}, viewErr
	}
	root := t.TempDir()
	model := NewRootModel(
		orch, sid, "", "",
		WithApplicationActionDispatcher(dispatcher), WithBugRoot(root), WithBugTicketRepo(""),
	)
	if model.applicationProjection.Canonical.Semantic.Ref != fixture.Expected.ApplicationSemanticRef ||
		model.applicationProjection.Frame.Actions[0].SemanticRef != fixture.Action.SemanticRef {
		t.Fatalf("TUI semantic projection = %#v", model.applicationProjection)
	}
	actionIntent := appplatform.TUIActionIntentPrefix + fixture.Action.ID
	actionCursor := -1
	for index, item := range model.choice.items {
		if item.intent == actionIntent {
			actionCursor = index
			break
		}
	}
	if actionCursor < 0 {
		t.Fatalf("TUI action %q is absent from choice adapter", fixture.Action.ID)
	}
	model.choice.cursor = actionCursor
	model.captureApplicationChoiceFocus()
	next, cmd := model.updateChoosing(tea.KeyMsg{Type: tea.KeyEnter})
	outcome, ok := applicationTurnOutcome(cmd)
	if !ok {
		t.Fatal("TUI action did not dispatch through its intent boundary")
	}
	landed, _ := next.(RootModel).handleTurnOutcome(outcome)
	normalized := applicationconformance.Normalize(dispatched)
	if normalized.Transport != appplatform.TransportTUI ||
		normalized.Schema != fixture.Expected.OutcomeSchema ||
		normalized.ReceiptSchema != fixture.Expected.ReceiptSchema {
		t.Fatalf("TUI normalized outcome = %#v", normalized)
	}

	body, _, _ := BugCommand{}.Run(landed.(RootModel), strings.Fields(fixture.Feedback.Instruction))
	if !strings.Contains(body, "filed .artifacts/issues/bugs/") {
		t.Fatalf("TUI feedback result = %q", body)
	}
	anchors, _ := filepath.Glob(filepath.Join(root, ".artifacts", "issues", "bugs", "*.artifacts", "application-anchor.json"))
	if len(anchors) != 1 {
		t.Fatalf("TUI feedback anchors = %#v", anchors)
	}
	anchor, err := os.ReadFile(anchors[0])
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(anchor, &decoded); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(anchor), fixture.Feedback.Ref) {
		t.Fatalf("TUI feedback anchor = %s", anchor)
	}
	for _, excluded := range fixture.Feedback.Excluded {
		if strings.Contains(string(anchor), excluded) {
			t.Fatalf("TUI feedback leaked %q", excluded)
		}
	}
}
