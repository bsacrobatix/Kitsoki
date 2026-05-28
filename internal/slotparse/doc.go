// Package slotparse holds the typed slot parsers consumed by the
// Phase-4 semantic matcher (semantic-routing proposal §2.4, §4.2,
// §5.2). Each parser is a small in-process grammar keyed off
// app.Slot.Type — they are NOT general-purpose NLP. The dialect they
// recognise is the one the proposal's worked examples and Oregon-Trail
// slot tests exercise.
//
// # Public surface
//
// All parsers share a [Result] / [Parser] interface so the matcher can
// dispatch off slot.Type without a type switch at every call site:
//
//	parser := slotparse.For(slot)
//	if parser == nil {
//	    // unsupported type (list / date — Phase 7); caller falls back
//	} else if r := parser.Parse(tokens, slot); r.OK {
//	    // r.Value carries the parsed value; r.Consumed records the
//	    // token-byte ranges that were eaten; r.Reason explains why.
//	}
//
// For callers that already know the target type, the direct entry
// points are exported too: [ParseInt], [ParseMoney], [ParseEnum],
// [ParseBool].
//
// # Per-parser strategy order
//
// Every parser advances left-to-right over the [lex.Token] slice the
// caller supplies. Stopwords (Token.IsStop) are skipped at the start
// but never midway — once a parser is "in" a numeric run or a synonym
// match it consumes contiguous tokens.
//
//   - [ParseInt] tries: (a) the first digit-form token; (b) failing
//     that, the maximal run of tokens whose surfaces feed [lex.SpelledNumber]
//     cleanly ("two hundred fifty" → 250). Reason is "digit" or "spelled".
//
//   - [ParseMoney] tries, in this order: (a) "$<digits>" — a literal
//     "$" token immediately followed by a digit-form token, with an
//     optional ".<digits>" decimal that rounds to the nearest dollar
//     via math.Round; (b) a digit-form token followed by
//     "dollar"/"dollars"/"buck"/"bucks"; (c) when slot.Type is "money"
//     or "$int", a bare digit token. Spelled+unit forms ("a few
//     hundred dollars") feed through SpelledNumber's largest-prefix
//     read and require a trailing unit word. Reason is one of
//     "dollar-sign" / "unit-dollars" / "unit-bucks" / "bare-int".
//
//   - [ParseEnum] tries three tiers, first hit wins:
//     (1) direct: any token's Norm equals a slot.Values entry's Norm;
//     (2) synonym: per-value synonym phrases from slot.Synonyms run
//     through a sorted-stem-set containment check against the input
//     tokens (the phrase's content stems must be a subset of the
//     input's content stems);
//     (3) fuzzy: any input token's Norm within Damerau-Levenshtein-1
//     of a slot.Values entry's Norm, via
//     github.com/agnivade/levenshtein.
//     Multiple matches at the same tier yield OK=false with
//     Reason="ambiguous"; lower tiers are NOT consulted when a higher
//     tier produced a unique hit.
//
//   - [ParseBool] is a flat keyword table. The yes-list is
//     {yes, y, true, sure, ok, okay, affirmative}; the no-list is
//     {no, n, false, nope, nah, negative}. Reason is the exact
//     keyword that matched.
//
// # What you don't get here
//
// list[T] and date parsers are explicitly Phase 7 (proposal §10);
// [For] returns nil for those types so callers can fall through.
// The matcher itself, and the orchestrator wiring that calls into
// it, live in internal/semroute (Phase 2 / Phase 4) and
// internal/orchestrator respectively.
package slotparse
