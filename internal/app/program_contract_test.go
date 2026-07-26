package app

import (
	"strings"
	"testing"
)

func TestFiniteProgramContractsResolveRoomInterfaceAndCheckTotality(t *testing.T) {
	def := &AppDef{
		App:   AppMeta{ID: "orders"},
		World: map[string]VarDef{"order_id": {Type: "string"}},
		RoomInterfaces: map[string]*RoomInterfaceDef{
			"reviewer": {
				Intents: map[string]Intent{
					"approve": {Slots: map[string]Slot{"reason": {Type: "string", Required: true}}},
				},
				World: map[string]RoomWorldContract{"order_id": {Type: "string", Access: "read"}},
			},
		},
		States: map[string]*State{
			"review": {
				Implements: []string{"reviewer"},
				Intents: map[string]Intent{
					"approve": {Slots: map[string]Slot{"reason": {Type: "string", Required: true}}},
				},
				On: map[string][]Transition{
					"approve": {
						{When: "reason == 'later'", Target: "review"},
						{Default: true, Target: "done"},
					},
				},
			},
			"done": {Terminal: true},
		},
		Application: &ApplicationContract{
			Actions: map[string]*ApplicationAction{
				"orders.approve": {RoomInterface: "reviewer", Intent: "approve"},
			},
		},
	}
	if errs := validateFiniteProgramContracts(def, "app.yaml"); len(errs) != 0 {
		t.Fatalf("validateFiniteProgramContracts: %v", errs)
	}
	if got := def.Application.Actions["orders.approve"].State; got != "review" {
		t.Fatalf("resolved state = %q, want review", got)
	}

	def.States["review"].On["approve"] = []Transition{{When: "reason == 'later'", Target: "review"}}
	errs := validateFiniteProgramContracts(def, "app.yaml")
	if !errorsContain(errs, "guard partition is not total") {
		t.Fatalf("missing totality error: %v", errs)
	}
}

func TestFiniteProgramContractsValidateEffectOutcomeAndBindTypes(t *testing.T) {
	def := &AppDef{
		App:   AppMeta{ID: "orders"},
		World: map[string]VarDef{"count": {Type: "int"}},
		States: map[string]*State{
			"start": {
				OnEnter: []Effect{{
					Invoke: "host.orders.read",
					Result: map[string]EffectResultField{"value": {Type: "string"}},
					Bind:   map[string]string{"count": "value"},
					Outcomes: map[string]*EffectOutcome{
						"ok":   {When: "result.ok", Target: "done"},
						"fail": {Default: true, Target: "missing"},
					},
				}},
			},
			"done": {Terminal: true},
		},
	}
	errs := validateFiniteProgramContracts(def, "app.yaml")
	for _, want := range []string{"has incompatible types", `target "missing"`} {
		if !errorsContain(errs, want) {
			t.Errorf("missing %q in %v", want, errs)
		}
	}
}

func TestLoadBytesRejectsUndeclaredFiniteOutcomeBindTarget(t *testing.T) {
	_, err := LoadBytes([]byte(`
app: {id: finite-bind, version: 1.0.0}
hosts: [host.probe]
root: start
states:
  start:
    on_enter:
      - invoke: host.probe
        result:
          value: {type: string}
        bind:
          missing: value
        outcomes:
          done:
            default: true
            target: done
  done: {terminal: true}
`))
	if err == nil || !strings.Contains(err.Error(), "bind target missing is not declared in world") {
		t.Fatalf("LoadBytes error = %v", err)
	}
}

func TestLoadBytesPreservesLegacyDynamicBindBehavior(t *testing.T) {
	_, err := LoadBytes([]byte(`
app: {id: legacy-bind, version: 1.0.0}
hosts: [host.probe]
root: start
states:
  start:
    on_enter:
      - invoke: host.probe
        bind:
          legacy_dynamic: value
`))
	if err != nil {
		t.Fatalf("LoadBytes legacy bind: %v", err)
	}
}

func TestExpandRoomTemplatesUsesPhaseTemplateContract(t *testing.T) {
	def := &AppDef{
		PhaseTemplates: map[string]*PhaseTemplate{
			"review": {
				Parameters: map[string]PhaseTemplateParam{
					"kind": {Type: "string", Required: true},
				},
				States: map[string]*State{
					"{kind}_ready": {Description: "{{ tpl.kind }} ready", Terminal: true},
				},
			},
		},
		States: map[string]*State{
			"bad": {
				RoomTemplate: "review", RoomParameters: map[string]any{"kind": true},
				Initial: "true_ready",
			},
			"good": {
				RoomTemplate: "review", RoomParameters: map[string]any{"kind": "order"},
				Initial: "order_ready",
			},
		},
	}
	errs := expandRoomTemplates(def, "app.yaml")
	if !errorsContain(errs, "has type bool, want string") {
		t.Fatalf("errors = %v", errs)
	}
	good := def.States["good"]
	if good.InstantiatedFrom != "review" || good.States["order_ready"] == nil {
		t.Fatalf("valid room was not expanded after invalid sibling: %#v", good)
	}
}

func errorsContain(errs []error, want string) bool {
	for _, err := range errs {
		if strings.Contains(err.Error(), want) {
			return true
		}
	}
	return false
}
