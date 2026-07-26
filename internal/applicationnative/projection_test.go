package applicationnative

import (
	"testing"

	"kitsoki/internal/app"
	"kitsoki/internal/application"
)

func TestProjectVSCodeReusesWebAndDeclaresCommands(t *testing.T) {
	def := &app.AppDef{Application: &app.ApplicationContract{
		Surfaces: map[string]*app.ApplicationSurface{
			"vscode": {Reuse: "web", Native: &app.ApplicationNativeSurface{Commands: []string{"demo.open"}}},
		},
	}}
	frame := nativeFrame()
	projection, err := ProjectVSCode(def, frame)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Reuse != "web" || len(projection.Commands) != 1 {
		t.Fatalf("projection = %#v", projection)
	}
	command := projection.Commands[0]
	if command.SemanticRef != "demo.action.open" ||
		command.Envelope.SessionID != "session-1" ||
		command.Envelope.FrameRevision != 3 {
		t.Fatalf("command = %#v", command)
	}
}

func TestProjectVSCodeRejectsUnknownCommand(t *testing.T) {
	def := &app.AppDef{Application: &app.ApplicationContract{
		Surfaces: map[string]*app.ApplicationSurface{
			"vscode": {Native: &app.ApplicationNativeSurface{Commands: []string{"demo.missing"}}},
		},
	}}
	if _, err := ProjectVSCode(def, nativeFrame()); err == nil {
		t.Fatal("expected unknown command to fail")
	}
}

func TestProjectTUIUsesCanonicalProjection(t *testing.T) {
	def := &app.AppDef{Application: &app.ApplicationContract{
		Surfaces: map[string]*app.ApplicationSurface{"tui": {Projection: "cards"}},
	}}
	projection, err := ProjectTUI(def, nativeFrame())
	if err != nil {
		t.Fatal(err)
	}
	if projection.Projection != "cards" || len(projection.Frame.Actions) != 1 {
		t.Fatalf("projection = %#v", projection)
	}
}

func nativeFrame() application.Frame {
	applicationNode := semantic(application.SemanticApplication, "demo.application", "Demo")
	pageNode := semantic(application.SemanticPage, "demo.page.home", "Home")
	actionNode := semantic(application.SemanticAction, "demo.action.open", "Open")
	action := application.Action{
		ID: "demo.open", Handler: "demo.open", Enabled: true, Semantic: actionNode,
		RoutingMode: application.RoutingExact,
	}
	return application.Frame{
		Schema: application.FrameSchema, ApplicationID: "demo", SessionID: "session-1",
		Revision: 3, Page: "home", Semantic: applicationNode, PageSemantic: pageNode,
		Workflow: application.Workflow{State: "ready"}, Actions: []application.Action{action},
		Regions: []application.Region{{
			ID: "main", Semantic: semantic(application.SemanticRegion, "demo.region.main", "Main"),
			Cards: []application.Card{{
				ID: "card", Semantic: semantic(application.SemanticCard, "demo.card.main", "Work"),
				Body:    []application.Element{{Kind: "prose", Value: []byte(`"Ready"`)}},
				Actions: []application.Action{action},
			}},
		}},
	}
}

func semantic(kind application.SemanticKind, ref, name string) application.SemanticNode {
	return application.SemanticNode{
		Ref: ref, Kind: kind, Name: name, Description: name + " description",
		Source: application.Provenance{Story: "demo", Member: ref},
	}
}
