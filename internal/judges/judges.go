// Package judges is the contract-encoder for LLM-judge verdicts in the
// dev-story / bugfix pipeline.
//
// The package is intentionally tiny: it owns one canonical place where
// the "is this verdict auto-fireable?" rule lives, so contract §6's gate
// clause in stories/bugfix/rooms/*.yaml, the story flow tests, and any
// future tooling all agree on the same semantics. See
// docs/proposals/notes/dev-story-implementation-contract.md §5 (verdict
// schema), §6 (canonical checkpoint shape), and proposal §4.4
// (judge polymorphism).
//
// The package does NOT register a host handler — that is Slice β's
// territory. It does NOT execute the LLM call — host.oracle.ask_with_mcp
// already does that (with MCP-side schema enforcement). This is the
// thin typed convenience layer callers use to interpret the result.
package judges

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// Verdict is the typed shape of a judge's structured response. Matches
// stories/bugfix/schemas/judge_verdict.json verbatim. Slice α owns the
// canonical on-disk copy of that schema; the const below is the source
// of truth inside this package so the tests do not reach across the
// filesystem.
type Verdict struct {
	Verdict    string  `json:"verdict"`    // "pass" | "fail" | "uncertain"
	Intent     string  `json:"intent"`     // "accept" | "refine" | "restart_from" | "quit" | "uncertain"
	Reason     string  `json:"reason"`
	Confidence float64 `json:"confidence"`
}

// schemaJSON mirrors §5 of
// docs/proposals/notes/dev-story-implementation-contract.md. Keep this
// in lockstep with stories/bugfix/schemas/judge_verdict.json — the
// contract document is the source of truth; if it ever drifts, update
// this constant.
const schemaJSON = `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title":   "judge_verdict",
  "type":    "object",
  "required": ["verdict", "intent", "reason", "confidence"],
  "properties": {
    "verdict":    { "type": "string", "enum": ["pass", "fail", "uncertain"] },
    "intent":     { "type": "string", "enum": ["accept", "refine", "restart_from", "quit", "uncertain"] },
    "reason":     { "type": "string", "minLength": 4 },
    "confidence": { "type": "number", "minimum": 0.0, "maximum": 1.0 }
  },
  "additionalProperties": false
}`

// compiledSchema is the parsed, compiled judge_verdict schema. Compiled
// once at package init so Parse() is cheap. A compile-time failure is a
// programmer error in this package (drifted constant) — panic loudly
// rather than carry a "validator unavailable" error path.
var compiledSchema = mustCompileSchema()

func mustCompileSchema() *jsonschema.Schema {
	var probe any
	if err := json.Unmarshal([]byte(schemaJSON), &probe); err != nil {
		panic(fmt.Sprintf("judges: embedded schema is malformed JSON: %v", err))
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("judge_verdict.json", probe); err != nil {
		panic(fmt.Sprintf("judges: register embedded schema: %v", err))
	}
	s, err := c.Compile("judge_verdict.json")
	if err != nil {
		panic(fmt.Sprintf("judges: compile embedded schema: %v", err))
	}
	return s
}

// ErrMalformedJSON is returned when the raw payload is not valid JSON.
// Callers can errors.Is against this to route uncertain / malformed
// verdicts to a human bail-out per proposal §4.4 (llm_then_human mode).
var ErrMalformedJSON = errors.New("judges: malformed JSON")

// ErrSchemaViolation is returned when the payload is valid JSON but
// fails judge_verdict schema validation (missing required field, wrong
// enum value, out-of-range confidence, etc.). Callers can errors.Is
// against this to distinguish schema failures from transport failures.
var ErrSchemaViolation = errors.New("judges: schema violation")

// Parse validates raw JSON output from an LLM judge call against the
// canonical judge_verdict schema and returns a typed Verdict. Returns
// a structured error wrapping ErrMalformedJSON or ErrSchemaViolation
// on failure so callers can route uncertain / malformed verdicts to a
// human bail-out per proposal §4.4 (llm_then_human mode).
func Parse(raw []byte) (Verdict, error) {
	var probe any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&probe); err != nil {
		return Verdict{}, fmt.Errorf("%w: %v", ErrMalformedJSON, err)
	}
	if err := compiledSchema.Validate(probe); err != nil {
		return Verdict{}, fmt.Errorf("%w: %v", ErrSchemaViolation, err)
	}
	var v Verdict
	if err := json.Unmarshal(raw, &v); err != nil {
		// Schema passed but Go unmarshal failed — shouldn't happen, but
		// surface as malformed rather than panicking.
		return Verdict{}, fmt.Errorf("%w: %v", ErrMalformedJSON, err)
	}
	return v, nil
}

// ShouldAutoFire returns true when the verdict meets the confidence
// threshold and is not uncertain. Encodes the gate clause from contract
// §6 in one place so flows and tests agree on the rule:
//
//	world.judge_mode != 'human' &&
//	world.llm_verdict.confidence >= world.judge_confidence_threshold &&
//	world.llm_verdict.verdict != 'uncertain' &&
//	world.llm_verdict.intent != 'uncertain'
//
// The judge_mode check is the caller's responsibility (this package
// doesn't know about modes); the three remaining clauses are owned
// here so the YAML, the flow harness, and any future tooling agree.
// Comparison uses >= per contract §6.
func (v Verdict) ShouldAutoFire(threshold float64) bool {
	if v.Confidence < threshold {
		return false
	}
	if v.Verdict == "uncertain" || v.Intent == "uncertain" {
		return false
	}
	return true
}

// AutoFireIntent returns the intent the auto-fire effect should emit.
// Defined as a method (rather than a bare field read) so future fanned-
// out logic (e.g. mapping `restart_from` with a stage slot) stays
// inside this package.
func (v Verdict) AutoFireIntent() string {
	return v.Intent
}
