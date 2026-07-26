package graph

import (
	"fmt"
	"sort"
	"strings"

	"kitsoki/internal/app"
)

// ProgramGraph returns the complete static story program graph. Unlike
// RoomGraph, it accepts AppDef because application/handler/event declarations
// are intentionally not part of the small runtime App interface.
func ProgramGraph(def *app.AppDef, graphID string) KitsokiGraph {
	g := KitsokiGraph{
		Schema:   SchemaV1,
		GraphID:  graphID,
		Kind:     "story-program",
		Directed: true,
		LayoutHints: LayoutHints{
			Default: "layered",
			RankDir: "LR",
		},
		Meta: map[string]any{},
	}
	if def == nil {
		return g
	}
	g.Meta["application_id"] = def.App.ID
	g.Meta["application_version"] = def.App.Version
	g.Meta["lints"] = ProgramLints(def)

	nodes := map[string]GraphNode{}
	edgeCounts := map[string]int{}
	addNode := func(node GraphNode) string {
		if node.ID == "" {
			node.ID = graphNodeID(node.Kind, node.Ref.Ref)
		}
		if _, exists := nodes[node.ID]; !exists {
			nodes[node.ID] = node
		}
		return node.ID
	}
	addEdge := func(kind, source, target, label string, attrs map[string]any) {
		if source == "" || target == "" {
			return
		}
		base := strings.Join([]string{kind, source, target, label}, ":")
		index := edgeCounts[base]
		edgeCounts[base] = index + 1
		id := base
		if index > 0 {
			id = fmt.Sprintf("%s:%d", base, index)
		}
		g.Edges = append(g.Edges, GraphEdge{
			ID: id, Kind: kind, Source: source, Target: target, Label: label, Attrs: attrs,
		})
	}

	worldIDs := map[string]string{}
	for _, name := range sortedKeysProgram(def.World) {
		world := def.World[name]
		worldIDs[name] = addNode(GraphNode{
			Kind:  "world",
			Label: name,
			Ref:   GraphRef{Kind: "world", Ref: name},
			Attrs: map[string]any{"type": world.Type},
		})
	}

	intentIDs := map[string]string{}
	for _, name := range sortedKeysProgram(def.Intents) {
		intent := def.Intents[name]
		label := intent.Title
		if label == "" {
			label = name
		}
		intentIDs[name] = addNode(GraphNode{
			Kind:  "intent",
			Label: label,
			Ref:   GraphRef{Kind: "intent", Ref: name},
			Attrs: semanticAttrs("", intent.Title, intent.Description, "application.intents."+name),
		})
	}

	interfaceIDs := map[string]string{}
	for _, name := range sortedKeysProgram(def.RoomInterfaces) {
		contract := def.RoomInterfaces[name]
		attrs := map[string]any{}
		if contract != nil {
			attrs["description"] = contract.Description
			attrs["intents"] = sortedKeysProgram(contract.Intents)
			attrs["world"] = sortedKeysProgram(contract.World)
		}
		interfaceIDs[name] = addNode(GraphNode{
			Kind: "room-interface", Label: name,
			Ref: GraphRef{Kind: "room-interface", Ref: name}, Attrs: attrs,
		})
	}
	ensureIntent := func(name string, intent app.Intent, source string) string {
		if id := intentIDs[name]; id != "" {
			return id
		}
		label := intent.Title
		if label == "" {
			label = name
		}
		id := addNode(GraphNode{
			Kind: "intent", Label: label, Ref: GraphRef{Kind: "intent", Ref: name},
			Attrs: semanticAttrs("", intent.Title, intent.Description, source),
		})
		intentIDs[name] = id
		return id
	}

	stateIDs := map[string]string{}
	var declareStates func(string, map[string]*app.State)
	declareStates = func(prefix string, states map[string]*app.State) {
		for _, name := range sortedKeysProgram(states) {
			state := states[name]
			path := joinProgramPath(prefix, name)
			label := name
			if state != nil && strings.TrimSpace(state.Description) != "" {
				label = state.Description
			}
			stateIDs[path] = addNode(GraphNode{
				Kind: "state", Label: label, Ref: GraphRef{Kind: "state", Ref: path},
				Group: "room:" + string(app.StatePath(path).TopLevel()),
				Attrs: programStateAttrs(state),
			})
			if state != nil {
				declareStates(path, state.States)
			}
		}
	}
	declareStates("", def.States)
	for _, room := range sortedKeysProgram(def.States) {
		state := def.States[room]
		label := room
		if state != nil && state.Description != "" {
			label = state.Description
		}
		g.Groups = append(g.Groups, GraphGroup{ID: "room:" + room, Kind: "room", Label: label})
	}

	effectOrdinal := 0
	var addEffects func(ownerID, ownerPath, source string, effects []app.Effect)
	addEffects = func(ownerID, ownerPath, source string, effects []app.Effect) {
		for i, declared := range effects {
			effectOrdinal++
			effectRef := fmt.Sprintf("%s.%s[%d]", ownerPath, source, i)
			kind, label := programEffectKind(declared)
			effectID := addNode(GraphNode{
				ID:   graphNodeID("effect", fmt.Sprintf("%06d:%s", effectOrdinal, effectRef)),
				Kind: "effect", Label: label, Ref: GraphRef{Kind: "effect", Ref: effectRef},
				Group: "room:" + string(app.StatePath(ownerPath).TopLevel()),
				Attrs: map[string]any{
					"effect_kind": kind, "when": declared.When,
					"result_fields": sortedKeysProgram(declared.Result),
					"outcomes":      sortedKeysProgram(declared.Outcomes),
				},
			})
			addEdge("applies", ownerID, effectID, source, nil)
			for _, key := range programEffectWrites(declared) {
				if worldID := worldIDs[key]; worldID != "" {
					addEdge("writes", effectID, worldID, key, nil)
				}
			}
			for _, key := range programEffectReads(declared, def.World) {
				if worldID := worldIDs[key]; worldID != "" {
					addEdge("reads", effectID, worldID, key, nil)
				}
			}
			if declared.Invoke != "" {
				hostID := addNode(GraphNode{
					Kind: "host", Label: declared.Invoke,
					Ref: GraphRef{Kind: "host", Ref: declared.Invoke},
				})
				addEdge("invokes", effectID, hostID, declared.Invoke, nil)
			}
			if declared.EmitIntent != "" && !strings.Contains(declared.EmitIntent, "{{") {
				intentID := ensureIntent(declared.EmitIntent, app.Intent{}, "effects")
				addEdge("dispatches", effectID, intentID, declared.EmitIntent, nil)
			}
			for _, outcomeName := range sortedKeysProgram(declared.Outcomes) {
				outcome := declared.Outcomes[outcomeName]
				if outcome == nil {
					continue
				}
				outcomeID := addNode(GraphNode{
					Kind: "outcome", Label: outcomeName,
					Ref:   GraphRef{Kind: "outcome", Ref: effectRef + ".outcomes." + outcomeName},
					Attrs: map[string]any{"when": outcome.When, "default": outcome.Default},
				})
				addEdge("produces", effectID, outcomeID, outcomeName, nil)
				target := resolveGraphTarget(ownerPath, outcome.Target)
				addEdge("transitions", outcomeID, stateIDs[target], outcome.Target, map[string]any{"source": "effect_outcome"})
			}
			addEffects(effectID, ownerPath, effectRef+".on_complete", declared.OnComplete)
			addEffects(effectID, ownerPath, effectRef+".effects", declared.Effects)
		}
	}

	var connectStates func(string, map[string]*app.State)
	connectStates = func(prefix string, states map[string]*app.State) {
		for _, name := range sortedKeysProgram(states) {
			state := states[name]
			if state == nil {
				continue
			}
			path := joinProgramPath(prefix, name)
			stateID := stateIDs[path]
			for _, interfaceName := range state.Implements {
				addEdge("implements", stateID, interfaceIDs[interfaceName], interfaceName, nil)
			}
			for _, intentName := range sortedKeysProgram(state.Intents) {
				intentID := ensureIntent(intentName, state.Intents[intentName], "states."+path+".intents."+intentName)
				addEdge("handles", stateID, intentID, intentName, nil)
			}
			for _, intentName := range sortedKeysProgram(state.On) {
				intent, ok := state.Intents[intentName]
				if !ok {
					intent = def.Intents[intentName]
				}
				intentID := ensureIntent(intentName, intent, "states."+path+".on."+intentName)
				addEdge("handles", stateID, intentID, intentName, nil)
				for i, transition := range state.On[intentName] {
					target := resolveGraphTarget(path, transition.Target)
					targetID := stateIDs[target]
					attrs := map[string]any{
						"source_ref": path, "target_ref": target, "transition_index": i,
					}
					if transition.When != "" {
						attrs["when"] = transition.When
					}
					if targetID != "" {
						addEdge("transitions", stateID, targetID, intentName, attrs)
					}
					for _, key := range programReads([]string{transition.When}, def.World) {
						if worldID := worldIDs[key]; worldID != "" {
							addEdge("reads", stateID, worldID, key, map[string]any{"source": "transition_guard"})
						}
					}
					addEffects(stateID, path, fmt.Sprintf("on.%s[%d].effects", intentName, i), transition.Effects)
				}
			}
			addEffects(stateID, path, "on_enter", state.OnEnter)
			for _, key := range programViewReads(state.View, def.World) {
				if worldID := worldIDs[key]; worldID != "" {
					addEdge("reads", stateID, worldID, key, map[string]any{"source": "view"})
				}
			}
			connectStates(path, state.States)
		}
	}
	connectStates("", def.States)

	handlerIDs := map[string]string{}
	if def.Exports != nil {
		for _, handlerName := range sortedKeysProgram(def.Exports.Handlers) {
			handler := def.Exports.Handlers[handlerName]
			if handler == nil {
				continue
			}
			handlerIDs[handlerName] = addSemanticProgramNode(
				addNode, "handler", handlerName, handler.Name, handler.Description,
				handler.SemanticRef, "exports.handlers."+handlerName,
				handler.Origin,
				map[string]any{
					"effect": handler.Effect, "routing_mode": handler.RoutingMode,
					"session": handler.Session, "outcomes": handler.Outcomes,
					"expose": handler.Expose,
				},
			)
		}
		for _, handlerName := range sortedKeysProgram(def.Exports.Handlers) {
			handler := def.Exports.Handlers[handlerName]
			if handler == nil {
				continue
			}
			handlerID := handlerIDs[handlerName]
			if handler.Dispatch != nil && handler.Dispatch.Intent != "" {
				intentID := ensureIntent(handler.Dispatch.Intent, app.Intent{}, "exports.handlers."+handlerName+".dispatch")
				addEdge("dispatches", handlerID, intentID, handler.Dispatch.Intent, nil)
			}
			if handler.Dispatch != nil && handler.Dispatch.RoomInterface != "" {
				addEdge("targets-interface", handlerID, interfaceIDs[handler.Dispatch.RoomInterface], handler.Dispatch.RoomInterface, nil)
			}
			if handler.Dispatch != nil && handler.Dispatch.State != "" {
				addEdge("targets", handlerID, stateIDs[handler.Dispatch.State], handler.Dispatch.State, nil)
			}
			if handler.Starlark != nil {
				scriptID := addNode(GraphNode{
					Kind: "script", Label: handler.Starlark.Script,
					Ref: GraphRef{Kind: "script", Ref: handler.Starlark.Script},
				})
				addEdge("invokes", handlerID, scriptID, "starlark", nil)
			}
			if handler.Compensation != "" {
				addEdge("compensates-with", handlerID, handlerIDs[handler.Compensation], handler.Compensation, nil)
			}
		}
	}

	eventIDs := map[string]string{}
	for _, eventName := range sortedKeysProgram(def.Events) {
		event := def.Events[eventName]
		if event == nil {
			continue
		}
		eventIDs[eventName] = addNode(GraphNode{
			Kind: "event", Label: eventName, Ref: GraphRef{Kind: "event", Ref: eventName},
			Attrs: map[string]any{
				"source": event.Source, "mode": event.Mode, "session": event.Session,
				"input_schema": event.InputSchema, "routing_mode": event.RoutingMode,
			},
		})
		if event.Dispatch != nil {
			if event.Dispatch.Handler != "" {
				addEdge("dispatches", eventIDs[eventName], handlerIDs[event.Dispatch.Handler], event.Dispatch.Handler, nil)
			}
			if event.Dispatch.Intent != "" {
				addEdge("dispatches", eventIDs[eventName], ensureIntent(event.Dispatch.Intent, app.Intent{}, "events."+eventName), event.Dispatch.Intent, nil)
			}
			if event.Dispatch.RoomInterface != "" {
				addEdge("targets-interface", eventIDs[eventName], interfaceIDs[event.Dispatch.RoomInterface], event.Dispatch.RoomInterface, nil)
			}
			if event.Dispatch.State != "" {
				addEdge("targets", eventIDs[eventName], stateIDs[event.Dispatch.State], event.Dispatch.State, nil)
			}
		}
	}

	if contract := def.Application; contract != nil {
		applicationID := addSemanticProgramNode(
			addNode, "application", def.App.ID, contract.Name, contract.Description,
			contract.SemanticRef, "application", app.ApplicationMemberOrigin{},
			map[string]any{"schema": contract.Schema},
		)
		pageIDs := map[string]string{}
		for _, pageName := range sortedKeysProgram(contract.Pages) {
			page := contract.Pages[pageName]
			if page == nil {
				continue
			}
			pageIDs[pageName] = addSemanticProgramNode(
				addNode, "page", pageName, page.Name, page.Description, page.SemanticRef,
				"application.pages."+pageName, page.Origin, nil,
			)
			addEdge("contains", applicationID, pageIDs[pageName], pageName, nil)
		}
		for i, nav := range contract.Navigation {
			navID := addSemanticProgramNode(
				addNode, "navigation", nav.ID, nav.Name, nav.Description, nav.SemanticRef,
				fmt.Sprintf("application.navigation[%d]", i), nav.Origin, nil,
			)
			addEdge("contains", applicationID, navID, nav.ID, nil)
			addEdge("navigates-to", navID, pageIDs[nav.Page], nav.Page, nil)
		}
		componentIDs := map[string]string{}
		for _, componentName := range sortedKeysProgram(contract.Components) {
			component := contract.Components[componentName]
			if component == nil {
				continue
			}
			attrs := map[string]any{"props_schema": component.PropsSchema}
			if component.Fallback != nil {
				attrs["fallback"] = component.Fallback.Element
			}
			componentIDs[componentName] = addSemanticProgramNode(
				addNode, "component", componentName, component.Name, component.Description,
				component.SemanticRef, "application.components."+componentName, component.Origin, attrs,
			)
			addEdge("contains", applicationID, componentIDs[componentName], componentName, nil)
		}
		actionIDs := map[string]string{}
		for _, actionName := range sortedKeysProgram(contract.Actions) {
			action := contract.Actions[actionName]
			if action == nil {
				continue
			}
			actionIDs[actionName] = addSemanticProgramNode(
				addNode, "action", actionName, action.Name, action.Description, action.SemanticRef,
				"application.actions."+actionName,
				action.Origin,
				map[string]any{"routing_mode": action.RoutingMode, "input_schema": action.InputSchema},
			)
			addEdge("contains", applicationID, actionIDs[actionName], actionName, nil)
			if action.Handler != "" {
				addEdge("dispatches", actionIDs[actionName], handlerIDs[action.Handler], action.Handler, nil)
			}
			if action.Intent != "" {
				addEdge("dispatches", actionIDs[actionName], ensureIntent(action.Intent, app.Intent{}, "application.actions."+actionName), action.Intent, nil)
			}
			if action.RoomInterface != "" {
				addEdge("targets-interface", actionIDs[actionName], interfaceIDs[action.RoomInterface], action.RoomInterface, nil)
			}
			if action.State != "" {
				addEdge("targets", actionIDs[actionName], stateIDs[action.State], action.State, nil)
			}
		}
		for _, pageName := range sortedKeysProgram(contract.Pages) {
			page := contract.Pages[pageName]
			if page == nil {
				continue
			}
			for _, regionName := range sortedKeysProgram(page.Regions) {
				region := page.Regions[regionName]
				if region == nil {
					continue
				}
				regionSource := "application.pages." + pageName + ".regions." + regionName
				regionID := addSemanticProgramNode(
					addNode, "region", regionName, region.Name, region.Description,
					region.SemanticRef, regionSource, region.Origin, nil,
				)
				addEdge("contains", pageIDs[pageName], regionID, regionName, nil)
				for i, item := range region.Items {
					if item.Card == nil {
						continue
					}
					card := item.Card
					cardSource := fmt.Sprintf("%s.items[%d].card", regionSource, i)
					cardID := addSemanticProgramNode(
						addNode, "card", card.ID, card.Name, card.Description,
						card.SemanticRef, cardSource, card.Origin, nil,
					)
					addEdge("contains", regionID, cardID, card.ID, nil)
					if card.Component != "" {
						targetID := componentIDs[card.Component]
						if targetID == "" && isBuiltinProgramComponent(card.Component) {
							targetID = addNode(GraphNode{
								Kind: "component", Label: card.Component,
								Ref:   GraphRef{Kind: "component", Ref: card.Component},
								Attrs: map[string]any{"builtin": true},
							})
						}
						addEdge("renders", cardID, targetID, card.Component, nil)
					}
					for _, actionName := range card.Actions {
						addEdge("offers", cardID, actionIDs[actionName], actionName, nil)
					}
					for _, key := range programCardReads(card, def.World) {
						addEdge("reads", cardID, worldIDs[key], key, map[string]any{"source": "card_elements"})
					}
				}
			}
		}
		for name, eventID := range eventIDs {
			addEdge("observes", applicationID, eventID, name, nil)
		}
	}

	for _, node := range nodes {
		g.Nodes = append(g.Nodes, node)
	}
	sort.Slice(g.Nodes, func(i, j int) bool { return g.Nodes[i].ID < g.Nodes[j].ID })
	sort.Slice(g.Edges, func(i, j int) bool { return g.Edges[i].ID < g.Edges[j].ID })
	sort.Slice(g.Groups, func(i, j int) bool { return g.Groups[i].ID < g.Groups[j].ID })
	g.Cyclic = hasDirectedCycle(g.Nodes, g.Edges)
	return g
}

