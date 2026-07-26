package app

import "testing"

func TestEffectiveApplicationProjectsLegacyTypedView(t *testing.T) {
	def := &AppDef{
		App:  AppMeta{ID: "legacy", Title: "Legacy flow"},
		Root: "ready",
		Intents: map[string]Intent{
			"submit": {Title: "Submit", Description: "Submit the current value."},
		},
		States: map[string]*State{
			"ready": {
				Description: "Ready",
				View:        View{Elements: []ViewElement{{Kind: "prose", Source: "Current value"}}},
				On:          map[string][]Transition{"submit": {{Target: "done"}}},
			},
			"done": {Terminal: true},
		},
	}
	contract, generated := EffectiveApplication(def)
	if !generated || !contract.Generated || def.Application != nil {
		t.Fatalf("projection flags: generated=%v contract=%#v authored=%#v", generated, contract, def.Application)
	}
	card := contract.Pages["ready"].Regions["main"].Items[0].Card
	if len(card.Elements) != 1 || card.Elements[0].Source != "Current value" {
		t.Fatalf("projected elements = %#v", card.Elements)
	}
	action := contract.Actions["legacy.action.ready.submit"]
	if action == nil || action.Intent != "submit" || action.State != "ready" {
		t.Fatalf("projected action = %#v", action)
	}
}
