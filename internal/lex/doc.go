// Package lex provides tokenisation, normalisation, Porter2 stemming,
// stopword filtering, and a deterministic lexical signature for use by
// the semantic-routing subsystem (internal/semroute, internal/turncache,
// internal/slotparse).
//
// The package is intentionally state-free: every function is pure and
// deterministic across processes. The only inputs are the source string
// and an optional per-app "extraStops" list.
//
// Pipeline (per Tokenize):
//
//  1. NFKC-normalise the input via golang.org/x/text/unicode/norm — this
//     collapses fullwidth digits, ligatures and compatibility forms so
//     downstream stages see a canonical UTF-8 string.
//  2. Lowercase via golang.org/x/text/cases with language.English.
//  3. Segment into words using UAX#29 word boundaries
//     (github.com/clipperhouse/uax29/v2/words).
//  4. Drop pure-whitespace / pure-punctuation segments; keep alphanumerics
//     and any segment that contains at least one letter or digit.
//  5. Stem alphabetic surfaces via Porter2
//     (github.com/kljensen/snowball, "english", true).
//  6. Flag IsStop using the union of the builtin stopword list and the
//     caller's extraStops slice.
//  7. Flag IsNum when the surface is a digit form OR a single-word
//     spelled cardinal recognised by SpelledNumber.
//
// Token.Start and Token.End are byte offsets into the NFKC-normalised
// source — they are not offsets into the raw caller-provided string,
// which may have a different byte length. Callers that need to echo the
// exact original text should keep their own raw input; the Surface field
// is the segment of the normalised string.
package lex
