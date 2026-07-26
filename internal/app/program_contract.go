package app

import (
	"fmt"
	"sort"
	"strings"
)

// expandRoomTemplates instantiates phase_templates as compound room graphs.
// It is deliberately a thin adapter over substituteState/substituteStateName:
// the same parameter and substitution semantics therefore drive phases and
// parameterized application rooms.
func expandRoomTemplates(def *AppDef, file string) []error {
	if def == nil {
		return nil
	}
	var errs []error
	for _, roomID := range sortedKeys(def.States) {
		room := def.States[roomID]
		if room == nil || room.RoomTemplate == "" {
			continue
		}
		roomErrStart := len(errs)
		path := fmt.Sprintf("states.%s", roomID)
		templateName := room.RoomTemplate
		template := def.PhaseTemplates[templateName]
		if template == nil {
			errs = append(errs, &ValidationError{File: file, Message: fmt.Sprintf("%s.room_template %q is not declared in phase_templates", path, templateName)})
			continue
		}
		if room.Type != "" && room.Type != "compound" {
			errs = append(errs, &ValidationError{File: file, Message: fmt.Sprintf("%s: room_template requires type compound (got %q)", path, room.Type)})
			continue
		}

		params := make(map[string]any, len(template.Parameters)+len(room.RoomParameters)+1)
		for name, parameter := range template.Parameters {
			if parameter.Default != nil {
				params[name] = parameter.Default
			}
		}
		for name, value := range room.RoomParameters {
			if name != "id" {
				if _, declared := template.Parameters[name]; !declared {
					errs = append(errs, &ValidationError{File: file, Message: fmt.Sprintf("%s.room_parameters.%s is not declared by phase_template %q", path, name, templateName)})
					continue
				}
			}
			params[name] = value
		}
		if _, set := params["id"]; !set {
			params["id"] = roomID
		}
		for _, name := range sortedKeys(template.Parameters) {
			parameter := template.Parameters[name]
			value, present := params[name]
			if parameter.Required && !present {
				errs = append(errs, &ValidationError{File: file, Message: fmt.Sprintf("%s.room_parameters: missing required template parameter %q", path, name)})
				continue
			}
			if present && !roomParameterTypeMatches(parameter.Type, value) {
				errs = append(errs, &ValidationError{File: file, Message: fmt.Sprintf("%s.room_parameters.%s has type %T, want %s", path, name, value, parameter.Type)})
			}
		}
		if len(errs) > roomErrStart {
			continue
		}
		if room.States == nil {
			room.States = map[string]*State{}
		}
		for _, templateStateName := range sortedKeys(template.States) {
			expandedName, err := substituteStateName(templateStateName, params)
			if err != nil {
				errs = append(errs, &ValidationError{File: file, Message: fmt.Sprintf("%s room_template state %q: %v", path, templateStateName, err)})
				continue
			}
			if _, exists := room.States[expandedName]; exists {
				// A hand-authored child is an explicit whole-state override,
				// mirroring phase expansion.
				continue
			}
			instance, err := substituteState(template.States[templateStateName], params, nil)
			if err != nil {
				errs = append(errs, &ValidationError{File: file, Message: fmt.Sprintf("%s room_template state %q: %v", path, templateStateName, err)})
				continue
			}
			room.States[expandedName] = instance
		}
		if room.Initial == "" {
			errs = append(errs, &ValidationError{File: file, Message: fmt.Sprintf("%s.initial is required for a parameterized room", path)})
			continue
		}
		room.Initial = substString(room.Initial, params, nil)
		if _, ok := room.States[room.Initial]; !ok {
			errs = append(errs, &ValidationError{File: file, Message: fmt.Sprintf("%s.initial %q is not produced by room_template %q", path, room.Initial, templateName)})
			continue
		}
		room.Type = "compound"
		room.InstantiatedFrom = templateName
		room.InstantiatedParameters = params
		room.RoomTemplate = ""
		room.RoomParameters = nil
	}
	return errs
}

func roomParameterTypeMatches(want string, value any) bool {
	switch want {
	case "", "any":
		return true
	case "string":
		_, ok := value.(string)
		return ok
	case "bool", "boolean":
		_, ok := value.(bool)
		return ok
	case "int", "integer":
		switch value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return true
		default:
			return false
		}
	case "number", "float":
		switch value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
			return true
		default:
			return false
		}
	case "map", "object":
		_, ok := value.(map[string]any)
		return ok
	case "list", "array":
		switch value.(type) {
		case []any, []string:
			return true
		default:
			return false
		}
	default:
		return false
	}
}

