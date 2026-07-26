package app

import (
	"fmt"
	"strings"

	"kitsoki/internal/effect"
)

const ApplicationSchemaV1 = "application/v1"

// ApplicationContract is the optional story-owned application shell. It is
// renderer-neutral: paths and component names are declarations, never live
// Vue/DOM/TUI objects.
type ApplicationContract struct {
	Schema      string                           `yaml:"schema" json:"schema"`
	Name        string                           `yaml:"name" json:"name"`
	Description string                           `yaml:"description" json:"description"`
	SemanticRef string                           `yaml:"semantic_ref" json:"semantic_ref"`
	Feedback    *ApplicationFeedbackPolicy       `yaml:"feedback,omitempty" json:"feedback,omitempty"`
	Shell       ApplicationShell                 `yaml:"shell,omitempty" json:"shell,omitempty"`
	Navigation  []ApplicationNavigation          `yaml:"navigation,omitempty" json:"navigation,omitempty"`
	Pages       map[string]*ApplicationPage      `yaml:"pages,omitempty" json:"pages,omitempty"`
	Components  map[string]*ApplicationComponent `yaml:"components,omitempty" json:"components,omitempty"`
	Actions     map[string]*ApplicationAction    `yaml:"actions,omitempty" json:"actions,omitempty"`
	Surfaces    map[string]*ApplicationSurface   `yaml:"surfaces,omitempty" json:"surfaces,omitempty"`
}

type ApplicationFeedbackPolicy struct {
	Context map[string]*ApplicationFeedbackContext `yaml:"context,omitempty" json:"context,omitempty"`
}

type ApplicationFeedbackContext struct {
	Source      string `yaml:"source" json:"source"`
	Sensitivity string `yaml:"sensitivity" json:"sensitivity"`
	Policy      string `yaml:"policy" json:"policy"`
}

type ApplicationShell struct {
	Contract     string `yaml:"contract,omitempty" json:"contract,omitempty"`
	Presentation string `yaml:"presentation,omitempty" json:"presentation,omitempty"`
	Entry        string `yaml:"entry,omitempty" json:"entry,omitempty"`
}

type ApplicationNavigation struct {
	ID          string `yaml:"id" json:"id"`
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
	SemanticRef string `yaml:"semantic_ref" json:"semantic_ref"`
	Page        string `yaml:"page" json:"page"`
}

type ApplicationPage struct {
	Name        string                        `yaml:"name" json:"name"`
	Description string                        `yaml:"description" json:"description"`
	SemanticRef string                        `yaml:"semantic_ref" json:"semantic_ref"`
	Regions     map[string]*ApplicationRegion `yaml:"regions,omitempty" json:"regions,omitempty"`
}

type ApplicationRegion struct {
	Name        string                  `yaml:"name" json:"name"`
	Description string                  `yaml:"description" json:"description"`
	SemanticRef string                  `yaml:"semantic_ref" json:"semantic_ref"`
	Items       []ApplicationRegionItem `yaml:"items,omitempty" json:"items,omitempty"`
}

// ApplicationRegionItem is deliberately a finite union. New semantic item
// kinds must extend the contract instead of entering as renderer-specific data.
type ApplicationRegionItem struct {
	Card *ApplicationCard `yaml:"card,omitempty" json:"card,omitempty"`
}

type ApplicationCard struct {
	ID          string         `yaml:"id" json:"id"`
	Name        string         `yaml:"name" json:"name"`
	Description string         `yaml:"description" json:"description"`
	SemanticRef string         `yaml:"semantic_ref" json:"semantic_ref"`
	Component   string         `yaml:"component,omitempty" json:"component,omitempty"`
	Props       map[string]any `yaml:"props,omitempty" json:"props,omitempty"`
	Actions     []string       `yaml:"actions,omitempty" json:"actions,omitempty"`
}

