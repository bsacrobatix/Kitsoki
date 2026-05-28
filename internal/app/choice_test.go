package app

import (
	"encoding/json"
	"strings"
	"testing"

	"kitsoki/internal/render"
)

// ---- P1-4: Cross-ref scope must include compound-ancestor intents ----------

// TestLoad_Choice_AncestorIntentInScope confirms that a child state's
// `choice:` element can reference an intent declared on a compound-state
// ancestor. The runtime machine walks the compound stack to resolve
// intents; the load-time choice cross-ref must use the same scope.
//
// Before the fix this load failed with
//
//	state "parent.child": view[0] (choice): items[0]: intent "ancestor_pick" not declared in state or global intents
func TestLoad_Choice_AncestorIntentInScope(t *testing.T) {
	yaml := []byte(`app:
  id: ancestor-intent-scope
  version: 0.1.0
root: parent
states:
  parent:
    type: compound
    initial: child
    intents:
      ancestor_pick:
        title: "Pick something"
        slots:
          value:
            type: string
            required: true
    states:
      child:
        view:
          - choice:
              mode: single
              items:
                - label: "Alpha"
                  intent: ancestor_pick
                  slots: { value: a }
`)
	def, err := LoadBytes(yaml)
	if err != nil {
		t.Fatalf("LoadBytes: unexpected error: %v", err)
	}
	if def == nil {
		t.Fatalf("def is nil")
	}
}

// TestLoad_Choice_AncestorIntentInScope_TransitionView covers the same
// inheritance rule but on a transition view (which has its own
// cross-ref call site).
func TestLoad_Choice_AncestorIntentInScope_TransitionView(t *testing.T) {
	yaml := []byte(`app:
  id: ancestor-intent-trv
  version: 0.1.0
root: parent
intents:
  noop:
    title: "noop"
states:
  parent:
    type: compound
    initial: child
    intents:
      ancestor_pick:
        title: "Pick"
        slots:
          value:
            type: string
            required: true
    states:
      child:
        on:
          noop:
            - view:
                - choice:
                    mode: single
                    items:
                      - label: "Pick"
                        intent: ancestor_pick
                        slots: { value: x }
`)
	if _, err := LoadBytes(yaml); err != nil {
		t.Fatalf("LoadBytes: unexpected error: %v", err)
	}
}

// TestLoad_Choice_UnknownIntentStillFails ensures the relaxed scope
// doesn't silently accept truly-unknown intents.
func TestLoad_Choice_UnknownIntentStillFails(t *testing.T) {
	yaml := []byte(`app:
  id: unknown-intent
  version: 0.1.0
root: parent
states:
  parent:
    type: compound
    initial: child
    states:
      child:
        view:
          - choice:
              mode: single
              items:
                - label: "Alpha"
                  intent: nope_not_declared
`)
	_, err := LoadBytes(yaml)
	if err == nil {
		t.Fatalf("expected an error for the unknown-intent reference")
	}
	if !strings.Contains(err.Error(), "nope_not_declared") {
		t.Fatalf("error did not mention the unknown intent: %v", err)
	}
}

// ---- P1-5: pongo parse probe is parse-only ---------------------------------

// TestValidateChoice_PongoTemplateSyntaxError pins the load-time
// behaviour for a malformed template body. The new discriminator runs
// pongo2.FromString without Execute, so a syntax error is the only
// possible source of a non-nil error from PongoParse — a future pongo2
// rephrasing of any specific message will not silently demote this to
// no-op.
func TestValidateChoice_PongoTemplateSyntaxError(t *testing.T) {
	el := ViewElement{
		Kind:           "choice",
		ChoiceMode:     "form",
		ChoiceIntent:   "do_thing",
		ChoiceTemplate: "{{ unclosed",
		ChoiceFields: []ChoiceField{
			{Name: "x", Type: "string"},
		},
		ChoiceRaw: json.RawMessage(`{
  "mode":"form",
  "intent":"do_thing",
  "template":"{{ unclosed",
  "fields":{"x":{"type":"string"}}
}`),
	}
	err := validateChoice(el)
	if err == nil {
		t.Fatalf("expected a syntax error from validateChoice")
	}
	if !strings.Contains(err.Error(), "template: pongo template syntax error") {
		t.Fatalf("expected the 'template: pongo template syntax error' prefix; got: %v", err)
	}
}

// TestValidateChoice_PongoRuntimeRefIsLoadOk pins the other side: a
// template that compiles but references identifiers the loader can't
// resolve (world.*, form.*, slots.*) MUST NOT fail at load. These are
// runtime concerns. The discriminator's structural guarantee is that
// undefined-identifier errors never reach validateChoice because
// PongoParse never calls Execute.
func TestValidateChoice_PongoRuntimeRefIsLoadOk(t *testing.T) {
	// Direct PongoParse check — undefined identifiers are an Execute-
	// time concern; parse must succeed.
	if err := render.PongoParse("{{ world.totally_undefined_var }}"); err != nil {
		t.Fatalf("PongoParse of a syntactically-valid template must succeed even for undefined refs; got: %v", err)
	}
}

// ---- P1-6: form field default/min/max must be scalar ------------------------