// validateFiniteProgramContracts verifies opt-in room/outcome contracts and
// guard totality for intents exposed through application actions, handlers,
// events, or room interfaces.
func validateFiniteProgramContracts(def *AppDef, file string) []error {
	if def == nil {
		return nil
	}
	var errs []error
	add := func(message string) {
		errs = append(errs, &ValidationError{File: file, Message: message})
	}
	validateOptInWorldWrites(def, add)

	implementors := map[string][]string{}
	for name, contract := range def.RoomInterfaces {
		path := "room_interfaces." + name
		if strings.TrimSpace(name) == "" {
			add("room_interfaces: interface name must not be empty")
		}
		if contract == nil {
			add(path + ": empty definition")
			continue
		}
		for field, world := range contract.World {
			if strings.TrimSpace(world.Type) == "" {
				add(fmt.Sprintf("%s.world.%s.type is required", path, field))
			}
			switch world.Access {
			case "", "read", "write", "readwrite":
			default:
				add(fmt.Sprintf("%s.world.%s.access %q is not one of read|write|readwrite", path, field, world.Access))
			}
		}
	}

	var collectImplementors func(string, map[string]*State, bool)
	collectImplementors = func(prefix string, states map[string]*State, insideImport bool) {
		for _, stateID := range sortedKeys(states) {
			state := states[stateID]
			if state == nil {
				continue
			}
			path := joinPath(prefix, stateID)
			imported := insideImport || state.ImportAlias != ""
			if prefix != "" && !imported && len(state.Implements) > 0 {
				add(fmt.Sprintf("state %q: implements is only valid on a top-level room or imported room", path))
			}
			if prefix == "" || imported {
				for _, interfaceName := range state.Implements {
					contract := def.RoomInterfaces[interfaceName]
					if contract == nil {
						add(fmt.Sprintf("state %q implements unknown room_interface %q", path, interfaceName))
						continue
					}
					implementors[interfaceName] = append(implementors[interfaceName], path)
					validateRoomImplementation(def, path, state, interfaceName, contract, add)
				}
			}
			collectImplementors(path, state.States, imported)
		}
	}
	collectImplementors("", def.States, false)
	for name := range implementors {
		sort.Strings(implementors[name])
	}

	resolveDispatch := func(path string, dispatch *ApplicationHandlerDispatch) {
		if dispatch == nil || dispatch.RoomInterface == "" {
			return
		}
		candidates := implementors[dispatch.RoomInterface]
		if len(candidates) == 0 {
			add(fmt.Sprintf("%s.room_interface %q has no verified implementor", path, dispatch.RoomInterface))
			return
		}
		if dispatch.State == "" {
			if len(candidates) != 1 {
				add(fmt.Sprintf("%s.room_interface %q has multiple implementors %v; state is required for deterministic dispatch", path, dispatch.RoomInterface, candidates))
				return
			}
			dispatch.State = candidates[0]
		}
		if !containsProgramString(candidates, dispatch.State) {
			add(fmt.Sprintf("%s.state %q does not implement room_interface %q", path, dispatch.State, dispatch.RoomInterface))
		}
	}
	if def.Application != nil {
		for actionID, action := range def.Application.Actions {
			if action == nil || action.RoomInterface == "" {
				continue
			}
			dispatch := &ApplicationHandlerDispatch{
				Intent: action.Intent, State: action.State, RoomInterface: action.RoomInterface,
			}
			resolveDispatch("application.actions."+actionID, dispatch)
			action.State = dispatch.State
		}
	}
	if def.Exports != nil {
		for handlerID, handler := range def.Exports.Handlers {
			if handler != nil {
				resolveDispatch("exports.handlers."+handlerID+".dispatch", handler.Dispatch)
			}
		}
	}
	for eventID, event := range def.Events {
		if event != nil {
			resolveDispatch("events."+eventID+".dispatch", event.Dispatch)
		}
	}

	targets := collectFiniteIntentTargets(def)
	for _, target := range targets {
		state, ok := def.LookupState(StatePath(target.state))
		if !ok || state == nil {
			continue
		}
		transitions := state.On[target.intent]
		if len(transitions) == 0 {
			continue
		}
		validateGuardPartition(target.state, target.intent, transitions, add)
	}
	validateEffectContracts(def, add)
	return errs
}

