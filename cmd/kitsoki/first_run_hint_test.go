package main

import "testing"

// TestFirstRunProviderHint verifies change 0.4: a fresh run with no provider
// gets an actionable message (not a silent replay fallback), and a run with a
// provider gets nothing.
func TestFirstRunProviderHint(t *testing.T) {
	if got := firstRunProviderHint(false, false); got == "" {
		t.Fatal("expected an actionable hint when no provider is configured, got empty")
	} else {
		for _, want := range []string{"no agent provider found", "ANTHROPIC_API_KEY", "claude", "harness profile", "replay"} {
			if !contains(got, want) {
				t.Errorf("hint missing %q; hint was:\n%s", want, got)
			}
		}
	}
	if got := firstRunProviderHint(true, false); got != "" {
		t.Errorf("expected no hint when claude is on PATH, got:\n%s", got)
	}
	if got := firstRunProviderHint(false, true); got != "" {
		t.Errorf("expected no hint when a credential is present, got:\n%s", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
