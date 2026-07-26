package application

import (
	"testing"
)

func TestProjectTUIUsesTypedElementsFallbacksAndSemanticTargets(t *testing.T) {
	frame := testFrame()
	frame.Components = []ComponentDescriptor{{
		ID:       "test.component.items",
		Fallback: "list",
		Semantic: semantic("test.component.items", SemanticComponent),
	}}
	frame.Regions[0].Cards[0].Body = []Element{{
		Kind:      "component",
		Component: "test.component.items",
	}}
	frame.Regions[0].Cards[0].Semantic.Source.ProgramNode = "program.card.items"
	projected, err := ProjectTUI(frame)
	if err != nil {
		t.Fatal(err)
	}
	if len(projected.View.Elements) < 7 {
		t.Fatalf("typed elements = %#v", projected.View.Elements)
	}
	if len(projected.Actions) != 1 || projected.Actions[0].Envelope.FrameRevision != 7 {
		t.Fatalf("actions = %#v", projected.Actions)
	}
	var cardTarget *TUISemanticTarget
	for i := range projected.Targets {
		if projected.Targets[i].Ref == "test.card.items" {
			cardTarget = &projected.Targets[i]
			break
		}
	}
	if cardTarget == nil || cardTarget.ProgramNode == "" || cardTarget.ElementEnd <= cardTarget.ElementStart {
		t.Fatalf("card target = %#v", cardTarget)
	}
}

func TestProjectTUIRejectsCustomComponentWithoutFallback(t *testing.T) {
	frame := testFrame()
	frame.Components = []ComponentDescriptor{{
		ID:       "demo.component",
		Semantic: semantic("demo.component", SemanticComponent),
	}}
	frame.Regions[0].Cards[0].Body = []Element{{Kind: "component", Component: "demo.component"}}
	if _, err := ProjectTUI(frame); err == nil {
		t.Fatal("expected missing fallback error")
	}
}
