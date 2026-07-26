package app

import (
	"fmt"
	"path/filepath"
	"strings"
)

// foldChildApplication composes an imported story's application fragments
// into an application-owning parent. Member ids are parent/alias-qualified;
// semantic refs remain child-owned and stable for feedback/provenance.
func foldChildApplication(parent, child *AppDef, alias string, rw *childRewriter, file string) []error {
	if parent == nil || child == nil {
		return nil
	}
	var errs []error
	add := func(message string) {
		errs = append(errs, &ValidationError{File: file, Message: fmt.Sprintf("imports.%s: application: %s", alias, message)})
	}

	// Room interfaces participate in story composition even when the parent
	// remains a legacy room-only story.
	if len(child.RoomInterfaces) > 0 {
		if parent.RoomInterfaces == nil {
			parent.RoomInterfaces = map[string]*RoomInterfaceDef{}
		}
		for _, name := range sortedKeys(child.RoomInterfaces) {
			prefixed := alias + "__" + name
			if _, exists := parent.RoomInterfaces[prefixed]; exists {
				add(fmt.Sprintf("room_interface %q collides", prefixed))
				continue
			}
			parent.RoomInterfaces[prefixed] = rewriteRoomInterface(child.RoomInterfaces[name], rw)
		}
		rewriteImportedRoomImplementations(parent.States[alias], alias)
	}

	if parent.Application == nil {
		return errs
	}
	if child.Application == nil && (child.Exports == nil || len(child.Exports.Handlers) == 0) {
		return errs
	}
	ensureApplicationMemberOrigins(child)
	if parent.ImportedApplicationOwners == nil {
		parent.ImportedApplicationOwners = map[string]struct{}{}
	}
	parent.ImportedApplicationOwners[child.App.ID] = struct{}{}
	for owner := range child.ImportedApplicationOwners {
		parent.ImportedApplicationOwners[owner] = struct{}{}
	}
	for _, root := range child.ApplicationPackageRoots {
		parent.ApplicationPackageRoots = appendUnique(parent.ApplicationPackageRoots, root)
	}

	if parent.Application.Pages == nil {
		parent.Application.Pages = map[string]*ApplicationPage{}
	}
	if parent.Application.Components == nil {
		parent.Application.Components = map[string]*ApplicationComponent{}
	}
	if parent.Application.Actions == nil {
		parent.Application.Actions = map[string]*ApplicationAction{}
	}
	if parent.Application.Schemas == nil {
		parent.Application.Schemas = map[string]string{}
	}
	if parent.Application.Tokens == nil {
		parent.Application.Tokens = map[string]string{}
	}
	if parent.Exports == nil {
		parent.Exports = &ExportsBlock{}
	}
	if parent.Exports.Handlers == nil {
		parent.Exports.Handlers = map[string]*ApplicationHandler{}
	}

	pageIDs := map[string]string{}
	componentIDs := map[string]string{}
	actionIDs := map[string]string{}
	schemaIDs := map[string]string{}
	tokenIDs := map[string]string{}
	handlerIDs := map[string]string{}
	var applicationExports *ApplicationExports
	if child.Exports != nil {
		applicationExports = child.Exports.Application
	}
	if child.Application != nil && applicationExports != nil {
		exportMembers := func(kind string, ids []string, exists func(string) bool, target map[string]string) {
			for _, id := range ids {
				if !exists(id) {
					add(fmt.Sprintf("exports.application.%s references undefined member %q", kind, id))
					continue
				}
				target[id] = composedApplicationID(parent.App.ID, alias, child.App.ID, id)
			}
		}
		exportMembers("pages", applicationExports.Pages, func(id string) bool {
			return child.Application.Pages[id] != nil
		}, pageIDs)
		for id := range pageIDs {
			pageIDs[id] = alias + "__" + id
		}
		exportMembers("components", applicationExports.Components, func(id string) bool {
			return child.Application.Components[id] != nil
		}, componentIDs)
		exportMembers("actions", applicationExports.Actions, func(id string) bool {
			return child.Application.Actions[id] != nil
		}, actionIDs)
		exportMembers("schemas", applicationExports.Schemas, func(id string) bool {
			_, ok := child.Application.Schemas[id]
			return ok
		}, schemaIDs)
		exportMembers("tokens", applicationExports.Tokens, func(id string) bool {
			_, ok := child.Application.Tokens[id]
			return ok
		}, tokenIDs)
	}
	if child.Exports != nil {
		for id := range child.Exports.Handlers {
			handlerIDs[id] = composedApplicationID(parent.App.ID, alias, child.App.ID, id)
		}
	}

	for _, id := range sortedKeys(pageIDs) {
		newID := pageIDs[id]
		if _, exists := parent.Application.Pages[newID]; exists {
			add(fmt.Sprintf("page %q collides", newID))
			continue
		}
		page := child.Application.Pages[id]
		privateDependency := false
		for _, region := range page.Regions {
			if region == nil {
				continue
			}
			for _, item := range region.Items {
				if item.Card == nil {
					continue
				}
				if component := item.Card.Component; child.Application.Components[component] != nil && componentIDs[component] == "" {
					add(fmt.Sprintf("exported page %q references private component %q", id, component))
					privateDependency = true
				}
				for _, action := range item.Card.Actions {
					if child.Application.Actions[action] != nil && actionIDs[action] == "" {
						add(fmt.Sprintf("exported page %q references private action %q", id, action))
						privateDependency = true
					}
				}
				for _, intent := range privateApplicationElementIntents(item.Card.Elements, child) {
					add(fmt.Sprintf("exported page %q references private intent %q from a typed element", id, intent))
					privateDependency = true
				}
			}
		}
		if privateDependency {
			continue
		}
		parent.Application.Pages[newID] = cloneComposedPage(page, alias, rw, componentIDs, actionIDs)
	}
	if child.Application != nil && applicationExports != nil {
		exportedNavigation := make(map[string]struct{}, len(applicationExports.Navigation))
		for _, id := range applicationExports.Navigation {
			exportedNavigation[id] = struct{}{}
		}
		for _, navigation := range child.Application.Navigation {
			if _, ok := exportedNavigation[navigation.ID]; !ok {
				continue
			}
			if pageIDs[navigation.Page] == "" {
				add(fmt.Sprintf("exported navigation %q targets private page %q", navigation.ID, navigation.Page))
				continue
			}
			clone := navigation
			clone.ID = alias + "__" + navigation.ID
			clone.Page = pageIDs[navigation.Page]
			parent.Application.Navigation = append(parent.Application.Navigation, clone)
			delete(exportedNavigation, navigation.ID)
		}
		for id := range exportedNavigation {
			add(fmt.Sprintf("exports.application.navigation references undefined member %q", id))
		}
	}
	for _, id := range sortedKeys(componentIDs) {
		newID := componentIDs[id]
		if _, exists := parent.Application.Components[newID]; exists {
			add(fmt.Sprintf("component %q collides", newID))
			continue
		}
		parent.Application.Components[newID] = cloneComposedComponent(child.Application.Components[id], child.BaseDir)
	}
	for _, id := range sortedKeys(actionIDs) {
		newID := actionIDs[id]
		if _, exists := parent.Application.Actions[newID]; exists {
			add(fmt.Sprintf("action %q collides", newID))
			continue
		}
		action := child.Application.Actions[id]
		if action.Intent != "" && (child.Exports == nil || !containsString(child.Exports.Intents, action.Intent)) {
			add(fmt.Sprintf("exported action %q targets private intent %q", id, action.Intent))
			continue
		}
		parent.Application.Actions[newID] = cloneComposedAction(action, alias, rw, handlerIDs, child.BaseDir)
	}
	for _, id := range sortedKeys(schemaIDs) {
		newID := schemaIDs[id]
		if _, exists := parent.Application.Schemas[newID]; exists {
			add(fmt.Sprintf("schema %q collides", newID))
			continue
		}
		parent.Application.Schemas[newID] = rebaseApplicationPath(child.Application.Schemas[id], child.BaseDir)
	}
	for _, id := range sortedKeys(tokenIDs) {
		newID := tokenIDs[id]
		if _, exists := parent.Application.Tokens[newID]; exists {
			add(fmt.Sprintf("token %q collides", newID))
			continue
		}
		parent.Application.Tokens[newID] = rebaseApplicationPath(child.Application.Tokens[id], child.BaseDir)
	}
	if child.Exports != nil {
		for _, id := range sortedKeys(handlerIDs) {
			newID := handlerIDs[id]
			if _, exists := parent.Exports.Handlers[newID]; exists {
				add(fmt.Sprintf("handler %q collides", newID))
				continue
			}
			parent.Exports.Handlers[newID] = cloneComposedHandler(child.Exports.Handlers[id], alias, rw, handlerIDs, child.BaseDir)
		}
	}
	return errs
}

