package app

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// EffectiveApplication returns the authored application contract, or a
// deterministic generated projection of legacy top-level rooms and typed
// views. The returned bool reports whether the contract was generated.
//
// The function never mutates def. Existing runtimes therefore keep their
// room-oriented behavior until they explicitly request this projection.
func EffectiveApplication(def *AppDef) (*ApplicationContract, bool) {
	if def == nil {
		return nil, false
	}
	if def.Application != nil {
		return def.Application, false
	}
	return projectLegacyApplication(def), true
}

func projectLegacyApplication(def *AppDef) *ApplicationContract {
	title := strings.TrimSpace(def.App.Title)
	if title == "" {
		title = def.App.ID
	}
	contract := &ApplicationContract{
		Schema: ApplicationSchemaV1, Name: title,
		Description: fmt.Sprintf("Generated application projection for story %s.", def.App.ID),
		SemanticRef: semanticJoin(def.App.ID, "application"),
		Pages:       map[string]*ApplicationPage{},
		Components:  map[string]*ApplicationComponent{},
		Actions:     map[string]*ApplicationAction{},
		Surfaces: map[string]*ApplicationSurface{
			"tui": {Projection: "cards"},
		},
		Generated: true,
	}

	for _, stateID := range sortedKeys(def.States) {
		state := def.States[stateID]
		if state == nil || IsSynthesizedState(stateID) {
			continue
		}
		name := strings.TrimSpace(state.Description)
		if name == "" {
			name = stateID
		}
		actionIDs := make([]string, 0, len(state.On))
		for _, intentName := range sortedKeys(state.On) {
			intent, _ := resolveLegacyIntent(def, state, intentName)
			actionID := semanticJoin(def.App.ID, "action", stateID, intentName)
			actionName := strings.TrimSpace(intent.Title)
			if actionName == "" {
				actionName = intentName
			}
			description := strings.TrimSpace(intent.Description)
			if description == "" {
				description = fmt.Sprintf("Dispatch %s in %s.", intentName, name)
			}
			contract.Actions[actionID] = &ApplicationAction{
				Name: actionName, Description: description,
				SemanticRef: actionID, Intent: intentName, State: stateID,
				RoutingMode: "exact", Generated: true,
			}
			actionIDs = append(actionIDs, actionID)
		}
		contract.Pages[stateID] = &ApplicationPage{
			Name: name, Description: fmt.Sprintf("Generated page for room %s.", stateID),
			SemanticRef: semanticJoin(def.App.ID, "page", stateID), Generated: true,
			Regions: map[string]*ApplicationRegion{
				"main": {
					Name: "Room content", Description: fmt.Sprintf("Typed content and actions for %s.", name),
					SemanticRef: semanticJoin(def.App.ID, "region", stateID, "main"),
					Generated:   true,
					Items: []ApplicationRegionItem{{Card: &ApplicationCard{
						ID: "view", Name: name, Description: fmt.Sprintf("Current view for %s.", name),
						SemanticRef: semanticJoin(def.App.ID, "card", stateID, "view"),
						Elements:    cloneViewElements(state.View.Elements),
						Actions:     actionIDs, Generated: true,
					}}},
				},
			},
		}
		contract.Navigation = append(contract.Navigation, ApplicationNavigation{
			ID: stateID, Name: name, Description: fmt.Sprintf("Open room %s.", stateID),
			SemanticRef: semanticJoin(def.App.ID, "nav", stateID), Page: stateID,
		})
	}
	if root, ok := def.Root.(string); ok {
		if _, exists := contract.Pages[root]; exists {
			contract.Shell.Entry = root
		}
	}
	if contract.Shell.Entry == "" && len(contract.Pages) > 0 {
		ids := make([]string, 0, len(contract.Pages))
		for id := range contract.Pages {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		contract.Shell.Entry = ids[0]
	}
	return contract
}

func resolveLegacyIntent(def *AppDef, state *State, name string) (Intent, bool) {
	if state != nil {
		if intent, ok := state.Intents[name]; ok {
			return intent, true
		}
	}
	intent, ok := def.Intents[name]
	return intent, ok
}

func semanticJoin(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = sanitizeSemanticSegment(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return strings.Join(out, ".")
}

func sanitizeSemanticSegment(value string) string {
	var out []rune
	for _, r := range value {
		switch {
		case unicode.IsLetter(r), r == '_', r == '-':
			out = append(out, r)
		case unicode.IsDigit(r) && len(out) > 0:
			out = append(out, r)
		default:
			if len(out) == 0 || out[len(out)-1] != '-' {
				out = append(out, '-')
			}
		}
	}
	return strings.Trim(string(out), "-")
}

func cloneViewElements(elements []ViewElement) []ViewElement {
	if len(elements) == 0 {
		return nil
	}
	out := make([]ViewElement, len(elements))
	cloner := &childRewriter{}
	for i, element := range elements {
		out[i] = cloner.rewriteViewElement(element)
	}
	return out
}
