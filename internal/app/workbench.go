// Package app — room workbench (`workbench:`) desugaring.
//
// See docs/proposals/room-workbench.md for the design rationale. A
// `workbench:` block on a State is a macro over four already-shipped
// primitives (write_mode, agent_off_ramp, on_enter host.agent.task,
// default_intent) that stories/dev-story/rooms/landing.yaml hand-rolls
// today. expandWorkbenches runs the desugaring at load time, producing
// concrete State fields + a synthesized top-level capture intent, so every
// downstream pass (roomDispatchesAgent / write-mode precondition, agent
// effect-taxonomy resolution, default_intent validation) sees the expanded
// shape and needs no workbench-specific knowledge of its own — mirroring
// how expandPhases (phases.go) precedes those same passes for `phases:`.
//
// A state with no `workbench:` block is byte-for-byte untouched: this pass
// is a pure no-op over it.
package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// expandWorkbenches walks def.States and desugars every non-nil
// State.Workbench into write_mode/agent_off_ramp/on_enter/default_intent (or
// a synthesized top-level capture intent registered in def.Intents), before
// the roomDispatchesAgent / write-mode precondition pass and before agent
// effect-taxonomy resolution run. Returns ValidationError-shaped errors for
// a missing required field or (when Plan is true) an acceptance_schema that
// doesn't embed the shared plan contract.
//
// If no state declares `workbench:` this is a no-op — states without the
// block are never touched.
func expandWorkbenches(def *AppDef, file string) []error {
	if def == nil {
		return nil
	}
	var errs []error
	var walk func(prefix string, states map[string]*State)
	walk = func(prefix string, states map[string]*State) {
		for _, name := range sortedKeys(states) {
			s := states[name]
			if s == nil {
				continue
			}
			statePath := joinPath(prefix, name)
			if s.Workbench != nil {
				if wErrs := expandOneWorkbench(def, file, statePath, name, s); len(wErrs) > 0 {
					errs = append(errs, wErrs...)
				}
			}
			if len(s.States) > 0 {
				walk(statePath, s.States)
			}
		}
	}
	walk("", def.States)
	return errs
}