func validateOptInWorldWrites(def *AppDef, add func(string)) {
	if def == nil || !hasFiniteApplicationContract(def) {
		return
	}
	walkAllEffects(def.States, func(location string, effect Effect) {
		writes := map[string]struct{}{}
		for key := range effect.Set {
			writes[key] = struct{}{}
		}
		for key := range effect.Increment {
			writes[key] = struct{}{}
		}
		for key := range effect.Bind {
			writes[key] = struct{}{}
		}
		for _, key := range sortedKeys(writes) {
			if _, reserved := ReservedWorldKeys[key]; reserved {
				continue
			}
			if _, declared := def.World[key]; !declared {
				add(fmt.Sprintf("%s: effect writes undeclared world field %q", location, key))
			}
		}
	})
}

func hasFiniteApplicationContract(def *AppDef) bool {
	if def.Application != nil || len(def.RoomInterfaces) > 0 {
		return true
	}
	found := false
	walkAllEffects(def.States, func(_ string, effect Effect) {
		if len(effect.Outcomes) > 0 {
			found = true
		}
	})
	return found
}

func validateRoomImplementation(def *AppDef, roomID string, room *State, interfaceName string, contract *RoomInterfaceDef, add func(string)) {
	for intentName, required := range contract.Intents {
		actual, ok := room.Intents[intentName]
		if !ok {
			actual, ok = def.Intents[intentName]
		}
		if !ok {
			add(fmt.Sprintf("state %q implements room_interface %q but intent %q has no typed declaration", roomID, interfaceName, intentName))
			continue
		}
		if _, handled := room.On[intentName]; !handled {
			add(fmt.Sprintf("state %q implements room_interface %q but does not handle intent %q", roomID, interfaceName, intentName))
		}
		for slotName, requiredSlot := range required.Slots {
			actualSlot, exists := actual.Slots[slotName]
			if !exists {
				add(fmt.Sprintf("state %q intent %q is missing room_interface %q slot %q", roomID, intentName, interfaceName, slotName))
				continue
			}
			if requiredSlot.Type != "" && actualSlot.Type != requiredSlot.Type {
				add(fmt.Sprintf("state %q intent %q slot %q type %q does not match room_interface %q type %q", roomID, intentName, slotName, actualSlot.Type, interfaceName, requiredSlot.Type))
			}
			if requiredSlot.Required && !actualSlot.Required {
				add(fmt.Sprintf("state %q intent %q slot %q must be required by room_interface %q", roomID, intentName, slotName, interfaceName))
			}
		}
		for slotName, actualSlot := range actual.Slots {
			if _, declared := required.Slots[slotName]; !declared && actualSlot.Required {
				add(fmt.Sprintf("state %q intent %q adds required slot %q outside room_interface %q", roomID, intentName, slotName, interfaceName))
			}
		}
	}
	for field, required := range contract.World {
		actual, ok := def.World[field]
		if !ok {
			add(fmt.Sprintf("state %q implements room_interface %q but world field %q is not declared", roomID, interfaceName, field))
			continue
		}
		if required.Type != "" && actual.Type != required.Type {
			add(fmt.Sprintf("state %q world field %q type %q does not match room_interface %q type %q", roomID, field, actual.Type, interfaceName, required.Type))
		}
	}
}

type finiteIntentTarget struct {
	state  string
	intent string
}

func collectFiniteIntentTargets(def *AppDef) []finiteIntentTarget {
	set := map[finiteIntentTarget]struct{}{}
	add := func(state, intent string) {
		if state == "" || intent == "" {
			return
		}
		set[finiteIntentTarget{state: state, intent: intent}] = struct{}{}
	}
	walkStates(def.States, "", func(path string, room *State) {
		if room == nil {
			return
		}
		for _, interfaceName := range room.Implements {
			if contract := def.RoomInterfaces[interfaceName]; contract != nil {
				for intent := range contract.Intents {
					add(path, intent)
				}
			}
		}
	})
	if def.Application != nil {
		for _, action := range def.Application.Actions {
			if action != nil {
				add(action.State, action.Intent)
			}
		}
	}
	if def.Exports != nil {
		for _, handler := range def.Exports.Handlers {
			if handler != nil && handler.Dispatch != nil {
				add(handler.Dispatch.State, handler.Dispatch.Intent)
			}
		}
	}
	for _, event := range def.Events {
		if event != nil && event.Dispatch != nil {
			add(event.Dispatch.State, event.Dispatch.Intent)
		}
	}
	out := make([]finiteIntentTarget, 0, len(set))
	for target := range set {
		out = append(out, target)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].state == out[j].state {
			return out[i].intent < out[j].intent
		}
		return out[i].state < out[j].state
	})
	return out
}

