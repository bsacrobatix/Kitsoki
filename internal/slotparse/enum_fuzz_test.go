// Fuzz target for [ParseEnum]. Seeded with the proposal corpus plus
// inputs designed to exercise the three tiers.
//
// Invariant: no panics, ever. The parser MUST be defensive against
// adversarial slot data because [app.Slot] is YAML-authored.
package slotparse

import (
	"testing"

	"kitsoki/internal/app"
	"kitsoki/internal/lex"
)

// fuzzSlot is the slot fed to every fuzz input. The fuzz target
// doesn't vary slot shape — that's a separate concern; here we focus
// on input robustness with a stable slot.
var fuzzSlot = app.Slot{
	Type:   "enum",
	Values: []string{"banker", "carpenter", "farmer"},
	Synonyms: map[string][]string{
		"banker":    {"banker", "rich guy", "money man"},
		"carpenter": {"carpenter", "builder", "woodworker"},
		"farmer":    {"farmer", "farmhand"},
	},
}

var enumFuzzCorpus = []string{
	// Tier 1.
	"banker", "carpenter", "farmer", "BANKERS",
	// Tier 2.
	"rich guy", "money man", "builder",
	"i want to be the rich guy please",
	// Tier 3.
	"bankr", "carpenetr", "farmar",
	// Ambiguity.
	"red apple",
	// Pathologicals.
	"",
	" ",
	"\x00\x01control",
	"héllo wörld",
	"wizard",
}

func FuzzParseEnum(f *testing.F) {
	for _, s := range enumFuzzCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		// The only invariant we assert is "no panics" — semantic
		// invariants are encoded in the unit/property tests. We do
		// however verify the Reason whitelist so an unexpected Reason
		// (e.g. a typo in a future refactor) is caught here too.
		got := ParseEnum(lex.Tokenize(s, nil), fuzzSlot)
		if !got.OK {
			if got.Reason != "" && got.Reason != "ambiguous" {
				t.Fatalf("ParseEnum(%q): OK=false but Reason=%q (want empty or \"ambiguous\")", s, got.Reason)
			}
			return
		}
		// On a hit, Reason must start with one of three documented
		// prefixes and Value must be a string (the slot value).
		_, ok := got.Value.(string)
		if !ok {
			t.Fatalf("ParseEnum(%q): Value=%#v not a string", s, got.Value)
		}
		if !hasAnyPrefix(got.Reason, "direct:", "synonym:", "fuzzy:") {
			t.Fatalf("ParseEnum(%q): Reason=%q has no recognised prefix", s, got.Reason)
		}
	})
}

func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if len(s) >= len(p) && s[:len(p)] == p {
			return true
		}
	}
	return false
}