// expandOneWorkbench desugars a single state's WorkbenchDecl in place. room
// is the state's own (leaf) name — the source for every synthesized name
// (`<room>_note`, `<room>_request`, `<room>_capture`), matching
// landing.yaml's landing_* naming exactly. statePath is the full dotted path,
// used only in error messages.
func expandOneWorkbench(def *AppDef, file, statePath, room string, s *State) []error {
	decl := s.Workbench
	var errs []error
	addErr := func(msg string) {
		errs = append(errs, &ValidationError{File: file, Message: fmt.Sprintf("state %q: workbench: %s", statePath, msg)})
	}

	if strings.TrimSpace(decl.Agent) == "" {
		addErr("agent is required")
	}
	if strings.TrimSpace(decl.Prompt) == "" {
		addErr("prompt is required")
	}
	if strings.TrimSpace(decl.AcceptanceSchema) == "" {
		addErr("acceptance_schema is required")
	}
	if len(errs) > 0 {
		// Don't desugar off a decl missing its required fields — every
		// synthesized primitive below depends on all three.
		return errs
	}

	captureSlot := decl.CaptureSlot
	if strings.TrimSpace(captureSlot) == "" {
		captureSlot = room + "_request"
	}
	noteKey := room + "_note"
	captureIntent := room + "_capture"

	offRampAgent := decl.OffRampAgent
	if strings.TrimSpace(offRampAgent) == "" {
		offRampAgent = decl.Agent
	}

	// 1. write_mode: read_only — the desugared room dispatches an agent via
	// the synthesized on_enter below, so every mutating tool call it
	// attempts holds for the operator's write-mode grant.
	s.WriteMode = WriteModeReadOnly

	// 2. agent_off_ramp — genuine Q&A that never reaches the capture intent
	// falls through to a converse turn instead of a rejection. enabled must
	// be set explicitly since this struct is built directly rather than
	// through OffRampDef.UnmarshalYAML (which is what sets it for
	// hand-authored agent_off_ramp: blocks); loader.go's post-pass nils any
	// *OffRampDef whose Enabled() is false, so leaving it unset would
	// silently discard the synthesized off-ramp.
	s.AgentOffRamp = &OffRampDef{Agent: offRampAgent, enabled: true}

	// 3. Synthesized on_enter host.agent.task, appended after any
	// hand-authored on_enter effects. Mirrors landing.yaml's on_enter
	// dispatch shape exactly (guard, once, working_dir, acceptance.schema,
	// context.prompt, bind, on_error).
	s.OnEnter = append(s.OnEnter, Effect{
		When:   fmt.Sprintf("world.%s != ''", captureSlot),
		Invoke: "host.agent.task",
		Once:   true,
		With: map[string]any{
			"agent":       decl.Agent,
			"working_dir": "{{ world.workdir }}",
			"acceptance": map[string]any{
				"schema": decl.AcceptanceSchema,
			},
			"context": map[string]any{
				"prompt": decl.Prompt,
				"args": map[string]any{
					"request": fmt.Sprintf("{{ world.%s }}", captureSlot),
				},
			},
		},
		Bind: map[string]string{
			noteKey: "submitted",
		},
		OnError: statePath,
	})

	// 4. Synthesized catch-all capture intent — the free-text sink. The
	// intent itself is registered top-level (def.Intents) so it resolves
	// through the same globalIntentDefs lookup default_intent validation
	// already uses; the room gets one `on:` arc that stores the utterance
	// into captureSlot and self-targets so on_enter re-dispatches on the
	// fresh request (mirrors landing.yaml's `work` arc).
	if def.Intents == nil {
		def.Intents = map[string]Intent{}
	}
	if _, exists := def.Intents[captureIntent]; !exists {
		def.Intents[captureIntent] = Intent{
			Title:       "Describe work",
			Description: fmt.Sprintf("Free-form work for the %q workbench agent to pick up.", room),
			Slots: map[string]Slot{
				"request": {Type: "string", Required: true, Description: "What you want the workbench agent to do."},
			},
		}
	}
	if s.On == nil {
		s.On = map[string][]Transition{}
	}
	if _, exists := s.On[captureIntent]; !exists {
		s.On[captureIntent] = []Transition{{
			Target: statePath,
			Effects: []Effect{{
				Set: map[string]any{
					captureSlot: "{{ slots.request }}",
					noteKey:     map[string]any{},
				},
			}},
		}}
	}
	s.DefaultIntent = captureIntent

	// 5. plan: true — load-time contract check that acceptance_schema
	// embeds the shared plan.json shape. The propose/accept/apply/verify
	// sub-loop rooms stay hand-authored (open question 2); this only
	// guards the schema contract they and the planner must agree on.
	if decl.Plan {
		if err := validateWorkbenchPlanContract(def, statePath, decl.AcceptanceSchema); err != nil {
			errs = append(errs, &ValidationError{File: file, Message: err.Error()})
		}
	}

	return errs
}

// validateWorkbenchPlanContract loads decl's acceptance_schema JSON file
// (resolved against def.BaseDir) and checks it declares a top-level "plan"
// object property whose required list is a superset of {goal, step, verify}
// — the shape stories/dev-story/schemas/plan.json declares. It does not
// check property-level detail beyond presence + requiredness: the schema is
// the contract's source of truth, this is only a load-time trip-wire against
// a workbench whose planner output shape doesn't match what the hand-authored
// applying/verifying rooms actually consume.
func validateWorkbenchPlanContract(def *AppDef, statePath, schemaPath string) error {
	resolved := schemaPath
	if def.BaseDir != "" && !filepath.IsAbs(schemaPath) {
		resolved = filepath.Join(def.BaseDir, schemaPath)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return fmt.Errorf("state %q: workbench: plan: true requires acceptance_schema %q to be readable: %w", statePath, schemaPath, err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("state %q: workbench: acceptance_schema %q is not valid JSON: %w", statePath, schemaPath, err)
	}
	props, _ := parsed["properties"].(map[string]any)
	planProp, ok := props["plan"].(map[string]any)
	if !ok {
		return fmt.Errorf("state %q: workbench: plan: true requires acceptance_schema %q to declare a top-level \"plan\" object property matching the shared plan.json contract (goal/step/verify)", statePath, schemaPath)
	}
	required, _ := planProp["required"].([]any)
	have := make(map[string]bool, len(required))
	for _, r := range required {
		if str, ok := r.(string); ok {
			have[str] = true
		}
	}
	for _, want := range []string{"goal", "step", "verify"} {
		if !have[want] {
			return fmt.Errorf("state %q: workbench: acceptance_schema %q's \"plan\" property must require %q (shared plan.json contract)", statePath, schemaPath, want)
		}
	}
	return nil
}