// TestLoad_Choice_FormField_NonScalarDefault asserts the JSON Schema
// rejects a non-scalar `default:` value. Authors hitting this case
// previously got a confusing runtime error from the renderer; we now
// trap at load.
func TestLoad_Choice_FormField_NonScalarDefault(t *testing.T) {
	yaml := []byte(`app:
  id: choice-nonscalar-default
  version: 0.1.0
root: room
intents:
  propose:
    title: "Propose"
    slots:
      qty:
        type: int
        required: true
states:
  room:
    view:
      - choice:
          mode: form
          intent: propose
          template: "Buy {qty}"
          fields:
            qty:
              type: int
              default: { not: scalar }
`)
	_, err := LoadBytes(yaml)
	if err == nil {
		t.Fatalf("expected schema rejection for non-scalar default")
	}
	if !strings.Contains(err.Error(), "default") {
		t.Fatalf("error did not name the offending property 'default': %v", err)
	}
}

// TestLoad_Choice_FormField_NonScalarMin asserts the JSON Schema
// rejects a sequence value for `min:`.
func TestLoad_Choice_FormField_NonScalarMin(t *testing.T) {
	yaml := []byte(`app:
  id: choice-nonscalar-min
  version: 0.1.0
root: room
intents:
  propose:
    title: "Propose"
    slots:
      qty:
        type: int
        required: true
states:
  room:
    view:
      - choice:
          mode: form
          intent: propose
          template: "Buy {qty}"
          fields:
            qty:
              type: int
              min: [1, 2]
`)
	_, err := LoadBytes(yaml)
	if err == nil {
		t.Fatalf("expected schema rejection for non-scalar min")
	}
	if !strings.Contains(err.Error(), "min") {
		t.Fatalf("error did not name the offending property 'min': %v", err)
	}
}

// TestLoad_Choice_FormField_ScalarMinMaxDefault confirms that the
// allowed scalar union (string / number / integer / boolean) still
// loads — this is the positive companion to the rejection cases above
// and guards against an over-zealous tightening that would break
// existing fixtures.
func TestLoad_Choice_FormField_ScalarMinMaxDefault(t *testing.T) {
	yaml := []byte(`app:
  id: choice-scalar-min-max
  version: 0.1.0
root: room
intents:
  propose:
    title: "Propose"
    slots:
      qty:
        type: int
        required: true
      name:
        type: string
        required: true
states:
  room:
    view:
      - choice:
          mode: form
          intent: propose
          template: "{name} buys {qty}"
          fields:
            name:
              type: string
              default: "anon"
            qty:
              type: int
              default: 1
              min: 1
              max: 9999
`)
	if _, err := LoadBytes(yaml); err != nil {
		t.Fatalf("scalar default/min/max must load; got: %v", err)
	}
}

// ---- P2-4: element-level vs inner choice.when precedence -------------------

// TestView_Choice_When_InnerWinsOverElementLevel pins the current
// precedence: if BOTH the element-level `when:` (the field that lives
// next to `choice:` in the rawViewElementYAML) AND a `when:` inside
// the choice subtree are set, the inner choice subtree value wins.
//
// The decoder writes the subtree value over the element-level field
// in (*rawChoiceYAML).resolve(), and the fallback in toElement() only
// restores the element-level value when the inner is empty. This
// behaviour is deliberate but undocumented in the proposal — pinning
// it in a test so a future refactor can't silently swap precedence.
func TestView_Choice_When_InnerWinsOverElementLevel(t *testing.T) {
	body := `view:
  - when: "world.outer"
    choice:
      mode: single
      when: "world.inner"
      items:
        - { label: "Only", intent: only_intent }
`
	v := unmarshalView(t, body)
	if len(v.Elements) != 1 {
		t.Fatalf("Elements len = %d; want 1", len(v.Elements))
	}
	if got, want := v.Elements[0].When, "world.inner"; got != want {
		t.Errorf("When = %q; want %q (inner choice.when must win when both are set)", got, want)
	}
}

// TestView_Choice_When_ElementLevelOnly confirms the element-level
// `when:` is honoured when the inner choice subtree omits it.
func TestView_Choice_When_ElementLevelOnly(t *testing.T) {
	body := `view:
  - when: "world.outer"
    choice:
      mode: single
      items:
        - { label: "Only", intent: only_intent }
`
	v := unmarshalView(t, body)
	if got, want := v.Elements[0].When, "world.outer"; got != want {
		t.Errorf("When = %q; want %q (element-level when must survive when inner is empty)", got, want)
	}
}

// TestView_Choice_When_InnerOnly confirms the inner-only case (the
// common authoring shape) reaches the element-level When field.
func TestView_Choice_When_InnerOnly(t *testing.T) {
	body := `view:
  - choice:
      mode: single
      when: "world.inner_only"
      items:
        - { label: "Only", intent: only_intent }
`
	v := unmarshalView(t, body)
	if got, want := v.Elements[0].When, "world.inner_only"; got != want {
		t.Errorf("When = %q; want %q (inner when must lift to ViewElement.When)", got, want)
	}
}

// ---- P1-8: mapToMapSlice produces sorted output ----------------------------

// TestMapToMapSlice_SortedKeys keeps the fallback-path output
// deterministic so tests that rely on the JSON re-marshal of a map
// literal don't flake under map-iteration nondeterminism.
func TestMapToMapSlice_SortedKeys(t *testing.T) {
	in := map[string]any{
		"zeta":  1,
		"alpha": 2,
		"mu":    3,
	}
	ms := mapToMapSlice(in)
	got := make([]string, 0, len(ms))
	for _, p := range ms {
		k, _ := p.Key.(string)
		got = append(got, k)
	}
	want := []string{"alpha", "mu", "zeta"}
	for i, k := range want {
		if got[i] != k {
			t.Errorf("key[%d] = %q; want %q (output must be sorted)", i, got[i], k)
		}
	}
}