func ensureApplicationMemberOrigins(def *AppDef) {
	if def == nil {
		return
	}
	origin := func(current *ApplicationMemberOrigin, member string) {
		if current.Story == "" {
			current.Story = def.App.ID
		}
		if current.Member == "" {
			current.Member = member
		}
	}
	if def.Application != nil {
		for i := range def.Application.Navigation {
			origin(&def.Application.Navigation[i].Origin, "application.navigation."+def.Application.Navigation[i].ID)
		}
		for id, page := range def.Application.Pages {
			if page == nil {
				continue
			}
			pageMember := "application.pages." + id
			origin(&page.Origin, pageMember)
			for regionID, region := range page.Regions {
				if region == nil {
					continue
				}
				regionMember := pageMember + ".regions." + regionID
				origin(&region.Origin, regionMember)
				for index := range region.Items {
					card := region.Items[index].Card
					if card != nil {
						origin(&card.Origin, fmt.Sprintf("%s.items[%d].card", regionMember, index))
					}
				}
			}
		}
		for id, component := range def.Application.Components {
			if component != nil {
				origin(&component.Origin, "application.components."+id)
			}
		}
		for id, action := range def.Application.Actions {
			if action != nil {
				origin(&action.Origin, "application.actions."+id)
			}
		}
	}
	if def.Exports != nil {
		for id, handler := range def.Exports.Handlers {
			if handler != nil {
				origin(&handler.Origin, "exports.handlers."+id)
			}
		}
	}
}

