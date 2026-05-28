// Tests for [ParseList]. Structure follows int_test.go: a banner per
// behavioural block, table-driven sub-cases, then property tests at
// the bottom. Every sub-test runs in parallel.
package slotparse

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"kitsoki/internal/app"
)

// ====================== ParseList: happy paths ======================

func TestParseList_Int_HappyPaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		wantVals []any
		wantOK   bool
		wantPref string
	}{
		// Single item — no separator.
		{"single_digit", "6", []any{6}, true, "list:digit"},
		{"single_with_unit", "6 oxen", []any{6}, true, "list:digit"},
		{"single_spelled", "six", []any{6}, true, "list:spelled"},

		// Comma-separated (commas vanish on tokenisation — back-to-back).
		{"comma_three_digits", "6, 12, 3", []any{6, 12, 3}, true, "list:digit"},
		{"comma_two_digits", "6, 12", []any{6, 12}, true, "list:digit"},
		{"adjacent_no_separator", "6 12 3", []any{6, 12, 3}, true, "list:digit"},

		// "and"-separated.
		{"and_two", "6 and 12", []any{6, 12}, true, "list:digit"},
		{"and_three", "6 and 12 and 3", []any{6, 12, 3}, true, "list:digit"},

		// Mixed comma + and (the Oxford-style canonical form).
		{"comma_and", "6, 12, and 3", []any{6, 12, 3}, true, "list:digit"},

		// Trailing junk — list stops at first non-numeric non-"and" token.
		{"and_then_drink", "6 and 12 then drink", []any{6, 12}, true, "list:digit"},
		{"three_then_purple", "6 12 purple", []any{6, 12}, true, "list:digit"},

		// Leading filler skipped.
		{"please_buy_three", "please buy 6 and 12", []any{6, 12}, true, "list:digit"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseList(tok(t, tc.input), intParser{})
			if got.OK != tc.wantOK {
				t.Fatalf("ParseList(%q): OK=%v, want %v; result=%+v", tc.input, got.OK, tc.wantOK, got)
			}
			if !tc.wantOK {
				return
			}
			gotVals, ok := got.Value.([]any)
			if !ok {
				t.Fatalf("ParseList(%q): Value type %T, want []any", tc.input, got.Value)
			}
			if !reflect.DeepEqual(gotVals, tc.wantVals) {
				t.Errorf("ParseList(%q): Value=%v, want %v", tc.input, gotVals, tc.wantVals)
			}
			if !strings.HasPrefix(got.Reason, tc.wantPref) {
				t.Errorf("ParseList(%q): Reason=%q, want prefix %q", tc.input, got.Reason, tc.wantPref)
			}
			if len(got.Consumed) == 0 {
				t.Errorf("ParseList(%q): Consumed empty on OK=true (%+v)", tc.input, got)
			}
		})
	}
}

// ====================== ParseList: misses ======================

func TestParseList_Int_Misses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"only_filler", "please buy"},
		{"only_words", "purple cats sing"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseList(tok(t, tc.input), intParser{})
			if got.OK {
				t.Fatalf("ParseList(%q): want miss, got %+v", tc.input, got)
			}
			if tc.input != "" && got.Reason != "list:miss" {
				// Empty input short-circuits without setting Reason;
				// every other miss reports "list:miss" so trace
				// consumers can distinguish structural vs empty.
				t.Errorf("ParseList(%q): Reason=%q, want %q", tc.input, got.Reason, "list:miss")
			}
		})
	}
}

// ====================== ParseList: consumed range ======================

func TestParseList_ConsumedRangeStopsBeforeJunk(t *testing.T) {
	t.Parallel()
	// "6 and 12 then drink" — five tokens [6][and][12][then][drink].
	// We expect Consumed to cover at MOST through index 3 (i.e. token
	// "12" at index 2, half-open End=3). "and" is consumed between
	// items; "then" / "drink" are NOT.
	got := ParseList(tok(t, "6 and 12 then drink"), intParser{})
	if !got.OK {
		t.Fatalf("want OK, got miss: %+v", got)
	}
	var maxEnd int
	for _, r := range got.Consumed {
		if r.End > maxEnd {
			maxEnd = r.End
		}
	}
	if maxEnd > 3 {
		t.Errorf("Consumed extends to %d, want ≤ 3 (so 'then' / 'drink' aren't eaten); ranges=%+v", maxEnd, got.Consumed)
	}
}