func addSemanticProgramNode(
	add func(GraphNode) string,
	kind, ref, name, description, semanticRef, source string,
	origin app.ApplicationMemberOrigin,
	extra map[string]any,
) string {
	if origin.Member != "" {
		source = origin.Member
	}
	attrs := semanticAttrs(semanticRef, name, description, source)
	if origin.Story != "" {
		attrs["story"] = origin.Story
	}
	for key, value := range extra {
		attrs[key] = value
	}
	if name == "" {
		name = ref
	}
	nodeRef := ref
	if semanticRef != "" {
		nodeRef = semanticRef
	}
	return add(GraphNode{
		Kind: kind, Label: name, Ref: GraphRef{Kind: kind, Ref: nodeRef}, Attrs: attrs,
	})
}

func semanticAttrs(ref, name, description, source string) map[string]any {
	attrs := map[string]any{"source": source}
	if ref != "" {
		attrs["semantic_ref"] = ref
		if story, _, ok := strings.Cut(ref, "."); ok {
			attrs["story"] = story
		}
	}
	if name != "" {
		attrs["name"] = name
	}
	if description != "" {
		attrs["description"] = description
	}
	return attrs
}

func programEffectKind(effect app.Effect) (string, string) {
	switch {
	case effect.Invoke != "":
		return "invoke", effect.Invoke
	case len(effect.Set) > 0:
		return "set", "set world"
	case len(effect.Increment) > 0:
		return "increment", "increment world"
	case effect.EmitIntent != "":
		return "emit_intent", effect.EmitIntent
	case effect.Emit != "":
		return "emit", effect.Emit
	case effect.Say != "":
		return "say", "say"
	default:
		return "effect", "effect"
	}
}

