package graph_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"kitsoki/internal/app"
	"kitsoki/internal/app/graph"
	"kitsoki/internal/effect"
)

func programDef() *app.AppDef {
	return &app.AppDef{
		App:  app.AppMeta{ID: "pog", Version: "1.0.0"},
		Root: "ready",
		World: map[string]app.VarDef{
			"change_id": {Type: "string"},
			"opened":    {Type: "bool"},
		},
		Intents: map[string]app.Intent{
			"open_change": {Title: "Open change", Description: "Open the selected change."},
		},
		States: map[string]*app.State{
			"ready": {
				Description: "Ready",
				View:        app.LegacyView("Selected: {{ world.change_id }}"),
				OnEnter: []app.Effect{{
					Invoke: "host.catalog.list",
					With:   map[string]any{"id": "{{ world.change_id }}"},
					Bind:   map[string]string{"opened": "ok"},
				}},
				On: map[string][]app.Transition{
					"open_change": {{
						Target: "ready",
						Effects: []app.Effect{{
							Set: map[string]any{"opened": true},
						}},
					}},
				},
			},
		},
		Application: &app.ApplicationContract{
			Schema:      app.ApplicationSchemaV1,
			Name:        "POG",
			Description: "Shape and deliver product changes.",
			SemanticRef: "pog.application",
			Shell:       app.ApplicationShell{Entry: "dashboard"},
			Navigation: []app.ApplicationNavigation{{
				ID: "dashboard", Name: "Dashboard", Description: "Open the portfolio.",
				SemanticRef: "pog.nav.dashboard", Page: "dashboard",
			}},
			Pages: map[string]*app.ApplicationPage{
				"dashboard": {
					Name: "Portfolio", Description: "Summarize active work.",
					SemanticRef: "pog.page.dashboard",
					Regions: map[string]*app.ApplicationRegion{
						"main": {
							Name: "Portfolio overview", Description: "Show active changes.",
							SemanticRef: "pog.region.main",
							Items: []app.ApplicationRegionItem{{Card: &app.ApplicationCard{
								ID: "active", Name: "Active changes", Description: "List active changes.",
								SemanticRef: "pog.card.active", Component: "pog.change-list",
								Actions: []string{"pog.change.open"},
							}}},
						},
					},
				},
			},
			Components: map[string]*app.ApplicationComponent{
				"pog.change-list": {
					Name: "Change list", Description: "Present change summaries.",
					SemanticRef: "pog.component.change-list",
					Web:         &app.ApplicationWebComponent{Module: "ui/ChangeList.vue"},
					Fallback:    &app.ApplicationComponentFallback{Element: "list"},
				},
			},
			Actions: map[string]*app.ApplicationAction{
				"pog.change.open": {
					Name: "Open change", Description: "Open the selected change.",
					SemanticRef: "pog.action.change-open", Handler: "pog.change.open",
				},
			},
		},
		Exports: &app.ExportsBlock{Handlers: map[string]*app.ApplicationHandler{
			"pog.change.open": {
				Name: "Open change", Description: "Open a change and return its frame.",
				SemanticRef: "pog.handler.change-open",
				InputSchema: "schemas/in.json", OutputSchema: "schemas/out.json",
				Session: "required", Effect: effect.Read, Outcomes: []string{"ok"},
				Dispatch: &app.ApplicationHandlerDispatch{Intent: "open_change", State: "ready"},
			},
		}},
		Events: map[string]*app.ApplicationEvent{
			"change-updated": {
				Source: "pog.change.updated", InputSchema: "schemas/event.json",
				Session: "required", Mode: "background",
				Dispatch: &app.ApplicationHandlerDispatch{Handler: "pog.change.open"},
			},
		},
	}
}

func TestProgramGraphIncludesWorkflowApplicationAndDataDependencies(t *testing.T) {
	g := graph.ProgramGraph(programDef(), "pog-program")
	if g.Kind != "story-program" || g.GraphID != "pog-program" || !g.Directed {
		t.Fatalf("graph header = %#v", g)
	}
	for _, want := range []struct {
		kind string
		ref  string
	}{
		{"state", "ready"},
		{"intent", "open_change"},
		{"world", "change_id"},
		{"world", "opened"},
		{"host", "host.catalog.list"},
		{"application", "pog.application"},
		{"page", "pog.page.dashboard"},
		{"region", "pog.region.main"},
		{"card", "pog.card.active"},
		{"component", "pog.component.change-list"},
		{"action", "pog.action.change-open"},
		{"handler", "pog.handler.change-open"},
		{"event", "change-updated"},
	} {
		if !hasProgramNode(g, want.kind, want.ref) {
			t.Errorf("missing %s node %q", want.kind, want.ref)
		}
	}
	for _, kind := range []string{
		"handles", "transitions", "reads", "writes", "invokes", "contains",
		"navigates-to", "renders", "offers", "dispatches", "observes",
	} {
		if !hasProgramEdgeKind(g, kind) {
			t.Errorf("missing edge kind %q", kind)
		}
	}
	if !g.Cyclic {
		t.Error("ready self-transition should make program graph cyclic")
	}
}

func TestProgramGraphSemanticProvenanceAndDeterminism(t *testing.T) {
	first := graph.ProgramGraph(programDef(), "pog")
	second := graph.ProgramGraph(programDef(), "pog")
	if !reflect.DeepEqual(first, second) {
		t.Fatal("ProgramGraph is not deterministic")
	}
	for _, node := range first.Nodes {
		if node.Ref.Ref != "pog.card.active" {
			continue
		}
		if node.Attrs["semantic_ref"] != "pog.card.active" {
			t.Fatalf("card semantic_ref = %#v", node.Attrs["semantic_ref"])
		}
		if node.Attrs["source"] != "application.pages.dashboard.regions.main.items[0].card" {
			t.Fatalf("card source = %#v", node.Attrs["source"])
		}
		return
	}
	t.Fatal("card node not found")
}

func TestProgramGraphWireShape(t *testing.T) {
	g := graph.ProgramGraph(programDef(), "pog")
	blob, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	var decoded graph.KitsokiGraph
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Schema != graph.SchemaV1 || decoded.GraphID != "pog" {
		t.Fatalf("decoded header = %#v", decoded)
	}
	if len(decoded.Nodes) != len(g.Nodes) || len(decoded.Edges) != len(g.Edges) {
		t.Fatalf("decoded topology = %d nodes/%d edges, want %d/%d", len(decoded.Nodes), len(decoded.Edges), len(g.Nodes), len(g.Edges))
	}
}

func hasProgramNode(g graph.KitsokiGraph, kind, ref string) bool {
	for _, node := range g.Nodes {
		if node.Kind == kind && node.Ref.Ref == ref {
			return true
		}
	}
	return false
}

func hasProgramEdgeKind(g graph.KitsokiGraph, kind string) bool {
	for _, edge := range g.Edges {
		if edge.Kind == kind {
			return true
		}
	}
	return false
}