type ApplicationComponent struct {
	Name        string                        `yaml:"name" json:"name"`
	Description string                        `yaml:"description" json:"description"`
	SemanticRef string                        `yaml:"semantic_ref" json:"semantic_ref"`
	Web         *ApplicationWebComponent      `yaml:"web,omitempty" json:"web,omitempty"`
	PropsSchema string                        `yaml:"props_schema,omitempty" json:"props_schema,omitempty"`
	Fallback    *ApplicationComponentFallback `yaml:"fallback,omitempty" json:"fallback,omitempty"`
}

type ApplicationWebComponent struct {
	Module string `yaml:"module" json:"module"`
	Export string `yaml:"export,omitempty" json:"export,omitempty"`
}

type ApplicationComponentFallback struct {
	Element string `yaml:"element" json:"element"`
}

type ApplicationAction struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
	SemanticRef string `yaml:"semantic_ref" json:"semantic_ref"`
	Handler     string `yaml:"handler,omitempty" json:"handler,omitempty"`
	Intent      string `yaml:"intent,omitempty" json:"intent,omitempty"`
	State       string `yaml:"state,omitempty" json:"state,omitempty"`
	InputSchema string `yaml:"input_schema,omitempty" json:"input_schema,omitempty"`
	RoutingMode string `yaml:"routing_mode,omitempty" json:"routing_mode,omitempty"`
}

type ApplicationSurface struct {
	Presentation string                    `yaml:"presentation,omitempty" json:"presentation,omitempty"`
	Entry        string                    `yaml:"entry,omitempty" json:"entry,omitempty"`
	Reuse        string                    `yaml:"reuse,omitempty" json:"reuse,omitempty"`
	Projection   string                    `yaml:"projection,omitempty" json:"projection,omitempty"`
	Native       *ApplicationNativeSurface `yaml:"native,omitempty" json:"native,omitempty"`
}

type ApplicationNativeSurface struct {
	Commands []string `yaml:"commands,omitempty" json:"commands,omitempty"`
}

type ApplicationHandler struct {
	Name                   string                      `yaml:"name" json:"name"`
	Description            string                      `yaml:"description" json:"description"`
	SemanticRef            string                      `yaml:"semantic_ref" json:"semantic_ref"`
	InputSchema            string                      `yaml:"input_schema" json:"input_schema"`
	OutputSchema           string                      `yaml:"output_schema" json:"output_schema"`
	Session                string                      `yaml:"session" json:"session"`
	Effect                 effect.Effect               `yaml:"effect" json:"effect"`
	RoutingMode            string                      `yaml:"routing_mode,omitempty" json:"routing_mode,omitempty"`
	Outcomes               []string                    `yaml:"outcomes" json:"outcomes"`
	Dispatch               *ApplicationHandlerDispatch `yaml:"dispatch,omitempty" json:"dispatch,omitempty"`
	Starlark               *ApplicationStarlarkHandler `yaml:"starlark,omitempty" json:"starlark,omitempty"`
	Expose                 []string                    `yaml:"expose,omitempty" json:"expose,omitempty"`
	Idempotency            *HandlerIdempotencyPolicy   `yaml:"idempotency,omitempty" json:"idempotency,omitempty"`
	Retry                  *HandlerRetryPolicy         `yaml:"retry,omitempty" json:"retry,omitempty"`
	Compensation           string                      `yaml:"compensation,omitempty" json:"compensation,omitempty"`
	CompensationImpossible string                      `yaml:"compensation_impossible,omitempty" json:"compensation_impossible,omitempty"`
}

type ApplicationHandlerDispatch struct {
	Intent    string `yaml:"intent,omitempty" json:"intent,omitempty"`
	Handler   string `yaml:"handler,omitempty" json:"handler,omitempty"`
	State     string `yaml:"state,omitempty" json:"state,omitempty"`
	SlotsFrom string `yaml:"slots_from,omitempty" json:"slots_from,omitempty"`
}