func programEffectWrites(effect app.Effect) []string {
	set := map[string]struct{}{}
	for key := range effect.Set {
		set[key] = struct{}{}
	}
	for key := range effect.Increment {
		set[key] = struct{}{}
	}
	for key := range effect.Bind {
		set[key] = struct{}{}
	}
	return sortedSet(set)
}

func programEffectReads(effect app.Effect, world map[string]app.VarDef) []string {
	var texts []string
	texts = append(texts, effect.When, effect.Say, effect.Emit, effect.EmitIntent)
	for _, outcome := range effect.Outcomes {
		if outcome != nil {
			texts = append(texts, outcome.When)
		}
	}
	for _, value := range effect.Set {
		texts = append(texts, fmt.Sprint(value))
	}
	for _, value := range effect.With {
		texts = append(texts, fmt.Sprint(value))
	}
	for _, value := range effect.EmitSlots {
		texts = append(texts, fmt.Sprint(value))
	}
	set := map[string]struct{}{}
	for _, key := range programReads(texts, world) {
		set[key] = struct{}{}
	}
	for key := range effect.Increment {
		set[key] = struct{}{}
	}
	return sortedSet(set)
}

func programViewReads(view app.View, world map[string]app.VarDef) []string {
	texts := []string{view.Source}
	for _, element := range view.Elements {
		texts = append(texts, element.Source, element.When, element.Subtitle, element.MediaHandle, element.MediaPath)
	}
	return programReads(texts, world)
}