func validateGuardPartition(state, intent string, transitions []Transition, add func(string)) {
	fallbacks := 0
	for i, transition := range transitions {
		fallback := transition.Default || strings.TrimSpace(transition.When) == ""
		if transition.Default && strings.TrimSpace(transition.When) != "" {
			add(fmt.Sprintf("state %q intent %q transition[%d]: default and when are mutually exclusive", state, intent, i))
		}
		if fallback {
			fallbacks++
			if i != len(transitions)-1 {
				add(fmt.Sprintf("state %q intent %q transition[%d]: fallback must be the final branch", state, intent, i))
			}
		}
	}
	if fallbacks != 1 {
		add(fmt.Sprintf("state %q intent %q guard partition is not total: expected exactly one final default/unconditional branch, got %d", state, intent, fallbacks))
	}
}

func validateEffectContracts(def *AppDef, add func(string)) {
	allStatePaths := map[string]struct{}{}
	collectStatePaths("", def.States, allStatePaths)
	var walkEffects func(string, string, []Effect)
	walkEffects = func(statePath, source string, effects []Effect) {
		for i := range effects {
			effect := &effects[i]
			path := fmt.Sprintf("state %q %s[%d]", statePath, source, i)
			if len(effect.Outcomes) > 0 {
				if effect.Invoke == "" {
					add(path + ": outcomes require invoke")
				}
				if effect.Background {
					add(path + ": outcomes require a foreground invoke; background completion uses on_complete")
				}
				if len(effect.Bind) > 0 && len(effect.Result) == 0 {
					add(path + ": typed result is required when an outcome-declared invoke binds world fields")
				}
				for fieldName, field := range effect.Result {
					if strings.TrimSpace(field.Type) == "" {
						add(fmt.Sprintf("%s: result.%s.type is required", path, fieldName))
					}
				}
				for worldKey, resultKey := range effect.Bind {
					worldField, worldOK := def.World[worldKey]
					resultField, resultOK := effect.Result[resultKey]
					if !resultOK {
						add(fmt.Sprintf("%s: bind %s -> %s references an undeclared result field", path, worldKey, resultKey))
						continue
					}
					if !worldOK {
						add(fmt.Sprintf("%s: bind target %s is not declared in world", path, worldKey))
						continue
					}
					if worldOK && worldField.Type != "" && resultField.Type != "" && worldField.Type != resultField.Type {
						add(fmt.Sprintf("%s: bind %s (%s) <- %s (%s) has incompatible types", path, worldKey, worldField.Type, resultKey, resultField.Type))
					}
				}
				defaults := 0
				for outcomeName, outcome := range effect.Outcomes {
					outcomePath := fmt.Sprintf("%s outcomes.%s", path, outcomeName)
					if strings.TrimSpace(outcomeName) == "" {
						add(path + ": outcome name must not be empty")
					}
					if outcome == nil {
						add(outcomePath + ": empty definition")
						continue
					}
					if outcome.Default {
						defaults++
						if strings.TrimSpace(outcome.When) != "" {
							add(outcomePath + ": default and when are mutually exclusive")
						}
					} else if strings.TrimSpace(outcome.When) == "" {
						add(outcomePath + ": non-default outcome requires when")
					}
					if strings.TrimSpace(outcome.Target) == "" {
						add(outcomePath + ": target is required")
					} else {
						target := resolveTarget(statePath, outcome.Target)
						if _, ok := allStatePaths[target]; !ok {
							add(fmt.Sprintf("%s: target %q (resolved: %q) does not exist", outcomePath, outcome.Target, target))
						}
					}
				}
				if defaults != 1 {
					add(fmt.Sprintf("%s: outcome partition is not total: expected exactly one default, got %d", path, defaults))
				}
			}
			walkEffects(statePath, source+fmt.Sprintf("[%d].on_complete", i), effect.OnComplete)
			walkEffects(statePath, source+fmt.Sprintf("[%d].effects", i), effect.Effects)
		}
	}
	walkStates(def.States, "", func(path string, state *State) {
		if state == nil {
			return
		}
		walkEffects(path, "on_enter", state.OnEnter)
		for intent, transitions := range state.On {
			for i := range transitions {
				walkEffects(path, fmt.Sprintf("on.%s[%d].effects", intent, i), transitions[i].Effects)
			}
		}
	})
}

func containsProgramString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
