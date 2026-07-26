package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"kitsoki/internal/app"
	appplatform "kitsoki/internal/application"
	"kitsoki/internal/harness"
	"kitsoki/internal/machine"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/runstatus"
	"kitsoki/internal/runstatus/server"
	"kitsoki/internal/store"
)

type applicationHarness struct{}

func (applicationHarness) RunTurn(context.Context, harness.TurnInput) (mcp.CallToolParams, error) {
	return mcp.CallToolParams{}, nil
}

func (applicationHarness) Close() error { return nil }

func TestProjectApplicationTUIRetainsSemanticActionRevision(t *testing.T) {
	def := &app.AppDef{
		App: app.AppMeta{ID: "demo"},
		World: map[string]app.VarDef{
			"catalog": {Type: "object", Default: map[string]any{}},
		},
		Application: &app.ApplicationContract{
			Schema: app.ApplicationSchemaV1, Name: "Demo", Description: "Demo application.",
			SemanticRef: "demo.application", Shell: app.ApplicationShell{Entry: "home"},
			Data: map[string]*app.ApplicationData{
				"catalog": {
					Source: "world.catalog", Sensitivity: "internal", Policy: "include",
				},
			},
			Pages: map[string]*app.ApplicationPage{
				"home": {
					Name: "Home", Description: "Home page.", SemanticRef: "demo.page.home",
					Regions: map[string]*app.ApplicationRegion{
						"main": {
							Name: "Main", Description: "Main region.", SemanticRef: "demo.region.main",
							Items: []app.ApplicationRegionItem{{Card: &app.ApplicationCard{
								ID: "work", Name: "Work", Description: "Current work.",
								SemanticRef: "demo.card.work", Actions: []string{"demo.open"},
								Elements: []app.ViewElement{{Kind: "prose", Source: "Ready"}},
							}}},
						},
					},
				},
			},
			Actions: map[string]*app.ApplicationAction{
				"demo.open": {
					Name: "Open", Description: "Open the work.", SemanticRef: "demo.action.open",
					Intent: "open",
				},
			},
			Surfaces: map[string]*app.ApplicationSurface{"tui": {Projection: "cards"}},
		},
	}
	projection, err := projectApplicationTUI(
		def, "session-1", 9, "ready", []string{"open"},
		map[string]any{
			"catalog": map[string]any{"nodes": []any{"one"}},
			"ambient": "/private/path",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Projection != "cards" || len(projection.Frame.View.Elements) == 0 {
		t.Fatalf("projection = %#v", projection)
	}
	if len(projection.Frame.Actions) != 1 {
		t.Fatalf("actions = %#v", projection.Frame.Actions)
	}
	if got := string(projection.Canonical.Data["catalog"].Value); got != `{"nodes":["one"]}` {
		t.Fatalf("TUI frame data = %s", got)
	}
	wire, err := json.Marshal(projection.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "ambient") || strings.Contains(string(wire), "/private/path") {
		t.Fatalf("TUI frame leaked ambient world data: %s", wire)
	}
	action := projection.Frame.Actions[0]
	if action.SemanticRef != "demo.action.open" ||
		action.Envelope.FrameRevision != 9 ||
		action.Envelope.SessionID != "session-1" {
		raw, _ := json.Marshal(action)
		t.Fatalf("action identity = %s", raw)
	}
}

func TestRootModelInvokesProjectedApplicationActionThroughIntentBoundary(t *testing.T) {
	def := applicationTUITestDefinition()
	mach, err := machine.New(def)
	if err != nil {
		t.Fatal(err)
	}
	sessionStore, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessionStore.Close() })
	orch := orchestrator.New(def, mach, sessionStore, applicationHarness{})
	sessionID, err := orch.NewSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	source := applicationTUITestSource{orch: orch, sid: sessionID}
	driver := server.OrchestratorDriver{Orch: orch, SID: sessionID}
	service, err := server.NewSessionApplicationService(server.Entry{Source: source, Driver: driver}, "")
	if err != nil {
		t.Fatal(err)
	}
	var dispatched appplatform.OutcomeEnvelope
	dispatcher := func(ctx context.Context, envelope appplatform.ActionEnvelope) (ApplicationActionResult, error) {
		envelope.Actor = "operator"
		outcome, dispatchErr := service.DispatchAction(ctx, appplatform.TransportTUI, envelope)
		dispatched = outcome
		if dispatchErr != nil {
			return ApplicationActionResult{Outcome: outcome}, dispatchErr
		}
		view, viewErr := driver.View(ctx)
		return ApplicationActionResult{Outcome: outcome, View: view}, viewErr
	}
	model := NewRootModel(orch, sessionID, "", "", WithApplicationActionDispatcher(dispatcher))
	if model.mode != ModeChoosing || model.applicationProjection == nil {
		t.Fatalf("application projection was not mounted: mode=%v projection=%#v", model.mode, model.applicationProjection)
	}
	action := model.applicationProjection.Frame.Actions[0]
	if action.SemanticRef != "demo.action.open" || action.Envelope.FrameRevision != 0 {
		t.Fatalf("action = %#v", action)
	}

	nextModel, cmd := model.updateChoosing(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("application action did not dispatch")
	}
	outcome, ok := applicationTurnOutcome(cmd)
	if !ok {
		t.Fatal("dispatch did not produce a turn outcome")
	}
	landedModel, _ := nextModel.(RootModel).handleTurnOutcome(outcome)
	landed := landedModel.(RootModel)
	if landed.currentState != "done" {
		t.Fatalf("current state = %q, want done; error=%v outcome=%#v dispatched=%#v", landed.currentState, outcome.err, outcome, dispatched)
	}
	if dispatched.Receipt.SemanticRef != "demo.action.open" ||
		dispatched.Receipt.FrameRevision != 0 ||
		dispatched.Receipt.Transport != appplatform.TransportTUI {
		t.Fatalf("receipt = %#v", dispatched.Receipt)
	}
}

