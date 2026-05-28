// mcp_validator_test.go — covers the name-based positional schema
// dispatch (e.g. `kitsoki mcp-validator illness`) and the
// `--validate-once` one-shot verification mode that bypasses the
// stdio MCP server for CI checks.
package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestEmbeddedSchemas_IllnessRegistered asserts the illness schema is
// reachable by name (proposal §9.6).
func TestEmbeddedSchemas_IllnessRegistered(t *testing.T) {
	raw, ok := resolveEmbeddedSchema("illness")
	if !ok {
		t.Fatal("embedded schema 'illness' not registered")
	}
	// Spot-check the schema content so a future regression that points the
	// registry at the wrong file is caught loudly.
	s := string(raw)
	for _, want := range []string{
		"\"illness\"",
		"\"severity\"",
		"\"treatment\"",
		"dysentery", "cholera", "typhoid", "measles", "exhaustion",
		"medicine", "rest", "fluids", "none",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("illness.json missing %q\n--- schema ---\n%s", want, s)
		}
	}
}

// TestEmbeddedSchemas_UnknownNameRejected verifies the resolver returns
// (nil, false) for names not in the registry — the CLI surfaces this as
// a "Known: ..." error.
func TestEmbeddedSchemas_UnknownNameRejected(t *testing.T) {
	if data, ok := resolveEmbeddedSchema("nope"); ok || data != nil {
		t.Fatalf("expected (nil, false) for unknown schema; got (%q, %v)", data, ok)
	}
}

// TestRunValidateOnce_ConformingJSONExits0 confirms the one-shot
// validator accepts a payload matching the illness schema. This is the
// CI-style check used to verify the registered schema is enforceable
// end-to-end without spinning up the MCP server.
func TestRunValidateOnce_ConformingJSONExits0(t *testing.T) {
	raw, ok := resolveEmbeddedSchema("illness")
	if !ok {
		t.Fatal("setup: illness schema missing")
	}
	in := strings.NewReader(`{"illness":"cholera","severity":2,"treatment":"medicine"}`)
	var stdout, stderr bytes.Buffer
	if err := runValidateOnce(in, &stdout, &stderr, raw, "embedded:illness"); err != nil {
		t.Fatalf("runValidateOnce: unexpected error: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "validation passed") {
		t.Errorf("stdout missing 'validation passed': %q", stdout.String())
	}
}

// TestRunValidateOnce_RejectsNonConforming confirms an out-of-enum
// illness value (covid) fails schema validation. The error message is
// surfaced on stderr; the function returns a non-nil error so the CLI
// exits non-zero.
func TestRunValidateOnce_RejectsNonConforming(t *testing.T) {
	raw, ok := resolveEmbeddedSchema("illness")
	if !ok {
		t.Fatal("setup: illness schema missing")
	}
	in := strings.NewReader(`{"illness":"covid","severity":2,"treatment":"medicine"}`)
	var stdout, stderr bytes.Buffer
	if err := runValidateOnce(in, &stdout, &stderr, raw, "embedded:illness"); err == nil {
		t.Fatalf("runValidateOnce: expected error for non-enum illness, got nil. stdout=%q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "validation FAILED") {
		t.Errorf("stderr missing 'validation FAILED': %q", stderr.String())
	}
}

// TestRunValidateOnce_RejectsOutOfRangeSeverity catches the integer
// bound — severity 7 is above the schema's maximum of 5.
func TestRunValidateOnce_RejectsOutOfRangeSeverity(t *testing.T) {
	raw, _ := resolveEmbeddedSchema("illness")
	in := strings.NewReader(`{"illness":"typhoid","severity":7,"treatment":"rest"}`)
	var stdout, stderr bytes.Buffer
	if err := runValidateOnce(in, &stdout, &stderr, raw, "embedded:illness"); err == nil {
		t.Fatalf("runValidateOnce: expected error for severity=7, got nil")
	}
}

// TestRunValidateOnce_RejectsMissingRequired catches the required-field
// rule — every illness diagnosis must include all three keys.
func TestRunValidateOnce_RejectsMissingRequired(t *testing.T) {
	raw, _ := resolveEmbeddedSchema("illness")
	in := strings.NewReader(`{"illness":"measles","severity":3}`)
	var stdout, stderr bytes.Buffer
	if err := runValidateOnce(in, &stdout, &stderr, raw, "embedded:illness"); err == nil {
		t.Fatalf("runValidateOnce: expected error for missing 'treatment', got nil")
	}
}
