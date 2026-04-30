// Package harness — BuildTransitionSchema is the single source of truth for
// the transition-tool input schema. Both LiveHarness and ClaudeCLIHarness
// derive their tool schema from here so semantic formats declared on slots
// (e.g. `format: jql`) flow through to slot extraction in either harness.
package harness

import (
	"encoding/json"
	"fmt"

	"hally/internal/app"
)

// BuildTransitionSchema returns a JSON Schema (draft 2020-12) describing the
// arguments to the transition tool. The top-level shape is fixed:
//
//	{intent: <enum>, slots: {...}, confidence: number}
//
// Per-intent slot constraints live under $defs/slots__<name> and are wired
// into the top-level schema via `allOf` of `if/then` clauses keyed on
// `intent`. Intents with no slots are omitted from `allOf` (their slots
// default to the permissive `{"type":"object"}`).
//
// An empty allowedIntents list yields `intent.enum: []` and no per-intent
// branches — still a structurally valid schema.
func BuildTransitionSchema(appDef *app.AppDef, allowedIntents []string) ([]byte, error) {
	if appDef == nil {
		return nil, fmt.Errorf("harness/schema: appDef must not be nil")
	}

	enum := make([]any, 0, len(allowedIntents))
	for _, name := range allowedIntents {
		enum = append(enum, name)
	}

	defs := map[string]any{}
	allOf := []any{}

	for _, name := range allowedIntents {
		intent, ok := appDef.Intents[name]
		if !ok || len(intent.Slots) == 0 {
			continue
		}
		defKey := "slots__" + name
		defs[defKey] = buildSlotSchema(intent.Slots)
		allOf = append(allOf, map[string]any{
			"if": map[string]any{
				"properties": map[string]any{
					"intent": map[string]any{"const": name},
				},
			},
			"then": map[string]any{
				"properties": map[string]any{
					"slots": map[string]any{"$ref": "#/$defs/" + defKey},
				},
			},
		})
	}

	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"intent"},
		"properties": map[string]any{
			"intent": map[string]any{
				"type": "string",
				"enum": enum,
			},
			"slots": map[string]any{
				"type": "object",
			},
			"confidence": map[string]any{
				"type":    "number",
				"minimum": 0,
				"maximum": 1,
			},
		},
	}
	if len(allOf) > 0 {
		schema["allOf"] = allOf
	}
	if len(defs) > 0 {
		schema["$defs"] = defs
	}

	return json.Marshal(schema)
}

// buildSlotSchema renders a per-intent slots object schema from the slot map.
// additionalProperties is false so unknown slot keys are rejected; required
// holds slot names with Slot.Required == true.
func buildSlotSchema(slots map[string]app.Slot) map[string]any {
	props := map[string]any{}
	required := []any{}
	for name, slot := range slots {
		props[name] = buildSlotProperty(slot)
		if slot.Required {
			required = append(required, name)
		}
	}
	out := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

// buildSlotProperty renders one slot's JSON-Schema property.
func buildSlotProperty(slot app.Slot) map[string]any {
	prop := map[string]any{
		"type": mapSlotType(slot.Type),
	}
	if slot.Description != "" {
		prop["description"] = slot.Description
	}
	if len(slot.Examples) > 0 {
		examples := make([]any, len(slot.Examples))
		for i, ex := range slot.Examples {
			examples[i] = ex
		}
		prop["examples"] = examples
	}
	if len(slot.Values) > 0 {
		enum := make([]any, len(slot.Values))
		for i, v := range slot.Values {
			enum[i] = v
		}
		prop["enum"] = enum
	}
	if slot.Format != "" {
		prop["format"] = slot.Format
	}
	return prop
}

// mapSlotType maps a Slot.Type string to a JSON-Schema primitive type.
// Anything unrecognised (including the empty string and authoring shorthands
// like "enum") falls back to "string", which is the right default for the
// LLM-facing extraction surface.
func mapSlotType(t string) string {
	switch t {
	case "string":
		return "string"
	case "number":
		return "number"
	case "integer":
		return "integer"
	case "boolean":
		return "boolean"
	default:
		return "string"
	}
}