func TestRootModelResumedApplicationProjectionUsesCurrentTurnRevision(t *testing.T) {
	def := applicationTUITestDefinition()
	mach, err := machine.New(def)
	if err != nil {
		t.Fatal(err)
	}
	sessionStore, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessionStore.Close() })
	orch := orchestrator.New(def, mach, sessionStore, applicationHarness{})
	sid, err := orch.NewSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	journey, err := orch.LoadJourney(sid)
	if err != nil {
		t.Fatal(err)
	}
	model := NewRootModel(
		orch, sid, "", "",
		WithResumedJourney(journey.State, journey.World, 9),
	)
	if model.applicationProjection == nil || model.applicationProjection.Canonical.Revision != 9 {
		t.Fatalf("resumed projection = %#v", model.applicationProjection)
	}
	anchor := model.applicationBugAnchor()
	if !strings.Contains(string(anchor), `"ref": "demo.action.open"`) ||
		!strings.Contains(string(anchor), `"plugin": "kitsoki.application"`) {
		t.Fatalf("application bug anchor = %s", anchor)
	}
	for _, excluded := range []string{"secret-prop", "api-token", "/Users/operator"} {
		if strings.Contains(string(anchor), excluded) {
			t.Fatalf("application bug anchor leaked %q: %s", excluded, anchor)
		}
	}
}

