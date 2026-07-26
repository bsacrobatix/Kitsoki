package graph

import (
	"fmt"
	"sort"
	"strings"

	"kitsoki/internal/app"
)

type ProgramLint struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Ref      string `json:"ref"`
	Message  string `json:"message"`
}

// ProgramLints returns deterministic, read-only whole-program diagnostics.
// Hard contract failures remain loader errors; these lints cover graph quality
// and coverage properties that are useful to authors without blocking legacy
// stories.
func ProgramLints(def *app.AppDef) []ProgramLint {
	if def == nil {
		return nil
	}
	var out []ProgramLint
	add := func(severity, code, ref, message string) {
		out = append(out, ProgramLint{Severity: severity, Code: code, Ref: ref, Message: message})
	}

	reachable := reachableProgramRooms(def)
	for _, roomID := range sortedKeysProgram(def.States) {
		if !reachable[roomID] {
			add("warning", "state-unreachable", roomID, "room is unreachable from the initial state")
		}
	}

	implemented := map[string]int{}
	walkProgramStates("", def.States, func(path string, state *app.State) {
		if state == nil {
			return
		}
		for _, name := range state.Implements {
			implemented[name]++
		}
		if !state.Terminal && len(state.States) == 0 && len(state.On) == 0 &&
			state.Timeout == nil && !effectsCanAdvance(state.OnEnter) {
			add("warning", "state-deadlock", path, "non-terminal state has no transition, timeout, or advancing effect")
		}
		for _, effects := range stateEffects(state) {
			for _, effect := range effects {
				for _, key := range effectWritesRecursive(effect) {
					if _, declared := def.World[key]; !declared {
						add("error", "world-write-undeclared", path, fmt.Sprintf("effect writes undeclared world field %q", key))
					}
				}
			}
		}
	})
	for _, name := range sortedKeysProgram(def.RoomInterfaces) {
		if implemented[name] == 0 {
			add("warning", "interface-unimplemented", name, "room interface has no implementation")
		}
	}

	if contract := def.Application; contract != nil {
		offered := map[string]bool{}
		for pageID, page := range contract.Pages {
			if page == nil {
				continue
			}
			semanticQuality("page", pageID, page.Name, page.Description, add)
			for _, region := range page.Regions {
				if region == nil {
					continue
				}
				for _, item := range region.Items {
					if item.Card == nil {
						continue
					}
					for _, actionID := range item.Card.Actions {
						offered[actionID] = true
					}
				}
			}
		}
		for id, action := range contract.Actions {
			if action == nil {
				continue
			}
			semanticQuality("action", id, action.Name, action.Description, add)
			if !offered[id] && !nativeCommand(contract, id) {
				add("warning", "action-unoffered", id, "application action is not offered by a card or native surface")
			}
		}
		for id, component := range contract.Components {
			if component == nil {
				continue
			}
			semanticQuality("component", id, component.Name, component.Description, add)
			if component.Web != nil && hasNonWebSurface(contract) && component.Fallback == nil {
				add("error", "component-fallback-missing", id, "custom web component has no fallback for a non-web surface")
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity < out[j].Severity
		}
		if out[i].Code != out[j].Code {
			return out[i].Code < out[j].Code
		}
		if out[i].Ref != out[j].Ref {
			return out[i].Ref < out[j].Ref
		}
		return out[i].Message < out[j].Message
	})
	return out
}

func reachableProgramRooms(def *app.AppDef) map[string]bool {
	reachable := map[string]bool{}
	root, _ := def.Root.(string)
	root = string(app.StatePath(root).TopLevel())
	if root == "" {
		return reachable
	}
	queue := []string{root}
	for len(queue) > 0 {
		roomID := queue[0]
		queue = queue[1:]
		if reachable[roomID] {
			continue
		}
		state := def.States[roomID]
		if state == nil {
			continue
		}
		reachable[roomID] = true
		walkProgramStates("", map[string]*app.State{roomID: state}, func(path string, current *app.State) {
			if current == nil {
				return
			}
			for _, transitions := range current.On {
				for _, transition := range transitions {
					target := resolveGraphTarget(path, transition.Target)
					targetRoom := string(app.StatePath(target).TopLevel())
					if targetRoom != "" && !reachable[targetRoom] {
						queue = append(queue, targetRoom)
					}
				}
			}
		})
	}
	return reachable
}

func walkProgramStates(prefix string, states map[string]*app.State, visit func(string, *app.State)) {
	for _, name := range sortedKeysProgram(states) {
		state := states[name]
		path := joinProgramPath(prefix, name)
		visit(path, state)
		if state != nil {
			walkProgramStates(path, state.States, visit)
		}
	}
}

func effectsCanAdvance(effects []app.Effect) bool {
	for _, effect := range effects {
		if effect.Target != "" || effect.EmitIntent != "" || len(effect.Outcomes) > 0 {
			return true
		}
		if effectsCanAdvance(effect.OnComplete) || effectsCanAdvance(effect.Effects) {
			return true
		}
	}
	return false
}

func stateEffects(state *app.State) [][]app.Effect {
	out := [][]app.Effect{state.OnEnter}
	for _, transitions := range state.On {
		for _, transition := range transitions {
			out = append(out, transition.Effects)
		}
	}
	return out
}

func effectWritesRecursive(effect app.Effect) []string {
	set := map[string]struct{}{}
	for _, key := range programEffectWrites(effect) {
		set[key] = struct{}{}
	}
	for _, child := range append(append([]app.Effect(nil), effect.OnComplete...), effect.Effects...) {
		for _, key := range effectWritesRecursive(child) {
			set[key] = struct{}{}
		}
	}
	return sortedSet(set)
}

func semanticQuality(kind, id, name, description string, add func(string, string, string, string)) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(description) == "" {
		add("warning", "semantic-copy-missing", id, kind+" requires a meaningful name and description")
		return
	}
	if strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(description)) {
		add("warning", "semantic-copy-repeated", id, kind+" description repeats its name")
	}
}

func nativeCommand(contract *app.ApplicationContract, actionID string) bool {
	for _, surface := range contract.Surfaces {
		if surface == nil || surface.Native == nil {
			continue
		}
		for _, command := range surface.Native.Commands {
			if command == actionID {
				return true
			}
		}
	}
	return false
}

func hasNonWebSurface(contract *app.ApplicationContract) bool {
	for name := range contract.Surfaces {
		if name != "web" {
			return true
		}
	}
	return false
}
