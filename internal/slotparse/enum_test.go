// Tests for [ParseEnum]. Structure mirrors int_test.go.
package slotparse

import (
	"strings"
	"testing"

	"kitsoki/internal/app"
)

// ====================== ParseEnum: happy path ======================

func TestParseEnum_AllTiers(t *testing.T) {
	t.Parallel()
	slot := fixtureProfessionSlot()
	tests := []struct {
		name       string
		input      string
		wantValue  string
		wantOK     bool
		reasonHead string // prefix of want Reason — exact tail is value-specific
	}{
		// Tier 1: direct match against slot.Values (Norm == stem).
		{"direct_banker", "banker", "banker", true, "direct:"},
		{"direct_carpenter", "carpenter", "carpenter", true, "direct:"},
		{"direct_farmer", "farmer", "farmer", true, "direct:"},
		// "bankers" → Porter2 stem "banker" → direct match (NOT fuzzy).
		// This is the pinning case noted in the spec.
		{"direct_plural_stems_to_banker", "bankers", "banker", true, "direct:"},

		// Tier 2: synonym phrase match.
		{"synonym_rich_guy", "rich guy", "banker", true, "synonym:"},
		{"synonym_money_man", "money man", "banker", true, "synonym:"},
		{"synonym_embedded_in_sentence", "i want to be the rich guy please", "banker", true, "synonym:"},
		{"synonym_farmhand", "farmhand", "farmer", true, "synonym:"},
		{"synonym_builder", "builder", "carpenter", true, "synonym:"},

		// Tier 3: DL-1 fuzzy.
		{"fuzzy_bankr", "bankr", "banker", true, "fuzzy:"},
		{"fuzzy_farmar", "farmar", "farmer", true, "fuzzy:"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseEnum(tok(t, tc.input), slot)
			if got.OK != tc.wantOK {
				t.Fatalf("ParseEnum(%q): OK=%v, want %v (got=%+v)", tc.input, got.OK, tc.wantOK, got)
			}
			if !tc.wantOK {
				return
			}
			if got.Value != tc.wantValue {
				t.Errorf("ParseEnum(%q): Value=%v, want %q (got=%+v)", tc.input, got.Value, tc.wantValue, got)
			}
			if !strings.HasPrefix(got.Reason, tc.reasonHead) {
				t.Errorf("ParseEnum(%q): Reason=%q, want prefix %q", tc.input, got.Reason, tc.reasonHead)
			}
			if len(got.Consumed) == 0 {
				t.Errorf("ParseEnum(%q): Consumed empty on OK=true", tc.input)
			}
		})
	}
}

// ====================== ParseEnum: misses ======================

func TestParseEnum_MissesAndEdgeCases(t *testing.T) {
	t.Parallel()
	slot := fixtureProfessionSlot()
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"only_stopwords", "the and please"},
		{"no_match_at_any_tier", "wizard"},
		{"no_match_long_phrase", "i would like to be a wizard please"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseEnum(tok(t, tc.input), slot)
			if got.OK {
				t.Errorf("ParseEnum(%q): want OK=false, got %+v", tc.input, got)
			}
		})
	}
}

// ====================== ParseEnum: ambiguity ======================

// TestParseEnum_AmbiguousDirectMatch encodes the proposal note:
// "ambiguity: a slot whose synonyms list a phrase that overlaps with
// another value's direct form → ambiguous Reason."
//
// We use values=[red, apple] with synonym {apple: ["red apple"]}.
// Input "red apple" matches both direct values at Tier 1; ambiguous.
func TestParseEnum_AmbiguousDirectMatch(t *testing.T) {
	t.Parallel()
	slot := app.Slot{
		Type:   "enum",
		Values: []string{"red", "apple"},
		Synonyms: map[string][]string{
			"apple": {"red apple"},
		},
	}
	got := ParseEnum(tok(t, "red apple"), slot)
	if got.OK {
		t.Errorf("ParseEnum(%q): want OK=false (ambiguous), got %+v", "red apple", got)
	}
	if got.Reason != "ambiguous" {
		t.Errorf("ParseEnum(%q): Reason=%q, want %q", "red apple", got.Reason, "ambiguous")
	}
}

// TestParseEnum_AmbiguousSynonymTier encodes the synonym-tier
// variant: two values each declare a synonym that fires on the same
// input. Lower tiers (fuzzy) must NOT be consulted; the parser
// returns ambiguous rather than falling through.
func TestParseEnum_AmbiguousSynonymTier(t *testing.T) {
	t.Parallel()
	slot := app.Slot{
		Type:   "enum",
		Values: []string{"alpha", "bravo"},
		Synonyms: map[string][]string{
			"alpha": {"first place"},
			"bravo": {"first place"},
		},
	}
	got := ParseEnum(tok(t, "first place"), slot)
	if got.OK || got.Reason != "ambiguous" {
		t.Errorf("ParseEnum(%q): want OK=false Reason=\"ambiguous\", got %+v", "first place", got)
	}
}