func TestBugCommandWritesCurrentApplicationActionAnchor(t *testing.T) {
	def := applicationTUITestDefinition()
	mach, err := machine.New(def)
	if err != nil {
		t.Fatal(err)
	}
	sessionStore, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessionStore.Close() })
	orch := orchestrator.New(def, mach, sessionStore, applicationHarness{})
	sid, err := orch.NewSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	model := NewRootModel(orch, sid, "", "", WithBugRoot(root), WithBugTicketRepo(""))
	body, _, cmd := BugCommand{}.Run(model, []string{"selected", "action", "is", "unclear"})
	if cmd != nil || !strings.Contains(body, "filed .artifacts/issues/bugs/") {
		t.Fatalf("/bug result: body=%q cmd=%v", body, cmd)
	}
	anchors, err := filepath.Glob(filepath.Join(
		root, ".artifacts", "issues", "bugs", "*.artifacts", "application-anchor.json",
	))
	if err != nil || len(anchors) != 1 {
		t.Fatalf("application anchor artifacts = %v, err=%v", anchors, err)
	}
	anchor, err := os.ReadFile(anchors[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(anchor), `"ref": "demo.action.open"`) {
		t.Fatalf("/bug application anchor = %s", anchor)
	}
	for _, excluded := range []string{"secret-prop", "api-token", "/Users/operator"} {
		if strings.Contains(string(anchor), excluded) {
			t.Fatalf("/bug application anchor leaked %q: %s", excluded, anchor)
		}
	}
}

type applicationTUITestSource struct {
	orch *orchestrator.Orchestrator
	sid  app.SessionID
}

func (s applicationTUITestSource) Snapshot() (runstatus.Snapshot, error) {
	journey, err := s.orch.LoadJourney(s.sid)
	if err != nil {
		return runstatus.Snapshot{}, err
	}
	return runstatus.Snapshot{
		Session: runstatus.SessionHeader{
			SessionID: string(s.sid), AppID: s.orch.AppDef().App.ID,
			CurrentState: string(journey.State), Turn: int(journey.Turn),
		},
		App: s.orch.AppDef(),
	}, nil
}

func (s applicationTUITestSource) Events() ([]runstatus.TraceEvent, error) { return nil, nil }
func (s applicationTUITestSource) AppDef() *app.AppDef                     { return s.orch.AppDef() }

func applicationTurnOutcome(cmd tea.Cmd) (turnOutcomeMsg, bool) {
	if cmd == nil {
		return turnOutcomeMsg{}, false
	}
	switch message := cmd().(type) {
	case turnOutcomeMsg:
		return message, true
	case tea.BatchMsg:
		for _, nested := range message {
			if outcome, ok := applicationTurnOutcome(nested); ok {
				return outcome, true
			}
		}
	}
	return turnOutcomeMsg{}, false
}

func applicationTUITestDefinition() *app.AppDef {
	def := &app.AppDef{
		App:  app.AppMeta{ID: "demo", Version: "1.0.0"},
		Root: "ready",
		Intents: map[string]app.Intent{
			"open": {Title: "Open", Description: "Open the work."},
		},
		States: map[string]*app.State{
			"ready": {
				On: map[string][]app.Transition{"open": {{Target: "done"}}},
			},
			"done": {Terminal: true},
		},
		Application: &app.ApplicationContract{
			Schema: app.ApplicationSchemaV1, Name: "Demo", Description: "Demo application.",
			SemanticRef: "demo.application", Shell: app.ApplicationShell{Entry: "home"},
			Pages: map[string]*app.ApplicationPage{
				"home": {
					Name: "Home", Description: "Home page.", SemanticRef: "demo.page.home",
					Regions: map[string]*app.ApplicationRegion{
						"main": {
							Name: "Main", Description: "Main region.", SemanticRef: "demo.region.main",
							Items: []app.ApplicationRegionItem{{Card: &app.ApplicationCard{
								ID: "work", Name: "Work", Description: "Current work.",
								SemanticRef: "demo.card.work", Actions: []string{"demo.open"},
								Elements: []app.ViewElement{{Kind: "prose", Source: "Ready"}},
							}}},
						},
					},
				},
			},
			Actions: map[string]*app.ApplicationAction{
				"demo.open": {
					Name: "Open", Description: "Open the work.", SemanticRef: "demo.action.open",
					Intent: "open",
				},
			},
			Surfaces: map[string]*app.ApplicationSurface{"tui": {Projection: "cards"}},
		},
	}
	return def
}
