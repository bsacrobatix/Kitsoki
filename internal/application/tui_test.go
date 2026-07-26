package application

import (
	"encoding/json"
	"testing"

	"kitsoki/internal/app"
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
		Props:     json.RawMessage(`["one","two"]`),
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
	var handlerChoice bool
	for _, element := range projected.View.Elements {
		if element.Kind == "choice" &&
			len(element.ChoiceItems) == 1 &&
			element.ChoiceItems[0].Intent == TUIActionIntentPrefix+"test.open" {
			handlerChoice = true
		}
	}
	if !handlerChoice {
		t.Fatalf("handler-backed action choice missing: %#v", projected.View.Elements)
	}
	var fallbackList bool
	for _, element := range projected.View.Elements {
		if element.Kind == "list" && len(element.Items) == 2 &&
			element.Items[0].Label == "one" && element.Items[1].Label == "two" {
			fallbackList = true
		}
	}
	if !fallbackList {
		t.Fatalf("list fallback was not projected semantically: %#v", projected.View.Elements)
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

func TestProjectTUIBindsFormInputToHandlerBackedAction(t *testing.T) {
	frame := testFrame()
	props, err := json.Marshal(app.ViewElement{
		Kind:         "choice",
		ChoiceMode:   "form",
		ChoiceIntent: "test.open",
		ChoiceFields: []app.ChoiceField{{Name: "item_id", Type: "string", Required: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	fieldSemantic := semantic("test.field.item-id", SemanticField)
	frame.Regions[0].Cards[0].Body = []Element{{
		Kind: "choice", Props: props, Semantic: &fieldSemantic,
	}}
	projected, err := ProjectTUI(frame)
	if err != nil {
		t.Fatal(err)
	}
	var binding string
	for _, element := range projected.View.Elements {
		if element.Kind == "choice" && element.ChoiceMode == "form" {
			binding = element.ChoiceIntent
		}
	}
	if binding != TUIActionIntentPrefix+"test.open" {
		t.Fatalf("form action binding = %#v", projected.View.Elements)
	}
	var fieldTarget *TUISemanticTarget
	for i := range projected.Targets {
		if projected.Targets[i].Ref == fieldSemantic.Ref {
			fieldTarget = &projected.Targets[i]
			break
		}
	}
	if fieldTarget == nil || fieldTarget.ElementEnd-fieldTarget.ElementStart != 1 {
		t.Fatalf("form semantic target = %#v; targets=%#v", fieldTarget, projected.Targets)
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