type ApplicationStarlarkHandler struct {
	Script       string         `yaml:"script" json:"script"`
	Capabilities map[string]any `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
}

type HandlerIdempotencyPolicy struct {
	Key   string `yaml:"key,omitempty" json:"key,omitempty"`
	Scope string `yaml:"scope,omitempty" json:"scope,omitempty"`
}

type HandlerRetryPolicy struct {
	MaxAttempts int `yaml:"max_attempts,omitempty" json:"max_attempts,omitempty"`
}

type ApplicationEvent struct {
	Source      string                      `yaml:"source" json:"source"`
	InputSchema string                      `yaml:"input_schema" json:"input_schema"`
	Session     string                      `yaml:"session" json:"session"`
	Mode        string                      `yaml:"mode" json:"mode"`
	RoutingMode string                      `yaml:"routing_mode,omitempty" json:"routing_mode,omitempty"`
	Dispatch    *ApplicationHandlerDispatch `yaml:"dispatch" json:"dispatch"`
}

func semanticContractError(path, message string) error {
	return fmt.Errorf("%s: %s", path, message)
}

func requireSemanticIdentity(path, appID, kind, name, description, ref string, refs map[string]string) []error {
	var errs []error
	if strings.TrimSpace(name) == "" {
		errs = append(errs, semanticContractError(path+".name", "is required"))
	}
	if strings.TrimSpace(description) == "" {
		errs = append(errs, semanticContractError(path+".description", "is required"))
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		errs = append(errs, semanticContractError(path+".semantic_ref", "is required"))
		return errs
	}
	if !validSemanticRef(ref) {
		errs = append(errs, semanticContractError(path+".semantic_ref", fmt.Sprintf("%q is not a valid dotted semantic ref", ref)))
	}
	if appID != "" && ref != appID && !strings.HasPrefix(ref, appID+".") {
		errs = append(errs, semanticContractError(path+".semantic_ref", fmt.Sprintf("%q must be application-qualified with %q", ref, appID+".")))
	}
	if prior, ok := refs[ref]; ok {
		errs = append(errs, semanticContractError(path+".semantic_ref", fmt.Sprintf("%q duplicates %s", ref, prior)))
	} else {
		refs[ref] = path + " (" + kind + ")"
	}
	return errs
}

// validateApplicationContract performs the referential and policy checks that
// make the author contract safe to compile onto multiple surfaces. It is a
// no-op for legacy stories with no application/handlers/events declarations.
func validateApplicationContract(def *AppDef, file string) []error {
	if def == nil || (def.Application == nil && def.Exports == nil && len(def.Events) == 0) {
		return nil
	}
	var errs []error
	add := func(err error) {
		if err != nil {
			errs = append(errs, &ValidationError{File: file, Message: err.Error()})
		}
	}
	addf := func(path, format string, args ...any) {
		add(semanticContractError(path, fmt.Sprintf(format, args...)))
	}
	addIdentity := func(path, kind, name, description, ref string, refs map[string]string) {
		for _, err := range requireSemanticIdentity(path, def.App.ID, kind, name, description, ref, refs) {
			add(err)
		}
	}

	refs := map[string]string{}
	handlers := map[string]*ApplicationHandler{}
	if def.Exports != nil {
		handlers = def.Exports.Handlers
	}

	if contract := def.Application; contract != nil {
		if contract.Schema != ApplicationSchemaV1 {
			addf("application.schema", "must be %q (got %q)", ApplicationSchemaV1, contract.Schema)
		}
		addIdentity("application", "application", contract.Name, contract.Description, contract.SemanticRef, refs)
		if len(contract.Pages) == 0 {
			addf("application.pages", "must declare at least one page")
		}
		switch contract.Shell.Presentation {
		case "", "none", "default", "custom":
		default:
			addf("application.shell.presentation", "%q is not one of none|default|custom", contract.Shell.Presentation)
		}
		if contract.Shell.Entry != "" {
			if _, ok := contract.Pages[contract.Shell.Entry]; !ok {
				addf("application.shell.entry", "%q does not name an application page", contract.Shell.Entry)
			}
		}

		navigationIDs := map[string]struct{}{}
		for i, nav := range contract.Navigation {
			path := fmt.Sprintf("application.navigation[%d]", i)
			if strings.TrimSpace(nav.ID) == "" {
				addf(path+".id", "is required")
			} else if _, exists := navigationIDs[nav.ID]; exists {
				addf(path+".id", "%q is duplicated", nav.ID)
			} else {
				navigationIDs[nav.ID] = struct{}{}
			}
			addIdentity(path, "navigation", nav.Name, nav.Description, nav.SemanticRef, refs)
			if _, ok := contract.Pages[nav.Page]; !ok {
				addf(path+".page", "%q does not name an application page", nav.Page)
			}
		}

		for _, pageID := range sortedKeys(contract.Pages) {
			page := contract.Pages[pageID]
			path := "application.pages." + pageID
			if page == nil {
				addf(path, "empty definition")
				continue
			}
			addIdentity(path, "page", page.Name, page.Description, page.SemanticRef, refs)
			for _, regionID := range sortedKeys(page.Regions) {
				region := page.Regions[regionID]
				regionPath := path + ".regions." + regionID
				if region == nil {
					addf(regionPath, "empty definition")
					continue
				}
				addIdentity(regionPath, "region", region.Name, region.Description, region.SemanticRef, refs)
				for i, item := range region.Items {
					itemPath := fmt.Sprintf("%s.items[%d]", regionPath, i)
					if item.Card == nil {
						addf(itemPath, "must declare exactly one supported item kind (card)")
						continue
					}
					card := item.Card
					cardPath := itemPath + ".card"
					if strings.TrimSpace(card.ID) == "" {
						addf(cardPath+".id", "is required")
					}
					addIdentity(cardPath, "card", card.Name, card.Description, card.SemanticRef, refs)
					if card.Component != "" && !isBuiltinApplicationComponent(card.Component) {
						if _, ok := contract.Components[card.Component]; !ok {
							addf(cardPath+".component", "%q is not declared in application.components", card.Component)
						}
					}
					for actionIndex, actionID := range card.Actions {
						if _, ok := contract.Actions[actionID]; !ok {
							addf(fmt.Sprintf("%s.actions[%d]", cardPath, actionIndex), "%q is not declared in application.actions", actionID)
						}
					}
				}
			}
		}

		for _, componentID := range sortedKeys(contract.Components) {
			component := contract.Components[componentID]
			path := "application.components." + componentID
			if !isApplicationQualifiedID(def.App.ID, componentID) {
				addf(path, "id %q must be application-qualified with %q", componentID, def.App.ID+".")
			}
			if component == nil {
				addf(path, "empty definition")
				continue
			}
			addIdentity(path, "component", component.Name, component.Description, component.SemanticRef, refs)
			if component.Web != nil && strings.TrimSpace(component.Web.Module) == "" {
				addf(path+".web.module", "is required when web is declared")
			}
			if component.Web != nil && requiresComponentFallback(contract.Surfaces) {
				if component.Fallback == nil || strings.TrimSpace(component.Fallback.Element) == "" {
					addf(path+".fallback", "is required because the custom web component is used on a non-web surface")
				}
			}
			if component.Fallback != nil && !isBuiltinApplicationComponent(component.Fallback.Element) {
				addf(path+".fallback.element", "%q is not a built-in semantic element", component.Fallback.Element)
			}
		}

		for _, actionID := range sortedKeys(contract.Actions) {
			action := contract.Actions[actionID]
			path := "application.actions." + actionID
			if !isApplicationQualifiedID(def.App.ID, actionID) {
				addf(path, "id %q must be application-qualified with %q", actionID, def.App.ID+".")
			}
			if action == nil {
				addf(path, "empty definition")
				continue
			}
			addIdentity(path, "action", action.Name, action.Description, action.SemanticRef, refs)
			targets := 0
			if action.Handler != "" {
				targets++
				if _, ok := handlers[action.Handler]; !ok {
					addf(path+".handler", "%q is not declared in exports.handlers", action.Handler)
				}
			}
			if action.Intent != "" {
				targets++
				if !intentExists(def, action.State, action.Intent) {
					addf(path+".intent", "%q does not resolve in state %q or the story intent library", action.Intent, action.State)
				}
			}
			if targets != 1 {
				addf(path, "must bind exactly one of handler or intent")
			}
			validateRoutingMode(path+".routing_mode", action.RoutingMode, addf)
		}

		for surfaceID, surface := range contract.Surfaces {
			path := "application.surfaces." + surfaceID
			if !isApplicationSurface(surfaceID) {
				addf(path, "unknown surface (expected web|vscode|tui|cli|mcp|jsonrpc)")
			}
			if surface == nil {
				addf(path, "empty definition")
				continue
			}
			switch surface.Presentation {
			case "", "none", "default", "custom":
			default:
				addf(path+".presentation", "%q is not one of none|default|custom", surface.Presentation)
			}
			if surface.Reuse != "" {
				if _, ok := contract.Surfaces[surface.Reuse]; !ok {
					addf(path+".reuse", "%q is not a declared surface", surface.Reuse)
				}
			}
			if surface.Native != nil {
				for i, command := range surface.Native.Commands {
					if _, ok := contract.Actions[command]; !ok {
						addf(fmt.Sprintf("%s.native.commands[%d]", path, i), "%q is not declared in application.actions", command)
					}
				}
			}
		}

		if contract.Feedback != nil {
			for name, context := range contract.Feedback.Context {
				path := "application.feedback.context." + name
				if context == nil {
					addf(path, "empty definition")
					continue
				}
				if strings.TrimSpace(context.Source) == "" {
					addf(path+".source", "is required")
				}
				switch context.Sensitivity {
				case "public", "internal", "sensitive", "secret":
				default:
					addf(path+".sensitivity", "%q is not one of public|internal|sensitive|secret", context.Sensitivity)
				}
				switch context.Policy {
				case "include", "exclude", "redact", "hash":
				default:
					addf(path+".policy", "%q is not one of include|exclude|redact|hash", context.Policy)
				}
				if context.Policy == "include" && (context.Sensitivity == "sensitive" || context.Sensitivity == "secret") {
					addf(path, "sensitive or secret context may not use policy include")
				}
			}
		}
	}

	for _, handlerID := range sortedKeys(handlers) {
		handler := handlers[handlerID]
		path := "exports.handlers." + handlerID
		if !isApplicationQualifiedID(def.App.ID, handlerID) {
			addf(path, "id %q must be application-qualified with %q", handlerID, def.App.ID+".")
		}
		if handler == nil {
			addf(path, "empty definition")
			continue
		}
		addIdentity(path, "handler", handler.Name, handler.Description, handler.SemanticRef, refs)
		if strings.TrimSpace(handler.InputSchema) == "" {
			addf(path+".input_schema", "is required")
		}
		if strings.TrimSpace(handler.OutputSchema) == "" {
			addf(path+".output_schema", "is required")
		}
		switch handler.Session {
		case "none", "required", "create":
		default:
			addf(path+".session", "%q is not one of none|required|create", handler.Session)
		}
		if !handler.Effect.Valid() {
			addf(path+".effect", "%q is not one of pure|read|write|external", handler.Effect)
		}
		validateRoutingMode(path+".routing_mode", handler.RoutingMode, addf)
		if len(handler.Outcomes) == 0 {
			addf(path+".outcomes", "must declare at least one named outcome")
		}
		outcomes := map[string]struct{}{}
		for i, outcome := range handler.Outcomes {
			if strings.TrimSpace(outcome) == "" {
				addf(fmt.Sprintf("%s.outcomes[%d]", path, i), "must not be empty")
			} else if _, exists := outcomes[outcome]; exists {
				addf(fmt.Sprintf("%s.outcomes[%d]", path, i), "%q is duplicated", outcome)
			} else {
				outcomes[outcome] = struct{}{}
			}
		}
		if (handler.Dispatch == nil) == (handler.Starlark == nil) {
			addf(path, "must declare exactly one of dispatch or starlark")
		}
		if handler.Dispatch != nil {
			if handler.Dispatch.Intent == "" || handler.Dispatch.Handler != "" {
				addf(path+".dispatch", "a handler dispatch must name exactly one intent")
			} else if !intentExists(def, handler.Dispatch.State, handler.Dispatch.Intent) {
				addf(path+".dispatch.intent", "%q does not resolve in state %q or the story intent library", handler.Dispatch.Intent, handler.Dispatch.State)
			}
			if handler.Dispatch.SlotsFrom != "" && handler.Dispatch.SlotsFrom != "input" {
				addf(path+".dispatch.slots_from", "%q is not supported (expected input)", handler.Dispatch.SlotsFrom)
			}
		}
		if handler.Starlark != nil && strings.TrimSpace(handler.Starlark.Script) == "" {
			addf(path+".starlark.script", "is required")
		}
		exposed := map[string]struct{}{}
		for i, adapter := range handler.Expose {
			if !isApplicationAdapter(adapter) {
				addf(fmt.Sprintf("%s.expose[%d]", path, i), "%q is not one of jsonrpc|mcp|cli|web|vscode|tui", adapter)
			} else if _, exists := exposed[adapter]; exists {
				addf(fmt.Sprintf("%s.expose[%d]", path, i), "%q is duplicated", adapter)
			} else {
				exposed[adapter] = struct{}{}
			}
		}
		if handler.Effect == effect.Write || handler.Effect == effect.External {
			if handler.Idempotency == nil {
				addf(path+".idempotency", "is required for write and external handlers")
			}
		}
		if handler.Idempotency != nil {
			switch handler.Idempotency.Scope {
			case "", "request", "session", "application":
			default:
				addf(path+".idempotency.scope", "%q is not one of request|session|application", handler.Idempotency.Scope)
			}
		}
		if handler.Retry != nil {
			if handler.Retry.MaxAttempts < 1 {
				addf(path+".retry.max_attempts", "must be at least 1")
			}
			if handler.Effect == effect.External && handler.Retry.MaxAttempts > 1 {
				if (handler.Compensation == "") == (handler.CompensationImpossible == "") {
					addf(path, "retryable external handler must declare exactly one of compensation or compensation_impossible")
				}
			}
		}
		if handler.Compensation != "" {
			if handler.Compensation == handlerID {
				addf(path+".compensation", "may not reference the handler itself")
			} else if _, ok := handlers[handler.Compensation]; !ok {
				addf(path+".compensation", "%q is not declared in exports.handlers", handler.Compensation)
			}
		}
	}

	sources := map[string]string{}
	for _, eventID := range sortedKeys(def.Events) {
		event := def.Events[eventID]
		path := "events." + eventID
		if event == nil {
			addf(path, "empty definition")
			continue
		}
		if strings.TrimSpace(event.Source) == "" {
			addf(path+".source", "is required")
		} else if prior, exists := sources[event.Source]; exists {
			addf(path+".source", "%q is already bound by %s", event.Source, prior)
		} else {
			sources[event.Source] = path
		}
		if strings.TrimSpace(event.InputSchema) == "" {
			addf(path+".input_schema", "is required")
		}
		switch event.Session {
		case "none", "required", "create":
		default:
			addf(path+".session", "%q is not one of none|required|create", event.Session)
		}
		switch event.Mode {
		case "background", "interrupt":
		default:
			addf(path+".mode", "%q is not one of background|interrupt", event.Mode)
		}
		validateRoutingMode(path+".routing_mode", event.RoutingMode, addf)
		if event.Dispatch == nil {
			addf(path+".dispatch", "is required")
			continue
		}
		targets := 0
		if event.Dispatch.Handler != "" {
			targets++
			if _, ok := handlers[event.Dispatch.Handler]; !ok {
				addf(path+".dispatch.handler", "%q is not declared in exports.handlers", event.Dispatch.Handler)
			}
		}
		if event.Dispatch.Intent != "" {
			targets++
			if !intentExists(def, event.Dispatch.State, event.Dispatch.Intent) {
				addf(path+".dispatch.intent", "%q does not resolve in state %q or the story intent library", event.Dispatch.Intent, event.Dispatch.State)
			}
		}
		if targets != 1 {
			addf(path+".dispatch", "must name exactly one of handler or intent")
		}
	}

	return errs
}

func validateRoutingMode(path, mode string, addf func(string, string, ...any)) {
	switch mode {
	case "", "exact", "synonym", "semantic", "llm", "off":
	default:
		addf(path, "%q is not one of exact|synonym|semantic|llm|off", mode)
	}
}

func intentExists(def *AppDef, statePath, intent string) bool {
	if def == nil || strings.TrimSpace(intent) == "" {
		return false
	}
	if _, ok := def.Intents[intent]; ok {
		return true
	}
	if statePath != "" {
		state, ok := def.LookupState(StatePath(statePath))
		if !ok || state == nil {
			return false
		}
		if _, ok := state.Intents[intent]; ok {
			return true
		}
		_, ok = state.On[intent]
		return ok
	}
	var found bool
	walkStates(def.States, "", func(_ string, state *State) {
		if found || state == nil {
			return
		}
		if _, ok := state.Intents[intent]; ok {
			found = true
			return
		}
		if _, ok := state.On[intent]; ok {
			found = true
		}
	})
	return found
}

func isBuiltinApplicationComponent(name string) bool {
	switch name {
	case "prose", "form", "list", "table", "artifact", "status",
		"heading", "code", "template", "kv", "banner", "choice", "media":
		return true
	default:
		return false
	}
}

func requiresComponentFallback(surfaces map[string]*ApplicationSurface) bool {
	for name, surface := range surfaces {
		if name == "web" || surface == nil || surface.Presentation == "none" {
			continue
		}
		if name == "vscode" && surface.Reuse == "web" {
			continue
		}
		return true
	}
	return false
}

func isApplicationSurface(name string) bool {
	switch name {
	case "web", "vscode", "tui", "cli", "mcp", "jsonrpc":
		return true
	default:
		return false
	}
}

func isApplicationAdapter(name string) bool {
	switch name {
	case "jsonrpc", "mcp", "cli", "web", "vscode", "tui":
		return true
	default:
		return false
	}
}

func isApplicationQualifiedID(appID, id string) bool {
	return appID != "" && strings.HasPrefix(id, appID+".") && len(id) > len(appID)+1
}

func validSemanticRef(ref string) bool {
	parts := strings.Split(ref, ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for i, r := range part {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9' && i > 0) || r == '_' || r == '-' {
				continue
			}
			return false
		}
	}
	return true
}

// mergeApplicationDeclarations folds include-file application fragments into
// the root definition. Imports have a separate namespace/visibility contract;
// this helper is only for cloak-style same-story file splitting.
func mergeApplicationDeclarations(dst, src *AppDef, addErr func(string)) {
	if src.Application != nil {
		if dst.Application == nil {
			dst.Application = src.Application
		} else {
			mergeApplicationContract(dst.Application, src.Application, addErr)
		}
	}

	if src.Exports != nil {
		if dst.Exports == nil {
			dst.Exports = &ExportsBlock{}
		}
		exportedIntents := make(map[string]struct{}, len(dst.Exports.Intents))
		for _, name := range dst.Exports.Intents {
			exportedIntents[name] = struct{}{}
		}
		for _, name := range src.Exports.Intents {
			if _, exists := exportedIntents[name]; exists {
				addErr(fmt.Sprintf("include: exported intent %q is already declared", name))
				continue
			}
			dst.Exports.Intents = append(dst.Exports.Intents, name)
			exportedIntents[name] = struct{}{}
		}
		for name, handler := range src.Exports.Handlers {
			if _, exists := dst.Exports.Handlers[name]; exists {
				addErr(fmt.Sprintf("include: exported handler %q is already declared", name))
				continue
			}
			if dst.Exports.Handlers == nil {
				dst.Exports.Handlers = map[string]*ApplicationHandler{}
			}
			dst.Exports.Handlers[name] = handler
		}
	}

	for name, event := range src.Events {
		if _, exists := dst.Events[name]; exists {
			addErr(fmt.Sprintf("include: event %q is already declared", name))
			continue
		}
		if dst.Events == nil {
			dst.Events = map[string]*ApplicationEvent{}
		}
		dst.Events[name] = event
	}
}

func mergeApplicationContract(dst, src *ApplicationContract, addErr func(string)) {
	mergeScalar := func(path string, dstValue *string, srcValue string) {
		if srcValue == "" {
			return
		}
		if *dstValue != "" {
			addErr(fmt.Sprintf("include: %s is already declared", path))
			return
		}
		*dstValue = srcValue
	}
	mergeScalar("application.schema", &dst.Schema, src.Schema)
	mergeScalar("application.name", &dst.Name, src.Name)
	mergeScalar("application.description", &dst.Description, src.Description)
	mergeScalar("application.semantic_ref", &dst.SemanticRef, src.SemanticRef)
	mergeScalar("application.shell.contract", &dst.Shell.Contract, src.Shell.Contract)
	mergeScalar("application.shell.presentation", &dst.Shell.Presentation, src.Shell.Presentation)
	mergeScalar("application.shell.entry", &dst.Shell.Entry, src.Shell.Entry)

	dst.Navigation = append(dst.Navigation, src.Navigation...)
	for name, page := range src.Pages {
		if _, exists := dst.Pages[name]; exists {
			addErr(fmt.Sprintf("include: application page %q is already declared", name))
			continue
		}
		if dst.Pages == nil {
			dst.Pages = map[string]*ApplicationPage{}
		}
		dst.Pages[name] = page
	}
	for name, component := range src.Components {
		if _, exists := dst.Components[name]; exists {
			addErr(fmt.Sprintf("include: application component %q is already declared", name))
			continue
		}
		if dst.Components == nil {
			dst.Components = map[string]*ApplicationComponent{}
		}
		dst.Components[name] = component
	}
	for name, action := range src.Actions {
		if _, exists := dst.Actions[name]; exists {
			addErr(fmt.Sprintf("include: application action %q is already declared", name))
			continue
		}
		if dst.Actions == nil {
			dst.Actions = map[string]*ApplicationAction{}
		}
		dst.Actions[name] = action
	}
	for name, surface := range src.Surfaces {
		if _, exists := dst.Surfaces[name]; exists {
			addErr(fmt.Sprintf("include: application surface %q is already declared", name))
			continue
		}
		if dst.Surfaces == nil {
			dst.Surfaces = map[string]*ApplicationSurface{}
		}
		dst.Surfaces[name] = surface
	}
	if src.Feedback != nil {
		if dst.Feedback == nil {
			dst.Feedback = &ApplicationFeedbackPolicy{}
		}
		for name, context := range src.Feedback.Context {
			if _, exists := dst.Feedback.Context[name]; exists {
				addErr(fmt.Sprintf("include: application feedback context %q is already declared", name))
				continue
			}
			if dst.Feedback.Context == nil {
				dst.Feedback.Context = map[string]*ApplicationFeedbackContext{}
			}
			dst.Feedback.Context[name] = context
		}
	}
}
