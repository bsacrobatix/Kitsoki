package application

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"kitsoki/internal/app"
)

// CompileFrame projects a validated author contract into a deterministic,
// presentation-free runtime frame. It is deliberately pure: schema references
// remain references, while only inline static props are encoded as JSON.
func CompileFrame(def *app.AppDef, sessionID string, revision uint64, pageID string, workflow Workflow) (Frame, error) {
	return CompileFrameWithData(def, sessionID, revision, pageID, workflow, nil)
}

// CompileFrameWithData projects only world keys explicitly allowlisted by
// application.data. The input is never copied wholesale into the frame.
func CompileFrameWithData(def *app.AppDef, sessionID string, revision uint64, pageID string, workflow Workflow, world map[string]any) (Frame, error) {
	if def == nil {
		return Frame{}, fmt.Errorf("application: application contract is required")
	}
	contract, _ := app.EffectiveApplication(def)
	if contract == nil {
		return Frame{}, fmt.Errorf("application: application contract is required")
	}
	if contract.Schema != app.ApplicationSchemaV1 {
		return Frame{}, fmt.Errorf("application: contract schema %q, want %q", contract.Schema, app.ApplicationSchemaV1)
	}
	if sessionID == "" {
		return Frame{}, fmt.Errorf("application: session id is required")
	}
	if pageID == "" {
		pageID = contract.Shell.Entry
		if pageID == "" {
			pages := sortedMapKeys(contract.Pages)
			if len(pages) > 0 {
				pageID = pages[0]
			}
		}
	}
	page, ok := contract.Pages[pageID]
	if !ok || page == nil {
		return Frame{}, fmt.Errorf("application: page %q is not declared", pageID)
	}

	frame := Frame{
		Schema: FrameSchema, ApplicationID: def.App.ID, SessionID: sessionID,
		Revision: revision, Page: pageID, Workflow: workflow,
		Semantic: semanticFromContract(
			contract.SemanticRef, SemanticApplication, contract.Name, contract.Description,
			def.App.ID, "application",
		),
		PageSemantic: semanticFromApplicationMember(
			page.SemanticRef, SemanticPage, page.Name, page.Description,
			def.App.ID, "application.pages."+pageID, page.Origin,
		),
		Capabilities: Capabilities{Presentation: []string{"typed-elements"}},
	}
	data, err := compileFrameData(contract.Data, world)
	if err != nil {
		return Frame{}, err
	}
	frame.Data = data

	for _, nav := range contract.Navigation {
		node := semanticFromApplicationMember(
			nav.SemanticRef, SemanticNavigation, nav.Name, nav.Description,
			def.App.ID, fmt.Sprintf("application.navigation.%s", nav.ID), nav.Origin,
		)
		if target := contract.Pages[nav.Page]; target != nil {
			node.Relationships = append(node.Relationships, Relationship{Kind: "page", Ref: target.SemanticRef})
		}
		frame.Navigation = append(frame.Navigation, NavigationItem{
			ID: nav.ID, Page: nav.Page, Semantic: node,
		})
	}

	for _, id := range sortedMapKeys(contract.Pages) {
		decl := contract.Pages[id]
		if decl == nil {
			return Frame{}, fmt.Errorf("application: page %q has an empty definition", id)
		}
		frame.Pages = append(frame.Pages, PageDescriptor{
			ID: id, Current: id == pageID,
			Semantic: semanticFromApplicationMember(
				decl.SemanticRef, SemanticPage, decl.Name, decl.Description,
				def.App.ID, "application.pages."+id, decl.Origin,
			),
		})
	}

	for _, id := range sortedMapKeys(contract.Components) {
		decl := contract.Components[id]
		if decl == nil {
			return Frame{}, fmt.Errorf("application: component %q has an empty definition", id)
		}
		fallback := ""
		if decl.Fallback != nil {
			fallback = decl.Fallback.Element
		}
		frame.Components = append(frame.Components, ComponentDescriptor{
			ID: id, Fallback: fallback,
			Semantic: semanticFromApplicationMember(
				decl.SemanticRef, SemanticComponent, decl.Name, decl.Description,
				def.App.ID, "application.components."+id, decl.Origin,
			),
		})
		if decl.Web != nil {
			frame.Capabilities.Presentation = appendUnique(frame.Capabilities.Presentation, "custom-components")
		}
	}

	if def.Exports != nil {
		for _, id := range sortedMapKeys(def.Exports.Handlers) {
			decl := def.Exports.Handlers[id]
			if decl == nil {
				return Frame{}, fmt.Errorf("application: handler %q has an empty definition", id)
			}
			frame.Handlers = append(frame.Handlers, HandlerDescriptor{
				ID: id,
				Semantic: semanticFromApplicationMember(
					decl.SemanticRef, SemanticHandler, decl.Name, decl.Description,
					def.App.ID, "exports.handlers."+id, decl.Origin,
				),
			})
		}
	}

	for _, id := range sortedMapKeys(contract.Actions) {
		action, err := compileAction(def, id, contract.Actions[id])
		if err != nil {
			return Frame{}, err
		}
		frame.Actions = append(frame.Actions, action)
		frame.Capabilities.Actions = append(frame.Capabilities.Actions, id)
	}

	for _, regionID := range sortedMapKeys(page.Regions) {
		decl := page.Regions[regionID]
		if decl == nil {
			return Frame{}, fmt.Errorf("application: region %q has an empty definition", regionID)
		}
		region := Region{
			ID: regionID,
			Semantic: semanticFromApplicationMember(
				decl.SemanticRef, SemanticRegion, decl.Name, decl.Description,
				def.App.ID, fmt.Sprintf("application.pages.%s.regions.%s", pageID, regionID), decl.Origin,
			),
		}
		for itemIndex, item := range decl.Items {
			if item.Card == nil {
				return Frame{}, fmt.Errorf("application: page %q region %q item %d is not a card", pageID, regionID, itemIndex)
			}
			cardDecl := item.Card
			member := fmt.Sprintf("application.pages.%s.regions.%s.items[%d].card", pageID, regionID, itemIndex)
			card := Card{
				ID: cardDecl.ID,
				Semantic: semanticFromApplicationMember(
					cardDecl.SemanticRef, SemanticCard, cardDecl.Name, cardDecl.Description,
					def.App.ID, member, cardDecl.Origin,
				),
			}
			for _, elementDecl := range cardDecl.Elements {
				props, err := json.Marshal(elementDecl)
				if err != nil {
					return Frame{}, fmt.Errorf("application: encode typed element for card %q: %w", cardDecl.ID, err)
				}
				card.Body = append(card.Body, Element{Kind: elementDecl.Kind, Props: props})
			}
			if cardDecl.Component != "" {
				props, err := json.Marshal(cardDecl.Props)
				if err != nil {
					return Frame{}, fmt.Errorf("application: encode props for card %q: %w", cardDecl.ID, err)
				}
				if cardDecl.Props == nil {
					props = json.RawMessage(`{}`)
				}
				card.Body = append(card.Body, Element{
					Kind: "component", Component: cardDecl.Component, Props: props,
				})
				if component := contract.Components[cardDecl.Component]; component != nil {
					card.Semantic.Relationships = append(card.Semantic.Relationships, Relationship{
						Kind: "component", Ref: component.SemanticRef,
					})
				}
			}
			for _, actionID := range cardDecl.Actions {
				action, err := compileAction(def, actionID, contract.Actions[actionID])
				if err != nil {
					return Frame{}, err
				}
				card.Actions = append(card.Actions, action)
				card.Semantic.Relationships = append(card.Semantic.Relationships, Relationship{
					Kind: "action", Ref: action.Semantic.Ref,
				})
			}
			region.Cards = append(region.Cards, card)
			region.Semantic.Relationships = append(region.Semantic.Relationships, Relationship{
				Kind: "card", Ref: card.Semantic.Ref,
			})
		}
		frame.Regions = append(frame.Regions, region)
		frame.PageSemantic.Relationships = append(frame.PageSemantic.Relationships, Relationship{
			Kind: "region", Ref: region.Semantic.Ref,
		})
	}

	// Keep the current page descriptor identical to the current-page node,
	// including its runtime region relationships.
	for i := range frame.Pages {
		if frame.Pages[i].ID == pageID {
			frame.Pages[i].Semantic = frame.PageSemantic
			break
		}
	}
	if err := frame.Validate(); err != nil {
		return Frame{}, fmt.Errorf("application: compile frame: %w", err)
	}
	return frame, nil
}

