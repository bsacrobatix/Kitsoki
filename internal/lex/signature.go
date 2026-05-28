package lex

import (
	"crypto/sha1"
	"encoding/hex"
	"sort"
	"strconv"
)

// Signature returns a deterministic 16-hex-character (64-bit prefix of
// SHA-1) signature of the content tokens of s. The algorithm matches
// §3 of the semantic-routing proposal:
//
//  1. Tokenize(s, extraStops)
//  2. Drop stopwords, keep Token.Norm of the survivors.
//  3. For each surviving token, if its Surface (or Norm) is a single-
//     word spelled cardinal (via SpelledNumber), replace the entry with
//     the decimal string of the integer value. "six" → "6".
//  4. Sort + dedupe the resulting strings.
//  5. Return hex(sha1(strings.Join(content, " ")))[:16].
//
// Empty input — and input that produces no content tokens — returns the
// empty string. This is the stable sentinel callers use to skip the
// turncache when there's nothing to key on.
func Signature(s string, extraStops []string) string {
	tokens := Tokenize(s, extraStops)
	if len(tokens) == 0 {
		return ""
	}

	content := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if t.IsStop {
			continue
		}
		c := t.Norm
		// Numeric normalisation: spelled → digit. We try the Surface
		// because the stem of "six" is still "six" but SpelledNumber
		// accepts either; using Surface avoids a subtle case where
		// Porter2 might mangle a numeral surface.
		if n, ok := SpelledNumber([]string{t.Surface}); ok {
			c = strconv.Itoa(n)
		} else if n, ok := SpelledNumber([]string{t.Norm}); ok {
			c = strconv.Itoa(n)
		}
		content = append(content, c)
	}
	if len(content) == 0 {
		return ""
	}

	sort.Strings(content)
	content = dedupeSorted(content)

	sum := sha1.Sum([]byte(stringsJoinNorms(content)))
	return hex.EncodeToString(sum[:8]) // 8 bytes = 16 hex chars = 64 bits
}

// dedupeSorted returns ss with consecutive duplicates removed. ss is
// assumed to be sorted.
func dedupeSorted(ss []string) []string {
	if len(ss) < 2 {
		return ss
	}
	out := ss[:1]
	for _, s := range ss[1:] {
		if s == out[len(out)-1] {
			continue
		}
		out = append(out, s)
	}
	return out
}
