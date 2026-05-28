package slotparse

import "kitsoki/internal/lex"

// boolYesWords maps surface keywords that mean "yes" to themselves —
// the key IS the Reason suffix. We test against Token.Surface (NOT
// Norm) so that "y" / "n" round-trip; Porter2 sometimes mangles
// single-letter inputs.
var boolYesWords = map[string]struct{}{
	"yes":         {},
	"y":           {},
	"true":        {},
	"sure":        {},
	"ok":          {},
	"okay":        {},
	"affirmative": {},
}

// boolNoWords mirrors boolYesWords for the negative form.
var boolNoWords = map[string]struct{}{
	"no":       {},
	"n":        {},
	"false":    {},
	"nope":     {},
	"nah":      {},
	"negative": {},
}

// ParseBool returns true / false / OK=false based on the first
// non-stop token whose surface appears in either keyword list. Order
// doesn't matter within a token sequence — we scan left to right and
// the first hit wins, so "maybe yes" parses as true. The matcher
// passes us pre-clipped windows already so this is rarely surprising.
//
// Reason is the exact matched keyword surface ("yes" / "n" /
// "affirmative" / …). Callers branching on Reason should match the
// concrete word, not "yes" vs "no" — that's what Value is for.
//
// "maybe" intentionally returns OK=false: it's neither a yes nor a no,
// and folding it into one would silently bias a clarification card.
func ParseBool(tokens []lex.Token) Result {
	for i, t := range tokens {
		if t.IsStop {
			continue
		}
		surf := t.Surface
		if _, ok := boolYesWords[surf]; ok {
			return Result{
				Value:    true,
				Consumed: []TokenRange{{Start: i, End: i + 1}},
				OK:       true,
				Reason:   surf,
			}
		}
		if _, ok := boolNoWords[surf]; ok {
			return Result{
				Value:    false,
				Consumed: []TokenRange{{Start: i, End: i + 1}},
				OK:       true,
				Reason:   surf,
			}
		}
	}
	return Result{}
}