func compileFrameData(declarations map[string]*app.ApplicationData, world map[string]any) (map[string]FrameData, error) {
	if len(declarations) == 0 || world == nil {
		return nil, nil
	}
	data := make(map[string]FrameData)
	for _, name := range sortedMapKeys(declarations) {
		declaration := declarations[name]
		if declaration == nil || declaration.Policy == "exclude" {
			continue
		}
		key, ok := strings.CutPrefix(declaration.Source, "world.")
		if !ok || key == "" || strings.Contains(key, ".") {
			return nil, fmt.Errorf("application: data %q source %q must be world.<key>", name, declaration.Source)
		}
		if declaration.Policy == "include" &&
			(declaration.Sensitivity == "sensitive" || declaration.Sensitivity == "secret") {
			return nil, fmt.Errorf(
				"application: data %q cannot include %s value",
				name, declaration.Sensitivity,
			)
		}
		value, ok := world[key]
		if !ok {
			continue
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("application: encode data %q from %q: %w", name, declaration.Source, err)
		}
		switch declaration.Policy {
		case "include":
		case "redact":
			raw = json.RawMessage(`"[redacted]"`)
		case "hash":
			sum := sha256.Sum256(raw)
			raw, _ = json.Marshal(fmt.Sprintf("sha256:%x", sum))
		default:
			return nil, fmt.Errorf("application: data %q has invalid policy %q", name, declaration.Policy)
		}
		data[name] = FrameData{
			Value: raw, Sensitivity: declaration.Sensitivity, Policy: declaration.Policy,
		}
	}
	if len(data) == 0 {
		return nil, nil
	}
	return data, nil
}