// TestParseEnum_DirectTierBeatsFuzzy pins the documented strategy
// order: a direct hit short-circuits the fuzzy tier. Without this,
// "bankers" might be classified "fuzzy:banker" (because the surface
// is one edit from "banker"); the Porter2 stem makes it a direct hit.
func TestParseEnum_DirectTierBeatsFuzzy(t *testing.T) {
	t.Parallel()
	slot := fixtureProfessionSlot()
	got := ParseEnum(tok(t, "bankers"), slot)
	if !got.OK || got.Value != "banker" {
		t.Fatalf("ParseEnum(%q): want banker, got %+v", "bankers", got)
	}
	if !strings.HasPrefix(got.Reason, "direct:") {
		t.Errorf("ParseEnum(%q): Reason=%q, want direct:* (stem collapses plural before fuzzy)", "bankers", got.Reason)
	}
}

// ====================== ParseEnum: property ======================

// TestParseEnum_EveryValueIsDirectMatch is the spec's property test:
// for every value v in slot.Values, ParseEnum(Tokenize(v), slot)
// returns Value=v with a direct: Reason.
func TestParseEnum_EveryValueIsDirectMatch(t *testing.T) {
	t.Parallel()
	slot := fixtureProfessionSlot()
	for _, v := range slot.Values {
		v := v
		t.Run("value="+v, func(t *testing.T) {
			t.Parallel()
			got := ParseEnum(tok(t, v), slot)
			if !got.OK {
				t.Fatalf("ParseEnum(%q): want OK=true (round-trip), got %+v", v, got)
			}
			if got.Value != v {
				t.Errorf("ParseEnum(%q): Value=%v, want %q", v, got.Value, v)
			}
			if !strings.HasPrefix(got.Reason, "direct:") {
				t.Errorf("ParseEnum(%q): Reason=%q, want direct:*", v, got.Reason)
			}
		})
	}
}

// TestParseEnum_EmptySlotIsMiss pins that ParseEnum returns OK=false
// for a slot with no Values — there is no answer to give.
func TestParseEnum_EmptySlotIsMiss(t *testing.T) {
	t.Parallel()
	slot := app.Slot{Type: "enum"} // Values nil
	got := ParseEnum(tok(t, "anything"), slot)
	if got.OK {
		t.Errorf("ParseEnum on empty-Values slot: want OK=false, got %+v", got)
	}
}

// ====================== ParseEnum: multi-word values ======================

// TestParseEnum_MultiWordValue_DirectMatch pins the M2 fix: a multi-
// word enum value matches the direct tier when its non-stopword
// stem-bag is a subset of the input's stem-bag. "lord of the rings"
// stems to {lord, ring} (the/of are stopwords); the input "watch lord
// of the rings tonight" stems to {lord, night, ring, watch, tonight,
// ...} which covers {lord, ring}.
func TestParseEnum_MultiWordValue_DirectMatch(t *testing.T) {
	t.Parallel()
	slot := app.Slot{
		Type:   "enum",
		Values: []string{"lord of the rings", "star wars"},
	}
	tests := []struct {
		name      string
		input     string
		wantValue string
	}{
		{"explicit_phrase", "lord of the rings", "lord of the rings"},
		{"embedded_in_sentence", "watch lord of the rings tonight", "lord of the rings"},
		{"reordered_stems", "the rings of lord", "lord of the rings"},
		{"second_value", "star wars marathon", "star wars"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseEnum(tok(t, tc.input), slot)
			if !got.OK {
				t.Fatalf("ParseEnum(%q): want OK=true, got %+v", tc.input, got)
			}
			if got.Value != tc.wantValue {
				t.Errorf("ParseEnum(%q): Value=%v, want %q (got=%+v)", tc.input, got.Value, tc.wantValue, got)
			}
			wantReason := "direct:" + tc.wantValue
			if got.Reason != wantReason {
				t.Errorf("ParseEnum(%q): Reason=%q, want %q", tc.input, got.Reason, wantReason)
			}
			if len(got.Consumed) == 0 {
				t.Errorf("ParseEnum(%q): Consumed empty on OK=true", tc.input)
			}
		})
	}
}

// TestParseEnum_MultiWordValue_RejectsPartial pins the M2 bug
// reproducer: "lord of the manor" against values ["lord of the
// rings"] used to match (the legacy first-stem-only direct tier
// short-circuited on "lord"). After the fix the input stem-bag
// {lord, manor} does NOT contain {lord, ring}, so no tier matches.
func TestParseEnum_MultiWordValue_RejectsPartial(t *testing.T) {
	t.Parallel()
	slot := app.Slot{
		Type:   "enum",
		Values: []string{"lord of the rings"},
	}
	tests := []struct {
		name  string
		input string
	}{
		{"lord_only", "lord"},
		{"lord_of_the_manor", "lord of the manor"},
		{"rings_only", "rings"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseEnum(tok(t, tc.input), slot)
			if got.OK {
				t.Errorf("ParseEnum(%q): want OK=false (insufficient stem-bag overlap), got %+v", tc.input, got)
			}
		})
	}
}

