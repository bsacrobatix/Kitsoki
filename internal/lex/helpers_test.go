// Test-only helpers shared across lex_test.go, signature_test.go,
// lex_fuzz_test.go, and the package's *_bench_test.go files. Each helper
// calls t.Helper() so the failure line numbers point at the caller (the
// test body), not into this file.
package lex

import "testing"

// findToken returns the first token whose Surface matches surface and
// reports presence via a boolean. Callers should check ok before
// dereferencing the token.
func findToken(tb testing.TB, toks []Token, surface string) (Token, bool) {
	tb.Helper()
	for _, t := range toks {
		if t.Surface == surface {
			return t, true
		}
	}
	return Token{}, false
}

// surfaces collects Token.Surface in iteration order. Intended for use
// in error messages so a failure dumps the surface list the test was
// looking through.
func surfaces(toks []Token) []string {
	out := make([]string, len(toks))
	for i, t := range toks {
		out[i] = t.Surface
	}
	return out
}

// containsString reports whether ss contains s. Tiny helper to keep
// individual assertions readable.
func containsString(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