func privateApplicationElementIntents(elements []ViewElement, child *AppDef) []string {
	if child == nil {
		return nil
	}
	exported := map[string]struct{}{}
	if child.Exports != nil {
		for _, intent := range child.Exports.Intents {
			exported[intent] = struct{}{}
		}
	}
	private := map[string]struct{}{}
	check := func(intent string) {
		if intent == "" {
			return
		}
		if _, childOwned := child.Intents[intent]; !childOwned {
			return
		}
		if _, visible := exported[intent]; !visible {
			private[intent] = struct{}{}
		}
	}
	for _, element := range elements {
		check(element.AnnotateIntent)
		check(element.ChoiceIntent)
		for _, item := range element.ChoiceItems {
			check(item.Intent)
		}
	}
	return sortedKeys(private)
}

func composedApplicationID(parentID, alias, childID, id string) string {
	suffix := strings.TrimPrefix(id, childID+".")
	return parentID + "." + alias + "." + suffix
}

func cloneComposedPage(page *ApplicationPage, alias string, rw *childRewriter, components, actions map[string]string) *ApplicationPage {
	if page == nil {
		return nil
	}
	clone := *page
	clone.SemanticAliases = append([]string(nil), page.SemanticAliases...)
	clone.Regions = make(map[string]*ApplicationRegion, len(page.Regions))
	for regionID, region := range page.Regions {
		if region == nil {
			clone.Regions[regionID] = nil
			continue
		}
		regionClone := *region
		regionClone.Items = make([]ApplicationRegionItem, len(region.Items))
		for i, item := range region.Items {
			regionClone.Items[i] = item
			if item.Card == nil {
				continue
			}
			card := *item.Card
			card.ID = alias + "__" + card.ID
			card.Props = make(map[string]any, len(item.Card.Props))
			for key, value := range item.Card.Props {
				card.Props[key] = rw.rewriteAny(value)
			}
			card.Elements = cloneViewElements(card.Elements)
			for j := range card.Elements {
				card.Elements[j] = rw.rewriteViewElement(card.Elements[j])
			}
			card.Actions = append([]string(nil), card.Actions...)
			if mapped := components[card.Component]; mapped != "" {
				card.Component = mapped
			}
			for j, action := range card.Actions {
				if mapped := actions[action]; mapped != "" {
					card.Actions[j] = mapped
				}
			}
			regionClone.Items[i].Card = &card
		}
		clone.Regions[regionID] = &regionClone
	}
	return &clone
}

