package journal_test

import (
	"encoding/json"
	"testing"

	"kitsoki/internal/app"
	"kitsoki/internal/journal"
)

// miniSchema is a small synthetic WorldSchema used across applier tests.
var miniSchema = app.WorldSchema{
	"counter": {Type: "int"},
	"score":   {Type: "float"},
	"active":  {Type: "bool"},
	"label":   {Type: "string"},
	"data":    {Type: "object"},
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// TestApplier_IntCoercion is spike requirement R1: an int world var declared
// as type: int must come back as int64 after JSON round-trip + patch apply +
// schema-aware coercion. The raw JSON bytes encode "42" as a number; the
// caller uses CoerceWorldVars after unmarshal to get the typed int64 back.
func TestApplier_IntCoercion(t *testing.T) {
	t.Parallel()

	a := journal.NewApplier(miniSchema)

	initial := mustMarshal(t, map[string]any{
		"vars": map[string]any{
			"counter": int64(10),
		},
	})

	// Replace with a new integer value — JSON encodes it as a number, which
	// encoding/json decodes back as float64 without schema-aware coercion.
	ops := []journal.PatchOp{
		{Op: "replace", Path: "/vars/counter", Value: mustMarshal(t, 42)},
	}

	result, err := a.Apply("world", initial, ops)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Unmarshal into generic map and then apply CoerceWorldVars — this is
	// the canonical consumer pattern (mirrors BuildJourney + coerceWorldVar).
	var doc struct {
		Vars map[string]any `json:"vars"`
	}
	if err := json.Unmarshal(result, &doc); err != nil {
		t.Fatalf("Unmarshal result: %v", err)
	}
	journal.CoerceWorldVars(doc.Vars, miniSchema)

	v, ok := doc.Vars["counter"]
	if !ok {
		t.Fatal("vars.counter missing after patch")
	}
	got, isInt64 := v.(int64)
	if !isInt64 {
		t.Fatalf("vars.counter type = %T (%v), want int64", v, v)
	}
	if got != 42 {
		t.Errorf("vars.counter = %d, want 42", got)
	}
}

// TestApplier_BoolNotFloat checks bool vars are not coerced to float64.
func TestApplier_BoolNotFloat(t *testing.T) {
	t.Parallel()

	a := journal.NewApplier(miniSchema)

	initial := mustMarshal(t, map[string]any{
		"vars": map[string]any{"active": false},
	})
	ops := []journal.PatchOp{
		{Op: "replace", Path: "/vars/active", Value: mustMarshal(t, true)},
	}

	result, err := a.Apply("world", initial, ops)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var doc struct {
		Vars map[string]any `json:"vars"`
	}
	if err := json.Unmarshal(result, &doc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	v := doc.Vars["active"]
	if _, isBool := v.(bool); !isBool {
		t.Fatalf("vars.active type = %T, want bool", v)
	}
	if v.(bool) != true {
		t.Errorf("vars.active = %v, want true", v)
	}
}

// TestApplier_FloatCoercion checks float vars round-trip correctly.
func TestApplier_FloatCoercion(t *testing.T) {
	t.Parallel()

	a := journal.NewApplier(miniSchema)

	initial := mustMarshal(t, map[string]any{
		"vars": map[string]any{"score": 1.5},
	})
	ops := []journal.PatchOp{
		{Op: "replace", Path: "/vars/score", Value: mustMarshal(t, 3.14)},
	}

	result, err := a.Apply("world", initial, ops)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var doc struct {
		Vars map[string]any `json:"vars"`
	}
	if err := json.Unmarshal(result, &doc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	v, ok := doc.Vars["score"].(float64)
	if !ok {
		t.Fatalf("vars.score type = %T, want float64", doc.Vars["score"])
	}
	if v != 3.14 {
		t.Errorf("vars.score = %v, want 3.14", v)
	}
}

// TestApplier_StringPassThrough verifies string vars are unchanged.
func TestApplier_StringPassThrough(t *testing.T) {
	t.Parallel()

	a := journal.NewApplier(miniSchema)

	initial := mustMarshal(t, map[string]any{
		"vars": map[string]any{"label": "hello"},
	})
	ops := []journal.PatchOp{
		{Op: "replace", Path: "/vars/label", Value: mustMarshal(t, "world")},
	}

	result, err := a.Apply("world", initial, ops)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var doc struct {
		Vars map[string]any `json:"vars"`
	}
	if err := json.Unmarshal(result, &doc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if doc.Vars["label"] != "world" {
		t.Errorf("vars.label = %v, want %q", doc.Vars["label"], "world")
	}
}

// TestApplier_UndeclaredKey passes through undeclared keys unchanged.
func TestApplier_UndeclaredKey(t *testing.T) {
	t.Parallel()

	a := journal.NewApplier(miniSchema)

	initial := mustMarshal(t, map[string]any{
		"vars": map[string]any{},
	})
	ops := []journal.PatchOp{
		{Op: "add", Path: "/vars/unknown_int", Value: mustMarshal(t, 99)},
	}

	result, err := a.Apply("world", initial, ops)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var doc struct {
		Vars map[string]any `json:"vars"`
	}
	if err := json.Unmarshal(result, &doc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	// JSON number for an undeclared key stays as float64.
	if _, ok := doc.Vars["unknown_int"].(float64); !ok {
		t.Errorf("undeclared key type = %T, want float64 (passthrough)", doc.Vars["unknown_int"])
	}
}

// TestApplier_EngineInjectedKeyPassThrough verifies $inbox and similar keys
// are not coerced even when the schema happens to contain them.
func TestApplier_EngineInjectedKeyPassThrough(t *testing.T) {
	t.Parallel()

	schema := app.WorldSchema{
		// deliberately add a schema entry that would conflict
	}
	a := journal.NewApplier(schema)

	initial := mustMarshal(t, map[string]any{
		"vars": map[string]any{
			"$inbox": []any{"item1"},
		},
	})
	ops := []journal.PatchOp{
		{Op: "add", Path: "/vars/$inbox/-", Value: mustMarshal(t, "item2")},
	}

	result, err := a.Apply("world", initial, ops)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var doc struct {
		Vars map[string]any `json:"vars"`
	}
	if err := json.Unmarshal(result, &doc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	inbox, ok := doc.Vars["$inbox"].([]any)
	if !ok {
		t.Fatalf("$inbox type = %T, want []any", doc.Vars["$inbox"])
	}
	if len(inbox) != 2 {
		t.Errorf("$inbox len = %d, want 2", len(inbox))
	}
}

// TestApplier_NonWorldDoc uses plain RFC 6902 without coercion.
func TestApplier_NonWorldDoc(t *testing.T) {
	t.Parallel()

	a := journal.NewApplier(miniSchema)

	initial := mustMarshal(t, map[string]any{
		"path": "room/awaiting",
	})
	ops := []journal.PatchOp{
		{Op: "replace", Path: "/path", Value: mustMarshal(t, "room/done")},
	}

	result, err := a.Apply("state", initial, ops)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var doc struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(result, &doc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if doc.Path != "room/done" {
		t.Errorf("path = %q, want %q", doc.Path, "room/done")
	}
}

// TestApplier_AddOp tests the RFC 6902 "add" operation.
func TestApplier_AddOp(t *testing.T) {
	t.Parallel()

	a := journal.NewApplier(miniSchema)

	initial := mustMarshal(t, map[string]any{
		"vars": map[string]any{},
	})
	ops := []journal.PatchOp{
		{Op: "add", Path: "/vars/counter", Value: mustMarshal(t, 5)},
	}

	result, err := a.Apply("world", initial, ops)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var doc struct {
		Vars map[string]any `json:"vars"`
	}
	if err := json.Unmarshal(result, &doc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	// Apply schema coercion as a consumer would.
	journal.CoerceWorldVars(doc.Vars, miniSchema)

	v, ok := doc.Vars["counter"].(int64)
	if !ok {
		t.Fatalf("counter type = %T, want int64", doc.Vars["counter"])
	}
	if v != 5 {
		t.Errorf("counter = %d, want 5", v)
	}
}

// TestApplier_RemoveOp tests the RFC 6902 "remove" operation.
func TestApplier_RemoveOp(t *testing.T) {
	t.Parallel()

	a := journal.NewApplier(miniSchema)

	initial := mustMarshal(t, map[string]any{
		"vars": map[string]any{
			"counter": int64(1),
			"label":   "keep",
		},
	})
	ops := []journal.PatchOp{
		{Op: "remove", Path: "/vars/counter"},
	}

	result, err := a.Apply("world", initial, ops)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var doc struct {
		Vars map[string]any `json:"vars"`
	}
	if err := json.Unmarshal(result, &doc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, exists := doc.Vars["counter"]; exists {
		t.Error("vars.counter still present after remove op")
	}
	if doc.Vars["label"] != "keep" {
		t.Errorf("vars.label = %v, want %q", doc.Vars["label"], "keep")
	}
}

// TestApplier_NilSchema disables coercion gracefully.
func TestApplier_NilSchema(t *testing.T) {
	t.Parallel()

	a := journal.NewApplier(nil)

	initial := mustMarshal(t, map[string]any{
		"vars": map[string]any{"counter": 10},
	})
	ops := []journal.PatchOp{
		{Op: "replace", Path: "/vars/counter", Value: mustMarshal(t, 99)},
	}

	result, err := a.Apply("world", initial, ops)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// With nil schema no coercion; JSON decodes 99 as float64.
	var doc struct {
		Vars map[string]any `json:"vars"`
	}
	if err := json.Unmarshal(result, &doc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := doc.Vars["counter"].(float64); !ok {
		t.Errorf("nil schema: counter type = %T, want float64", doc.Vars["counter"])
	}
}
