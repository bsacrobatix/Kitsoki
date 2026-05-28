// Tests for [ParseMoney]. Structure mirrors int_test.go.
package slotparse

import "testing"

// ====================== ParseMoney: happy path ======================

func TestParseMoney_AcceptedSurfaces(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		input     string
		wantValue int
		wantOK    bool
		wantReas  string
	}{
		// Plain digit + unit.
		{"digit_plus_dollars", "120 dollars", 120, true, "unit-dollars"},
		{"digit_plus_dollar_singular", "1 dollar", 1, true, "unit-dollars"},
		{"digit_plus_bucks", "120 bucks", 120, true, "unit-bucks"},
		{"digit_plus_buck_singular", "1 buck", 1, true, "unit-bucks"},

		// Dollar-sign branch. The live lex layer drops "$" as
		// pure-punctuation; the integer surface survives, so these
		// parse via the bare-int path. Pinned here so a future lex
		// change (preserving "$") shows up as a test break, not a
		// silent behaviour change.
		{"dollar_sign_120", "$120", 120, true, "bare-int"},
		{"dollar_sign_120_50_rounds_up", "$120.50", 121, true, "bare-int"},
		{"dollar_sign_120_49_rounds_down", "$120.49", 120, true, "bare-int"},

		// Bare integer (Strategy 4) — accepted because ParseMoney is
		// the direct entry point; in the matcher For(slot) only
		// hands this in for slot.Type == "money".
		{"bare_integer", "120", 120, true, "bare-int"},
		{"bare_integer_with_filler", "please 120", 120, true, "bare-int"},

		// Embedded in a longer phrase. The lex layer drops "$" as
		// pure-punctuation, so by the time tokens land here
		// "$240" is indistinguishable from the bare 240; the
		// parser picks up the FIRST IsNum token, which in the
		// proposal's "buy 6 oxen for $240" example is "6", not
		// "240". Pinned here so the limitation is visible.
		{"embedded_first_int_wins", "buy 6 oxen for $240", 6, true, "bare-int"},
		// Unit-dollars matches anywhere via the digit+unit scan,
		// so "240 dollars" embedded in a longer phrase still wins.
		{"embedded_unit_phrase", "I'd like to spend 240 dollars", 240, true, "unit-dollars"},

		// Spelled cardinal + unit. SpelledNumber returns 100 for
		// "hundred" alone; "a few hundred dollars" picks up that
		// 100-only window. Pinned per the proposal note: when the
		// parser meets a spelled+unit input, it takes the LARGEST
		// SpelledNumber-accepting prefix (here: "hundred"=100, not
		// "few hundred" since "few" is not a numeral).
		{"spelled_a_few_hundred_dollars", "a few hundred dollars", 100, true, "unit-dollars"},
		{"spelled_two_hundred_dollars", "two hundred dollars", 200, true, "unit-dollars"},
		{"spelled_two_hundred_fifty_bucks", "two hundred fifty bucks", 250, true, "unit-bucks"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseMoney(tok(t, tc.input))
			if got.OK != tc.wantOK {
				t.Fatalf("ParseMoney(%q): OK=%v, want %v (got=%+v)", tc.input, got.OK, tc.wantOK, got)
			}
			if !tc.wantOK {
				return
			}
			if got.Value != tc.wantValue {
				t.Errorf("ParseMoney(%q): Value=%v, want %v (got=%+v)", tc.input, got.Value, tc.wantValue, got)
			}
			if got.Reason != tc.wantReas {
				t.Errorf("ParseMoney(%q): Reason=%q, want %q", tc.input, got.Reason, tc.wantReas)
			}
			if len(got.Consumed) == 0 {
				t.Errorf("ParseMoney(%q): Consumed empty on OK=true", tc.input)
			}
		})
	}
}

// ====================== ParseMoney: misses ======================

func TestParseMoney_MissesAndEdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"only_stopwords", "the and please"},
		{"no_number_no_unit", "buy oxen"},
		// "200lbs" segments as a single non-numeric token; nothing to parse.
		{"mixed_alnum_200lbs", "200lbs"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseMoney(tok(t, tc.input))
			if got.OK {
				t.Errorf("ParseMoney(%q): want OK=false, got %+v", tc.input, got)
			}
		})
	}
}

// TestParseMoney_NonMoneyUnitFallsToBareInt encodes a subtle case:
// "200 lbs" has no money unit, but the bare-int strategy will still
// accept the leading integer. We pin this so callers know ParseMoney
// is permissive — they need to weigh the bare-int Reason against
// other context (intent type, slot validator) before submitting.
func TestParseMoney_NonMoneyUnitFallsToBareInt(t *testing.T) {
	t.Parallel()
	got := ParseMoney(tok(t, "200 lbs"))
	if !got.OK {
		t.Fatalf("ParseMoney(%q): want OK=true (bare-int fallback), got %+v", "200 lbs", got)
	}
	if got.Value != 200 || got.Reason != "bare-int" {
		t.Errorf("ParseMoney(%q): want Value=200 Reason=bare-int, got Value=%v Reason=%q", "200 lbs", got.Value, got.Reason)
	}
}

// TestParseMoney_DecimalRoundingMatrix encodes the math.Round rule.
// Half-away-from-zero is the rounding mode strconv-like callers expect;
// banker's rounding (round-half-to-even) would map .50 → 120, surprising
// users. The matrix below covers the four corners (just under .5 / .5 /
// just over .5 / well over .5).
func TestParseMoney_DecimalRoundingMatrix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  int
	}{
		{"120.00 dollars", 120},
		{"120.49 dollars", 120},
		{"120.50 dollars", 121},
		{"120.51 dollars", 121},
		{"120.99 dollars", 121},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got := ParseMoney(tok(t, tc.input))
			if !got.OK {
				t.Fatalf("ParseMoney(%q): want OK=true, got %+v", tc.input, got)
			}
			if got.Value != tc.want {
				t.Errorf("ParseMoney(%q): Value=%v, want %d (math.Round round-half-away-from-zero)", tc.input, got.Value, tc.want)
			}
		})
	}
}
