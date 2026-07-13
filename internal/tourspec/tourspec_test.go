package tourspec

import (
	"testing"

	"kitsoki/internal/tour"
)

func TestValidateAndCompile(t *testing.T) {
	s := &Spec{Version: 1, ID: "save-tour", Bindings: map[string]Binding{"save": {Target: tourTarget("save")}}, Claims: []Claim{{ID: "save", MustExpect: "saved"}}, Beats: []Beat{{ID: "intro", Kind: "highlight", Title: "Intro", Narration: "See save", Expect: []string{"saved"}, Next: []string{"save"}}, {ID: "save", Binding: "save", Kind: "gate", Title: "Save", Narration: "Click save", Expect: []string{"saved"}, AdvanceOn: "click"}}}
	if r := s.Validate(); !r.OK {
		t.Fatal(r.Errors)
	}
	m, d, e := s.Compile()
	if e != nil || d == "" || len(m.Steps) != 2 {
		t.Fatalf("compile %v %q %#v", e, d, m)
	}
}
func TestRejectsUnknownBindingAndUncoveredClaim(t *testing.T) {
	s := &Spec{Version: 1, ID: "x", Claims: []Claim{{ID: "c", MustExpect: "missing"}}, Beats: []Beat{{ID: "b", Binding: "no", Kind: "highlight", Title: "x", Narration: "x", Expect: []string{"seen"}}}}
	if s.Validate().OK {
		t.Fatal("accepted invalid spec")
	}
}
func tourTarget(id string) tour.TargetBundle { return tour.TargetBundle{TestID: id} }
