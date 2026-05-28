// Tests for [ParseBool]. Structure mirrors int_test.go.
package slotparse

import "testing"

// ====================== ParseBool: keyword tables ======================

func TestParseBool_YesKeywords(t *testing.T) {
	t.Parallel()
	yesKeywords := []string{"yes", "y", "true", "sure", "ok", "okay", "affirmative"}
	for _, kw := range yesKeywords {
		kw := kw
		t.Run("yes/"+kw, func(t *testing.T) {
			t.Parallel()
			got := ParseBool(tok(t, kw))
			if !got.OK {
				t.Fatalf("ParseBool(%q): want OK=true, got %+v", kw, got)
			}
			if got.Value != true {
				t.Errorf("ParseBool(%q): Value=%v, want true", kw, got.Value)
			}
			if got.Reason != kw {
				t.Errorf("ParseBool(%q): Reason=%q, want %q", kw, got.Reason, kw)
			}
		})
	}
}

func TestParseBool_NoKeywords(t *testing.T) {
	t.Parallel()
	noKeywords := []string{"no", "n", "false", "nope", "nah", "negative"}
	for _, kw := range noKeywords {
		kw := kw
		t.Run("no/"+kw, func(t *testing.T) {
			t.Parallel()
			got := ParseBool(tok(t, kw))
			if !got.OK {
				t.Fatalf("ParseBool(%q): want OK=true, got %+v", kw, got)
			}
			if got.Value != false {
				t.Errorf("ParseBool(%q): Value=%v, want false", kw, got.Value)
			}
			if got.Reason != kw {
				t.Errorf("ParseBool(%q): Reason=%q, want %q", kw, got.Reason, kw)
			}
		})
	}
}

// ====================== ParseBool: phrases & misses ======================

func TestParseBool_PhraseInputs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		input     string
		wantValue bool
		wantOK    bool
	}{
		{"please_yes_skips_stop", "please yes", true, true},
		{"first_yes_wins", "yes please no", true, true},
		// "maybe" is neither — explicit miss.
		{"maybe_is_miss", "maybe", false, false},
		// Multi-word phrase with no keyword falls through.
		{"no_keyword", "i would like to think about it", false, false},
		{"empty", "", false, false},
		{"only_stopwords", "the and please", false, false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseBool(tok(t, tc.input))
			if got.OK != tc.wantOK {
				t.Fatalf("ParseBool(%q): OK=%v, want %v (got=%+v)", tc.input, got.OK, tc.wantOK, got)
			}
			if !tc.wantOK {
				return
			}
			if got.Value != tc.wantValue {
				t.Errorf("ParseBool(%q): Value=%v, want %v", tc.input, got.Value, tc.wantValue)
			}
		})
	}
}

// ====================== ParseBool: property ======================

// TestParseBool_EveryYesKeywordReturnsTrue / EveryNoKeywordReturnsFalse
// are the spec's keyword-table invariants — every entry returns the
// documented value with no surprises.
func TestParseBool_EveryYesKeywordReturnsTrue(t *testing.T) {
	t.Parallel()
	for kw := range boolYesWords {
		kw := kw
		t.Run(kw, func(t *testing.T) {
			t.Parallel()
			got := ParseBool(tok(t, kw))
			if !got.OK || got.Value != true {
				t.Errorf("ParseBool(%q): want (true, true), got Value=%v OK=%v", kw, got.Value, got.OK)
			}
		})
	}
}

func TestParseBool_EveryNoKeywordReturnsFalse(t *testing.T) {
	t.Parallel()
	for kw := range boolNoWords {
		kw := kw
		t.Run(kw, func(t *testing.T) {
			t.Parallel()
			got := ParseBool(tok(t, kw))
			if !got.OK || got.Value != false {
				t.Errorf("ParseBool(%q): want (false, true), got Value=%v OK=%v", kw, got.Value, got.OK)
			}
		})
	}
}
