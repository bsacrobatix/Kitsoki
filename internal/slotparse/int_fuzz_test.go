// Fuzz target for [ParseInt]. Seeded with the corpus from the
// semantic-routing proposal worked examples plus a few pathological
// shapes (negatives, leading zeros, very long digit runs).
//
// Invariants (no panics is the bedrock; properties layered on top):
//   - Tokenize → ParseInt must never panic on any input.
//   - When OK is true:
//   - For positive-decimal inputs (Value > 0), the input must
//     contain at least one digit OR a recognised cardinal word.
//   - Value=0 is only legal when the input contains the surface
//     "0" or "zero".
//   - Reason must be one of {"digit", "spelled"} when OK=true.
package slotparse

import (
	"strings"
	"testing"

	"kitsoki/internal/lex"
)

// intFuzzCorpus mirrors lex's fuzzCorpus but adds int-specific seeds.
// Keep this list tight — fuzzing 200 starts adds time without coverage.
var intFuzzCorpus = []string{
	// Spec worked examples.
	"buy 6 oxen and 200 lbs of food",
	"buy six oxen",
	"two hundred fifty",
	"please buy six",

	// Edge digit shapes.
	"0", "1", "1000000", "200lbs", "lbs200",
	"-3", "3.14",

	// Spelled edge cases.
	"zero", "ten", "twenty", "nineteen",
	"two hundred and fifty",
	"one million two hundred thousand",

	// Pathologicals.
	"",
	" ",
	"the and please",
	"buy oxen",
	"\x00\x01control",
	"héllo wörld",
}

func FuzzParseInt(f *testing.F) {
	for _, s := range intFuzzCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := ParseInt(lex.Tokenize(s, nil))
		if !got.OK {
			return
		}
		// Reason whitelist.
		if got.Reason != "digit" && got.Reason != "spelled" {
			t.Fatalf("ParseInt(%q): Reason=%q, want one of {digit,spelled}", s, got.Reason)
		}
		// Value must be an int.
		n, isInt := got.Value.(int)
		if !isInt {
			t.Fatalf("ParseInt(%q): Value=%#v is not int", s, got.Value)
		}
		// ParseInt never returns negative ints — strconv.Atoi might
		// produce one, but a digit-form surface refuses the leading
		// "-" because isDigitFormSurface rejects it. Pin the floor.
		if n < 0 {
			t.Fatalf("ParseInt(%q): Value=%d < 0; parser must not return negatives", s, n)
		}
		// Value > 0 implies the input has SOME digit or a numeral word.
		if n > 0 && !containsDigitOrNumeral(s) {
			t.Fatalf("ParseInt(%q): Value=%d > 0 but input has no digits or numeral words", s, n)
		}
		// Value == 0 implies the input contains "0" or "zero".
		if n == 0 && !strings.Contains(strings.ToLower(s), "0") && !strings.Contains(strings.ToLower(s), "zero") {
			t.Fatalf("ParseInt(%q): Value=0 but input contains neither %q nor %q", s, "0", "zero")
		}
	})
}

// containsDigitOrNumeral reports whether s contains any ASCII digit
// or any of the cardinal word surfaces SpelledNumber recognises.
// Used by the fuzz invariant; intentionally tolerant of casing.
func containsDigitOrNumeral(s string) bool {
	low := strings.ToLower(s)
	for i := 0; i < len(low); i++ {
		c := low[i]
		if c >= '0' && c <= '9' {
			return true
		}
	}
	// Cheap substring check over a frozen list. We don't import the
	// numUnits / numScales maps to keep this file self-contained.
	for _, w := range []string{
		"zero", "one", "two", "three", "four", "five", "six",
		"seven", "eight", "nine", "ten", "eleven", "twelve",
		"thirteen", "fourteen", "fifteen", "sixteen", "seventeen",
		"eighteen", "nineteen", "twenty", "thirty", "forty", "fifty",
		"sixty", "seventy", "eighty", "ninety", "hundred", "thousand",
		"million", "billion",
	} {
		if strings.Contains(low, w) {
			return true
		}
	}
	return false
}
