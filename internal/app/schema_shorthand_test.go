package app_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"

	"kitsoki/internal/app"
)

func TestShorthandToJSONSchema_AllSupportedTypes(t *testing.T) {
	// Every alias maps to its canonical JSON Schema type.
	cases := []struct {
		shorthand string
		want      string
	}{
		{"string", "string"},
		{"int", "integer"},
		{"integer", "integer"},
		{"number", "number"},
		{"float", "number"},
		{"bool", "boolean"},
		{"boolean", "boolean"},
		{"list", "array"},
		{"array", "array"},
		{"map", "object"},
		{"object", "object"},
	}
	for _, c := range cases {
		t.Run(c.shorthand, func(t *testing.T) {
			out, err := app.ShorthandToJSONSchema(map[string]string{"f": c.shorthand})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var doc map[string]any
			if err := json.Unmarshal(out, &doc); err != nil {
				t.Fatalf("output is not valid JSON: %v", err)
			}
			props, _ := doc["properties"].(map[string]any)
			f, _ := props["f"].(map[string]any)
			if got, _ := f["type"].(string); got != c.want {
				t.Fatalf("shorthand %q → type %q; want %q", c.shorthand, got, c.want)
			}
		})
	}
}

func TestShorthandToJSONSchema_CaseInsensitive(t *testing.T) {
	out, err := app.ShorthandToJSONSchema(map[string]string{"f": "INT"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(out), `"type":"integer"`) {
		t.Fatalf("expected integer type in %s", out)
	}
}

func TestShorthandToJSONSchema_UnknownType(t *testing.T) {
	_, err := app.ShorthandToJSONSchema(map[string]string{"total_cost": "money"})
	if err == nil {
		t.Fatal("expected error for unknown shorthand type")
	}
	if !strings.Contains(err.Error(), "total_cost") {
		t.Fatalf("error should name the offending field; got %q", err.Error())
	}
}

func TestShorthandToJSONSchema_Empty(t *testing.T) {
	out, err := app.ShorthandToJSONSchema(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if doc["type"] != "object" {
		t.Fatalf("expected type=object, got %v", doc["type"])
	}
	if doc["additionalProperties"] != false {
		t.Fatalf("expected additionalProperties=false, got %v", doc["additionalProperties"])
	}
	if _, ok := doc["required"]; ok {
		t.Fatalf("empty input should produce no required key, got %v", doc["required"])
	}
	if _, ok := doc["properties"]; ok {
		t.Fatalf("empty input should produce no properties key, got %v", doc["properties"])
	}
}

func TestShorthandToJSONSchema_Deterministic(t *testing.T) {
	in := map[string]string{
		"items":      "list",
		"total_cost": "int",
		"note":       "string",
	}
	a, err := app.ShorthandToJSONSchema(in)
	if err != nil {
		t.Fatalf("a: %v", err)
	}
	b, err := app.ShorthandToJSONSchema(in)
	if err != nil {
		t.Fatalf("b: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("non-deterministic output:\n  a=%s\n  b=%s", a, b)
	}

	// Same logical input with different insertion order should also match
	// — Go maps don't preserve insertion order, but rebuilding the map
	// from scratch keeps the test honest.
	in2 := map[string]string{
		"note":       "string",
		"total_cost": "int",
		"items":      "list",
	}
	c, err := app.ShorthandToJSONSchema(in2)
	if err != nil {
		t.Fatalf("c: %v", err)
	}
	if !bytes.Equal(a, c) {
		t.Fatalf("reordered input produced different output:\n  a=%s\n  c=%s", a, c)
	}
}

// Round-trip: compile the generated schema with the same library the MCP
// validator uses and confirm a valid payload passes, an invalid one fails.
func TestShorthandToJSONSchema_RoundTripWithValidator(t *testing.T) {
	schemaBytes, err := app.ShorthandToJSONSchema(map[string]string{
		"items":      "list",
		"total_cost": "int",
	})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}

	var doc any
	if err := json.Unmarshal(schemaBytes, &doc); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("shorthand.json", doc); err != nil {
		t.Fatalf("add resource: %v", err)
	}
	sch, err := compiler.Compile("shorthand.json")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	// Valid payload.
	valid := map[string]any{
		"items":      []any{"flour", "bacon"},
		"total_cost": 50,
	}
	if err := sch.Validate(valid); err != nil {
		t.Fatalf("expected valid payload to pass, got %v", err)
	}

	// Invalid payload (missing total_cost).
	invalid := map[string]any{
		"items": []any{"flour"},
	}
	if err := sch.Validate(invalid); err == nil {
		t.Fatal("expected payload missing required field to fail validation")
	}
}