func compileAction(def *app.AppDef, id string, decl *app.ApplicationAction) (Action, error) {
	if decl == nil {
		return Action{}, fmt.Errorf("application: action %q is not declared", id)
	}
	action := Action{
		ID: id, Handler: decl.Handler, Intent: decl.Intent, TargetState: decl.State,
		RoomInterface: decl.RoomInterface,
		RoutingMode:   RoutingMode(decl.RoutingMode), InputSchemaRef: decl.InputSchema, Enabled: true,
		Semantic: semanticFromApplicationMember(
			decl.SemanticRef, SemanticAction, decl.Name, decl.Description,
			def.App.ID, "application.actions."+id, decl.Origin,
		),
	}
	schema, err := resolveActionInputSchema(def, id, decl.InputSchema)
	if err != nil {
		return Action{}, err
	}
	action.InputSchema = schema
	if decl.Handler != "" && def.Exports != nil {
		if handler := def.Exports.Handlers[decl.Handler]; handler != nil {
			action.Semantic.Relationships = append(action.Semantic.Relationships, Relationship{
				Kind: "handler", Ref: handler.SemanticRef,
			})
		}
	}
	return action, nil
}

func resolveActionInputSchema(def *app.AppDef, actionID, reference string) (json.RawMessage, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return nil, nil
	}
	if def == nil || def.BaseDir == "" {
		return nil, fmt.Errorf("application: action %q input schema %q has no story root", actionID, reference)
	}
	if strings.Contains(reference, "{{") {
		return nil, fmt.Errorf("application: action %q input schema path may not be templated", actionID)
	}
	target := reference
	if !filepath.IsAbs(target) {
		target = filepath.Join(def.BaseDir, target)
	}
	target, err := filepath.EvalSymlinks(filepath.Clean(target))
	if err != nil {
		return nil, fmt.Errorf("application: action %q input schema %q: %w", actionID, reference, err)
	}
	allowedRoots := []string{def.BaseDir}
	for _, manifest := range def.LoadedManifests {
		allowedRoots = append(allowedRoots, filepath.Dir(manifest))
	}
	allowed := false
	for _, root := range allowedRoots {
		root, rootErr := filepath.EvalSymlinks(filepath.Clean(root))
		if rootErr != nil {
			continue
		}
		relative, relErr := filepath.Rel(root, target)
		if relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, fmt.Errorf("application: action %q input schema %q escapes story roots", actionID, reference)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		return nil, fmt.Errorf("application: action %q input schema %q: %w", actionID, reference, err)
	}
	normalized, err := NormalizeJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("application: action %q input schema %q: %w", actionID, reference, err)
	}
	return normalized, nil
}

func semanticFromContract(ref string, kind SemanticKind, name, description, story, member string) SemanticNode {
	return SemanticNode{
		Ref: ref, Kind: kind, Name: name, Description: description,
		Source: Provenance{Story: story, Member: member, ProgramNode: member},
	}
}

func semanticFromApplicationMember(
	ref string,
	kind SemanticKind,
	name, description, fallbackStory, fallbackMember string,
	origin app.ApplicationMemberOrigin,
) SemanticNode {
	if origin.Story == "" {
		origin.Story = fallbackStory
	}
	if origin.Member == "" {
		origin.Member = fallbackMember
	}
	return semanticFromContract(ref, kind, name, description, origin.Story, origin.Member)
}

func sortedMapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
