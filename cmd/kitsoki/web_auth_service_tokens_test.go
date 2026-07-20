package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestResolveServiceTokens(t *testing.T) {
	t.Setenv("TEST_COLONY_TOKEN", "0123456789abcdef0123456789abcdef")
	var warn bytes.Buffer
	tokens, err := resolveServiceTokens(map[string]string{"colony": "TEST_COLONY_TOKEN", "other": "TEST_UNSET_TOKEN"}, &warn)
	if err != nil {
		t.Fatalf("resolveServiceTokens: %v", err)
	}
	if tokens["colony"] != "0123456789abcdef0123456789abcdef" {
		t.Errorf("colony token = %q", tokens["colony"])
	}
	if _, ok := tokens["other"]; ok {
		t.Error("unset env resolved to a token, want disabled")
	}
	if !strings.Contains(warn.String(), "TEST_UNSET_TOKEN") {
		t.Errorf("warning %q does not name the unset env var", warn.String())
	}

	t.Setenv("TEST_WEAK_TOKEN", "short")
	if _, err := resolveServiceTokens(map[string]string{"colony": "TEST_WEAK_TOKEN"}, &warn); err == nil {
		t.Fatal("weak token accepted, want startup error")
	}
}