// ====================== ParseList: enum dispatch via For ======================

func TestParseList_For_EnumList(t *testing.T) {
	t.Parallel()
	slot := app.Slot{
		Type:   "list[enum]",
		Values: []string{"oxen", "food", "bullets"},
		Synonyms: map[string][]string{
			"oxen":    {"ox"},
			"food":    {"grub"},
			"bullets": {"ammo"},
		},
	}
	parser := For(slot)
	if parser == nil {
		t.Fatalf("For(list[enum]) returned nil")
	}
	// Note: ParseEnum is value-oriented — it doesn't consume entire
	// runs the way ParseInt does on digit form. The list parser still
	// works because each successful enum parse advances the cursor by
	// one token; subsequent items are tested at the new cursor.
	got := parser.Parse(tok(t, "oxen and food"), slot)
	if !got.OK {
		t.Fatalf("list[enum] parse: want OK, got miss: %+v", got)
	}
	vals, ok := got.Value.([]any)
	if !ok {
		t.Fatalf("Value type %T, want []any", got.Value)
	}
	want := []any{"oxen", "food"}
	if !reflect.DeepEqual(vals, want) {
		t.Errorf("list[enum] vals = %v, want %v", vals, want)
	}
}

func TestParseList_For_MalformedListType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		typ  string
	}{
		{"empty_brackets", "list[]"},
		{"unclosed", "list[int"},
		{"no_prefix", "[int]"},
		{"unknown_inner", "list[banana]"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			slot := app.Slot{Type: tc.typ}
			if p := For(slot); p != nil {
				t.Errorf("For(%q) returned %T, want nil", tc.typ, p)
			}
		})
	}
}

// ====================== ParseList: property test ======================

// TestParseList_Property_CommaJoinInts proves the proposal's worked
// invariant: for any small slice of non-negative ints, joining them
// as a comma-and-space-separated string and parsing through
// ParseList(intParser) round-trips the slice.
func TestParseList_Property_CommaJoinInts(t *testing.T) {
	t.Parallel()
	corpora := [][]int{
		{6},
		{6, 12},
		{6, 12, 3},
		{0, 1, 2, 3},
		{200, 6, 12},
		{100, 200, 300, 400},
	}
	for _, ints := range corpora {
		ints := ints
		t.Run(fmt.Sprintf("len_%d_%v", len(ints), ints), func(t *testing.T) {
			t.Parallel()
			parts := make([]string, len(ints))
			for i, n := range ints {
				parts[i] = strconv.Itoa(n)
			}
			input := strings.Join(parts, ", ")

			got := ParseList(tok(t, input), intParser{})
			if !got.OK {
				t.Fatalf("ParseList(%q) miss; want OK", input)
			}
			vals, ok := got.Value.([]any)
			if !ok {
				t.Fatalf("Value type %T, want []any", got.Value)
			}
			if len(vals) != len(ints) {
				t.Fatalf("len(vals)=%d, want %d (input=%q, got=%+v)", len(vals), len(ints), input, got)
			}
			for i, v := range vals {
				if v != ints[i] {
					t.Errorf("vals[%d]=%v, want %v", i, v, ints[i])
				}
			}
		})
	}
}

// ====================== ParseList: leading-fails-vs-prefix-wins ======================

// TestParseList_PrefixWinsOnMidStreamFailure pins the "one element
// fails mid-stream" choice from the proposal: items that DID parse
// before the failure are kept, the failure ends the list. This is the
// cleaner of the two reasonable behaviours (the other being
// all-or-nothing) and the one the worked example explicitly calls out.
func TestParseList_PrefixWinsOnMidStreamFailure(t *testing.T) {
	t.Parallel()
	got := ParseList(tok(t, "6, 12, blue, 3"), intParser{})
	if !got.OK {
		t.Fatalf("want partial OK; got %+v", got)
	}
	vals, _ := got.Value.([]any)
	want := []any{6, 12}
	if !reflect.DeepEqual(vals, want) {
		t.Errorf("vals=%v, want %v (prefix-wins)", vals, want)
	}
}