// TestParseEnum_MultiWordValue_AmbiguousStemBags pins that two
// multi-word values whose non-stopword stem-bags reduce to the same
// set ({lord} for both "the lord" and "lord lord lord") still fire
// the ambiguous branch — same algorithm as Tier 1 / Tier 2.
func TestParseEnum_MultiWordValue_AmbiguousStemBags(t *testing.T) {
	t.Parallel()
	slot := app.Slot{
		Type: "enum",
		// Both values reduce to non-stopword stem-bag {lord}, so any
		// input containing "lord" matches both → ambiguous.
		Values: []string{"the lord", "lord of the"},
	}
	got := ParseEnum(tok(t, "the lord rises"), slot)
	if got.OK {
		t.Errorf("ParseEnum(%q): want OK=false (ambiguous), got %+v", "the lord rises", got)
	}
	if got.Reason != "ambiguous" {
		t.Errorf("ParseEnum(%q): Reason=%q, want %q", "the lord rises", got.Reason, "ambiguous")
	}
}

// TestParseEnum_MultiWordValue_DisambiguatedByLongerInput pins that
// two values whose stem-bags differ only by one additional stem (e.g.
// "lord of the rings" → {lord, ring} vs "lord of the flies" →
// {fli, lord}) resolve cleanly when the input names the discriminator.
func TestParseEnum_MultiWordValue_DisambiguatedByLongerInput(t *testing.T) {
	t.Parallel()
	slot := app.Slot{
		Type:   "enum",
		Values: []string{"lord of the rings", "lord of the flies"},
	}
	tests := []struct {
		name      string
		input     string
		wantValue string
		wantOK    bool
	}{
		{"rings_disambiguates", "lord of the rings", "lord of the rings", true},
		{"flies_disambiguates", "lord of the flies", "lord of the flies", true},
		// "lord" alone fits NEITHER stem-bag (both need a second stem),
		// so no match — explicit miss, NOT ambiguous.
		{"bare_lord_misses", "lord", "", false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseEnum(tok(t, tc.input), slot)
			if got.OK != tc.wantOK {
				t.Fatalf("ParseEnum(%q): OK=%v want %v (got=%+v)", tc.input, got.OK, tc.wantOK, got)
			}
			if !tc.wantOK {
				return
			}
			if got.Value != tc.wantValue {
				t.Errorf("ParseEnum(%q): Value=%v, want %q", tc.input, got.Value, tc.wantValue)
			}
		})
	}
}

// TestParseEnum_MixedSingleAndMultiWordValues verifies that single-
// word values ("banker") and multi-word values ("rich guy") coexist
// in one slot's Values list without confusing the direct tier.
func TestParseEnum_MixedSingleAndMultiWordValues(t *testing.T) {
	t.Parallel()
	slot := app.Slot{
		Type:   "enum",
		Values: []string{"banker", "rich guy", "carpenter"},
	}
	tests := []struct {
		name      string
		input     string
		wantValue string
	}{
		{"single_word_banker", "banker", "banker"},
		{"multi_word_rich_guy", "rich guy", "rich guy"},
		{"multi_word_embedded", "be a rich guy please", "rich guy"},
		{"single_word_carpenter", "carpenter", "carpenter"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseEnum(tok(t, tc.input), slot)
			if !got.OK {
				t.Fatalf("ParseEnum(%q): want OK=true, got %+v", tc.input, got)
			}
			if got.Value != tc.wantValue {
				t.Errorf("ParseEnum(%q): Value=%v, want %q", tc.input, got.Value, tc.wantValue)
			}
			if got.Reason != "direct:"+tc.wantValue {
				t.Errorf("ParseEnum(%q): Reason=%q, want %q", tc.input, got.Reason, "direct:"+tc.wantValue)
			}
		})
	}
}

// TestParseEnum_MultiWordValueRoundTrip extends the
// every-value-parses-to-itself property test to multi-word values.
// Spec: ParseEnum(Tokenize(v), slot).Value == v for every v in
// slot.Values, regardless of v's token count.
func TestParseEnum_MultiWordValueRoundTrip(t *testing.T) {
	t.Parallel()
	slot := app.Slot{
		Type:   "enum",
		Values: []string{"banker", "lord of the rings", "star wars", "rich guy"},
	}
	for _, v := range slot.Values {
		v := v
		t.Run("value="+v, func(t *testing.T) {
			t.Parallel()
			got := ParseEnum(tok(t, v), slot)
			if !got.OK {
				t.Fatalf("ParseEnum(%q): want OK=true (round-trip), got %+v", v, got)
			}
			if got.Value != v {
				t.Errorf("ParseEnum(%q): Value=%v, want %q", v, got.Value, v)
			}
			if !strings.HasPrefix(got.Reason, "direct:") {
				t.Errorf("ParseEnum(%q): Reason=%q, want direct:*", v, got.Reason)
			}
		})
	}
}