func programElementReads(elements []app.ViewElement, world map[string]app.VarDef) []string {
	texts := make([]string, 0, len(elements)*5)
	for _, element := range elements {
		texts = append(texts, element.Source, element.When, element.Subtitle, element.MediaHandle, element.MediaPath)
	}
	return programReads(texts, world)
}

func programCardReads(card *app.ApplicationCard, world map[string]app.VarDef) []string {
	if card == nil {
		return nil
	}
	texts := make([]string, 0, len(card.Props))
	for _, value := range card.Props {
		texts = append(texts, fmt.Sprint(value))
	}
	for _, key := range programElementReads(card.Elements, world) {
		texts = append(texts, "world."+key)
	}
	return programReads(texts, world)
}

func programReads(texts []string, world map[string]app.VarDef) []string {
	set := map[string]struct{}{}
	for _, text := range texts {
		for key := range world {
			if referencesWorldKey(text, key) {
				set[key] = struct{}{}
			}
		}
	}
	return sortedSet(set)
}

func sortedSet(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for item := range set {
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func joinProgramPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

func programStateSource(state *app.State) string {
	if state == nil {
		return ""
	}
	return state.OriginFile
}

func programStateAttrs(state *app.State) map[string]any {
	attrs := map[string]any{"source": programStateSource(state)}
	if state == nil {
		return attrs
	}
	if len(state.Implements) > 0 {
		attrs["implements"] = append([]string(nil), state.Implements...)
	}
	if state.InstantiatedFrom != "" {
		attrs["instantiated_from"] = state.InstantiatedFrom
		attrs["instantiated_parameters"] = state.InstantiatedParameters
	}
	return attrs
}

func isBuiltinProgramComponent(name string) bool {
	switch name {
	case "prose", "form", "list", "table", "artifact", "status",
		"heading", "code", "template", "kv", "banner", "choice", "media":
		return true
	default:
		return false
	}
}

func sortedKeysProgram[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