func cloneComposedComponent(component *ApplicationComponent, baseDir string) *ApplicationComponent {
	if component == nil {
		return nil
	}
	clone := *component
	clone.SemanticAliases = append([]string(nil), component.SemanticAliases...)
	if component.Web != nil {
		web := *component.Web
		web.Module = rebaseApplicationPath(web.Module, baseDir)
		clone.Web = &web
	}
	if component.Fallback != nil {
		fallback := *component.Fallback
		clone.Fallback = &fallback
	}
	clone.PropsSchema = rebaseApplicationPath(clone.PropsSchema, baseDir)
	return &clone
}

func cloneComposedAction(action *ApplicationAction, alias string, rw *childRewriter, handlers map[string]string, baseDir string) *ApplicationAction {
	if action == nil {
		return nil
	}
	clone := *action
	clone.SemanticAliases = append([]string(nil), action.SemanticAliases...)
	if mapped := handlers[action.Handler]; mapped != "" {
		clone.Handler = mapped
	}
	if clone.Intent != "" {
		clone.Intent = rw.rewriteIntentRef(clone.Intent)
	}
	if clone.State != "" {
		clone.State = alias + "." + strings.ReplaceAll(clone.State, "/", ".")
	}
	if clone.RoomInterface != "" {
		clone.RoomInterface = alias + "__" + clone.RoomInterface
	}
	clone.InputSchema = rebaseApplicationPath(clone.InputSchema, baseDir)
	return &clone
}

func cloneComposedHandler(handler *ApplicationHandler, alias string, rw *childRewriter, handlers map[string]string, baseDir string) *ApplicationHandler {
	if handler == nil {
		return nil
	}
	clone := *handler
	clone.SemanticAliases = append([]string(nil), handler.SemanticAliases...)
	clone.Outcomes = append([]string(nil), handler.Outcomes...)
	clone.Expose = append([]string(nil), handler.Expose...)
	clone.InputSchema = rebaseApplicationPath(clone.InputSchema, baseDir)
	clone.OutputSchema = rebaseApplicationPath(clone.OutputSchema, baseDir)
	if handler.Dispatch != nil {
		dispatch := *handler.Dispatch
		if dispatch.Intent != "" {
			dispatch.Intent = rw.rewriteIntentRef(dispatch.Intent)
		}
		if dispatch.Handler != "" {
			dispatch.Handler = handlers[dispatch.Handler]
		}
		if dispatch.State != "" {
			dispatch.State = alias + "." + strings.ReplaceAll(dispatch.State, "/", ".")
		}
		if dispatch.RoomInterface != "" {
			dispatch.RoomInterface = alias + "__" + dispatch.RoomInterface
		}
		clone.Dispatch = &dispatch
	}
	if handler.Starlark != nil {
		starlark := *handler.Starlark
		starlark.Script = rebaseApplicationPath(starlark.Script, baseDir)
		starlark.Capabilities = cloneAnyMap(starlark.Capabilities)
		clone.Starlark = &starlark
	}
	if mapped := handlers[handler.Compensation]; mapped != "" {
		clone.Compensation = mapped
	}
	if handler.Idempotency != nil {
		idempotency := *handler.Idempotency
		clone.Idempotency = &idempotency
	}
	if handler.Retry != nil {
		retry := *handler.Retry
		clone.Retry = &retry
	}
	return &clone
}

func rebaseApplicationPath(path, baseDir string) string {
	if path == "" || baseDir == "" || filepath.IsAbs(path) || strings.Contains(path, "{{") {
		return path
	}
	return filepath.Join(baseDir, path)
}

func rewriteRoomInterface(contract *RoomInterfaceDef, rw *childRewriter) *RoomInterfaceDef {
	if contract == nil {
		return nil
	}
	clone := *contract
	clone.Intents = make(map[string]Intent, len(contract.Intents))
	for name, intent := range contract.Intents {
		rw.rewriteIntent(&intent)
		clone.Intents[rw.rewriteIntentRef(name)] = intent
	}
	clone.World = make(map[string]RoomWorldContract, len(contract.World))
	for field, world := range contract.World {
		clone.World[rw.rewriteWorldKeyRef(field)] = world
	}
	return &clone
}

func rewriteImportedRoomImplementations(wrapper *State, alias string) {
	if wrapper == nil {
		return
	}
	var walk func(map[string]*State)
	walk = func(states map[string]*State) {
		for _, state := range states {
			if state == nil {
				continue
			}
			for i, name := range state.Implements {
				state.Implements[i] = alias + "__" + name
			}
			walk(state.States)
		}
	}
	walk(wrapper.States)
}
