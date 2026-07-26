package app

import (
	"fmt"
	"strings"

	yaml "github.com/goccy/go-yaml"

	"kitsoki/internal/effect"
)

const ApplicationSchemaV1 = "application/v1"

// ApplicationMemberOrigin records which story supplied a composed member.
// It is loader metadata, not part of the author or wire schemas.
type ApplicationMemberOrigin struct {
	Story  string
	Member string
}

// ApplicationContract is the optional story-owned application shell. It is
// renderer-neutral: paths and component names are declarations, never live
// Vue/DOM/TUI objects.
type ApplicationContract struct {
	Schema      string                           `yaml:"schema" json:"schema"`
	Name        string                           `yaml:"name" json:"name"`
	Description string                           `yaml:"description" json:"description"`
	SemanticRef string                           `yaml:"semantic_ref" json:"semantic_ref"`
	Data        map[string]*ApplicationData      `yaml:"data,omitempty" json:"data,omitempty"`
	Feedback    *ApplicationFeedbackPolicy       `yaml:"feedback,omitempty" json:"feedback,omitempty"`
	Shell       ApplicationShell                 `yaml:"shell,omitempty" json:"shell,omitempty"`
	Navigation  []ApplicationNavigation          `yaml:"navigation,omitempty" json:"navigation,omitempty"`
	Pages       map[string]*ApplicationPage      `yaml:"pages,omitempty" json:"pages,omitempty"`
	Components  map[string]*ApplicationComponent `yaml:"components,omitempty" json:"components,omitempty"`
	Actions     map[string]*ApplicationAction    `yaml:"actions,omitempty" json:"actions,omitempty"`
	Surfaces    map[string]*ApplicationSurface   `yaml:"surfaces,omitempty" json:"surfaces,omitempty"`
	Packages    []ApplicationPackageUse          `yaml:"packages,omitempty" json:"packages,omitempty"`
	Schemas     map[string]string                `yaml:"schemas,omitempty" json:"schemas,omitempty"`
	Tokens      map[string]string                `yaml:"tokens,omitempty" json:"tokens,omitempty"`
	Generated   bool                             `yaml:"-" json:"generated,omitempty"`
}

// ApplicationData declares one finite world value that may cross the
// story/runtime boundary into application-frame/v1.
type ApplicationData struct {
	Source      string   `yaml:"source" json:"source"`
	Sensitivity string   `yaml:"sensitivity" json:"sensitivity"`
	Policy      string   `yaml:"policy" json:"policy"`
	Pages       []string `yaml:"pages,omitempty" json:"pages,omitempty"`
}

// ApplicationPackageUse selects named members from one package pinned in the
// project's existing .kitsoki/kits.lock. Package is the manifest identity
// (namespace.name); Select entries use category.member form.
type ApplicationPackageUse struct {
	Package string   `yaml:"package" json:"package"`
	Select  []string `yaml:"select" json:"select"`
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
	ID          string                  `yaml:"id" json:"id"`
	Name        string                  `yaml:"name" json:"name"`
	Description string                  `yaml:"description" json:"description"`
	SemanticRef string                  `yaml:"semantic_ref" json:"semantic_ref"`
	Page        string                  `yaml:"page" json:"page"`
	Origin      ApplicationMemberOrigin `yaml:"-" json:"-"`
}

type ApplicationPage struct {
	Name            string                        `yaml:"name" json:"name"`
	Description     string                        `yaml:"description" json:"description"`
	SemanticRef     string                        `yaml:"semantic_ref" json:"semantic_ref"`
	SemanticAliases []string                      `yaml:"semantic_aliases,omitempty" json:"semantic_aliases,omitempty"`
	Regions         map[string]*ApplicationRegion `yaml:"regions,omitempty" json:"regions,omitempty"`
	Generated       bool                          `yaml:"-" json:"generated,omitempty"`
	Origin          ApplicationMemberOrigin       `yaml:"-" json:"-"`
}

type ApplicationRegion struct {
	Name        string                  `yaml:"name" json:"name"`
	Description string                  `yaml:"description" json:"description"`
	SemanticRef string                  `yaml:"semantic_ref" json:"semantic_ref"`
	Items       []ApplicationRegionItem `yaml:"items,omitempty" json:"items,omitempty"`
	Generated   bool                    `yaml:"-" json:"generated,omitempty"`
	Origin      ApplicationMemberOrigin `yaml:"-" json:"-"`
}

// ApplicationRegionItem is deliberately a finite union. New semantic item
// kinds must extend the contract instead of entering as renderer-specific data.
type ApplicationRegionItem struct {
	Card *ApplicationCard `yaml:"card,omitempty" json:"card,omitempty"`
}

type ApplicationCard struct {
	ID          string                        `yaml:"id" json:"id"`
	Name        string                        `yaml:"name" json:"name"`
	Description string                        `yaml:"description" json:"description"`
	SemanticRef string                        `yaml:"semantic_ref" json:"semantic_ref"`
	Component   string                        `yaml:"component,omitempty" json:"component,omitempty"`
	Props       map[string]any                `yaml:"props,omitempty" json:"props,omitempty"`
	Bindings    *ApplicationComponentBindings `yaml:"bindings,omitempty" json:"bindings,omitempty"`
	Elements    []ViewElement                 `yaml:"elements,omitempty" json:"elements,omitempty"`
	Actions     []string                      `yaml:"actions,omitempty" json:"actions,omitempty"`
	Generated   bool                          `yaml:"-" json:"generated,omitempty"`
	Origin      ApplicationMemberOrigin       `yaml:"-" json:"-"`
}

type ApplicationComponent struct {
	Name            string                        `yaml:"name" json:"name"`
	Description     string                        `yaml:"description" json:"description"`
	SemanticRef     string                        `yaml:"semantic_ref" json:"semantic_ref"`
	SemanticAliases []string                      `yaml:"semantic_aliases,omitempty" json:"semantic_aliases,omitempty"`
	Web             *ApplicationWebComponent      `yaml:"web,omitempty" json:"web,omitempty"`
	PropsSchema     string                        `yaml:"props_schema,omitempty" json:"props_schema,omitempty"`
	Events          map[string]string             `yaml:"events,omitempty" json:"events,omitempty"`
	Fallback        *ApplicationComponentFallback `yaml:"fallback,omitempty" json:"fallback,omitempty"`
	Origin          ApplicationMemberOrigin       `yaml:"-" json:"-"`
}

// ApplicationComponentBindings is the finite data/event boundary between a
// lock-verified component package member and a story application.
type ApplicationComponentBindings struct {
	Props  map[string]*ApplicationValueBinding          `yaml:"props,omitempty" json:"props,omitempty"`
	Events map[string]*ApplicationComponentEventBinding `yaml:"events,omitempty" json:"events,omitempty"`
}

type ApplicationValueBinding struct {
	Source   string   `yaml:"source" json:"source"`
	Key      string   `yaml:"key,omitempty" json:"key,omitempty"`
	Path     []string `yaml:"path,omitempty" json:"path,omitempty"`
	Value    any      `yaml:"value,omitempty" json:"value,omitempty"`
	ValueSet bool     `yaml:"-" json:"-"`
}

type ApplicationComponentEventBinding struct {
	Action string                              `yaml:"action" json:"action"`
	Input  map[string]*ApplicationValueBinding `yaml:"input,omitempty" json:"input,omitempty"`
}

func (b *ApplicationValueBinding) UnmarshalYAML(data []byte) error {
	type plain ApplicationValueBinding
	var decoded plain
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]any
	if err := yaml.Unmarshal(data, &fields); err != nil {
		return err
	}
	*b = ApplicationValueBinding(decoded)
	_, b.ValueSet = fields["value"]
	return nil
}

type ApplicationWebComponent struct {
	Module string `yaml:"module" json:"module"`
	Export string `yaml:"export,omitempty" json:"export,omitempty"`
}

type ApplicationComponentFallback struct {
	Element   string `yaml:"element" json:"element"`
	ValueProp string `yaml:"value_prop,omitempty" json:"value_prop,omitempty"`
}

type ApplicationAction struct {
	Name            string                  `yaml:"name" json:"name"`
	Description     string                  `yaml:"description" json:"description"`
	SemanticRef     string                  `yaml:"semantic_ref" json:"semantic_ref"`
	SemanticAliases []string                `yaml:"semantic_aliases,omitempty" json:"semantic_aliases,omitempty"`
	Handler         string                  `yaml:"handler,omitempty" json:"handler,omitempty"`
	Intent          string                  `yaml:"intent,omitempty" json:"intent,omitempty"`
	State           string                  `yaml:"state,omitempty" json:"state,omitempty"`
	RoomInterface   string                  `yaml:"room_interface,omitempty" json:"room_interface,omitempty"`
	InputSchema     string                  `yaml:"input_schema,omitempty" json:"input_schema,omitempty"`
	RoutingMode     string                  `yaml:"routing_mode,omitempty" json:"routing_mode,omitempty"`
	Generated       bool                    `yaml:"-" json:"generated,omitempty"`
	Origin          ApplicationMemberOrigin `yaml:"-" json:"-"`
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
	SemanticAliases        []string                    `yaml:"semantic_aliases,omitempty" json:"semantic_aliases,omitempty"`
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
	Origin                 ApplicationMemberOrigin     `yaml:"-" json:"-"`
}

type ApplicationHandlerDispatch struct {
	Intent        string `yaml:"intent,omitempty" json:"intent,omitempty"`
	Handler       string `yaml:"handler,omitempty" json:"handler,omitempty"`
	State         string `yaml:"state,omitempty" json:"state,omitempty"`
	RoomInterface string `yaml:"room_interface,omitempty" json:"room_interface,omitempty"`
	SlotsFrom     string `yaml:"slots_from,omitempty" json:"slots_from,omitempty"`
}

// ApplicationOverrides are explicit import-time whole-member replacements.
// Keys address the child contract before alias qualification.
type ApplicationOverrides struct {
	Pages      map[string]*ApplicationPage      `yaml:"pages,omitempty" json:"pages,omitempty"`
	Components map[string]*ApplicationComponent `yaml:"components,omitempty" json:"components,omitempty"`
	Actions    map[string]*ApplicationAction    `yaml:"actions,omitempty" json:"actions,omitempty"`
	Handlers   map[string]*ApplicationHandler   `yaml:"handlers,omitempty" json:"handlers,omitempty"`
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

func requireSemanticIdentity(path, rootOwner string, owners map[string]struct{}, kind, name, description, ref string, refs map[string]string) []error {
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
	if !semanticOwnerAllowed(ref, owners) {
		errs = append(errs, semanticContractError(path+".semantic_ref", fmt.Sprintf("%q must be application-qualified with %q or an imported application owner", ref, rootOwner+".")))
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
	owners := map[string]struct{}{def.App.ID: {}}
	for owner := range def.ImportedApplicationOwners {
		owners[owner] = struct{}{}
	}
	addIdentity := func(path, kind, name, description, ref string, refs map[string]string) {
		for _, err := range requireSemanticIdentity(path, def.App.ID, owners, kind, name, description, ref, refs) {
			add(err)
		}
	}
	addAliases := func(path string, aliases []string, refs map[string]string) {
		for i, alias := range aliases {
			aliasPath := fmt.Sprintf("%s.semantic_aliases[%d]", path, i)
			if !validSemanticRef(alias) || !semanticOwnerAllowed(alias, owners) {
				addf(aliasPath, "%q is not a valid root/import-qualified semantic ref", alias)
				continue
			}
			if prior, exists := refs[alias]; exists {
				addf(aliasPath, "%q duplicates %s", alias, prior)
				continue
			}
			refs[alias] = path + " (semantic alias)"
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
		if def.Exports != nil && def.Exports.Application != nil {
			exports := def.Exports.Application
			validateExportList := func(kind string, ids []string, exists func(string) bool) {
				seen := map[string]struct{}{}
				for i, id := range ids {
					path := fmt.Sprintf("exports.application.%s[%d]", kind, i)
					if _, duplicate := seen[id]; duplicate {
						addf(path, "%q is duplicated", id)
						continue
					}
					seen[id] = struct{}{}
					if !exists(id) {
						addf(path, "%q does not name an application %s member", id, kind)
					}
				}
			}
			validateExportList("navigation", exports.Navigation, func(id string) bool {
				_, ok := navigationIDs[id]
				return ok
			})
			validateExportList("pages", exports.Pages, func(id string) bool {
				return contract.Pages[id] != nil
			})
			validateExportList("data", exports.Data, func(id string) bool {
				return contract.Data[id] != nil
			})
			validateExportList("components", exports.Components, func(id string) bool {
				return contract.Components[id] != nil
			})
			validateExportList("actions", exports.Actions, func(id string) bool {
				return contract.Actions[id] != nil
			})
			validateExportList("schemas", exports.Schemas, func(id string) bool {
				_, ok := contract.Schemas[id]
				return ok
			})
			validateExportList("tokens", exports.Tokens, func(id string) bool {
				_, ok := contract.Tokens[id]
				return ok
			})
			for _, id := range exports.Actions {
				action := contract.Actions[id]
				if action != nil && action.Intent != "" && !containsString(def.Exports.Intents, action.Intent) {
					addf("exports.application.actions", "exported action %q targets private intent %q", id, action.Intent)
				}
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
			addAliases(path, page.SemanticAliases, refs)
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
					validateApplicationComponentBindings(def, contract, cardPath, card, addf)
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
			if !semanticOwnerAllowed(componentID, owners) {
				addf(path, "id %q must be qualified by the application or a verified package owner", componentID)
			}
			if component == nil {
				addf(path, "empty definition")
				continue
			}
			addIdentity(path, "component", component.Name, component.Description, component.SemanticRef, refs)
			addAliases(path, component.SemanticAliases, refs)
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
			addAliases(path, action.SemanticAliases, refs)
			targets := 0
			if action.Handler != "" {
				targets++
				if _, ok := handlers[action.Handler]; !ok {
					addf(path+".handler", "%q is not declared in exports.handlers", action.Handler)
				}
			}
			if action.Intent != "" {
				targets++
				if !dispatchIntentExists(def, action.State, action.RoomInterface, action.Intent) {
					addf(path+".intent", "%q does not resolve in state %q or the story intent library", action.Intent, action.State)
				}
			}
			if action.RoomInterface != "" {
				if _, ok := def.RoomInterfaces[action.RoomInterface]; !ok {
					addf(path+".room_interface", "%q is not declared in room_interfaces", action.RoomInterface)
				}
				if action.Intent == "" {
					addf(path+".room_interface", "requires an intent target")
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
		for name, data := range contract.Data {
			path := "application.data." + name
			if !validApplicationDataName(name) {
				addf(path, "name %q must start with a lowercase letter and contain only lowercase letters, digits, underscores, or hyphens", name)
			}
			if data == nil {
				addf(path, "empty definition")
				continue
			}
			worldKey, ok := strings.CutPrefix(data.Source, "world.")
			if !ok || worldKey == "" || strings.Contains(worldKey, ".") {
				addf(path+".source", "%q must name one declared world key as world.<key>", data.Source)
			} else if _, declared := def.World[worldKey]; !declared {
				addf(path+".source", "%q names undeclared world key %q", data.Source, worldKey)
			}
			switch data.Sensitivity {
			case "public", "internal", "sensitive", "secret":
			default:
				addf(path+".sensitivity", "%q is not one of public|internal|sensitive|secret", data.Sensitivity)
			}
			switch data.Policy {
			case "include", "exclude", "redact", "hash":
			default:
				addf(path+".policy", "%q is not one of include|exclude|redact|hash", data.Policy)
			}
			if data.Policy == "include" && (data.Sensitivity == "sensitive" || data.Sensitivity == "secret") {
				addf(path, "sensitive or secret data may not use policy include")
			}
			seenPages := map[string]struct{}{}
			for i, page := range data.Pages {
				pagePath := fmt.Sprintf("%s.pages[%d]", path, i)
				if _, duplicate := seenPages[page]; duplicate {
					addf(pagePath, "%q is duplicated", page)
					continue
				}
				seenPages[page] = struct{}{}
				if contract.Pages[page] == nil {
					addf(pagePath, "%q does not name an application page", page)
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
		addAliases(path, handler.SemanticAliases, refs)
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
			} else if !dispatchIntentExists(def, handler.Dispatch.State, handler.Dispatch.RoomInterface, handler.Dispatch.Intent) {
				addf(path+".dispatch.intent", "%q does not resolve in state %q or the story intent library", handler.Dispatch.Intent, handler.Dispatch.State)
			}
			if handler.Dispatch.RoomInterface != "" {
				if _, ok := def.RoomInterfaces[handler.Dispatch.RoomInterface]; !ok {
					addf(path+".dispatch.room_interface", "%q is not declared in room_interfaces", handler.Dispatch.RoomInterface)
				}
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
			} else {
				if !validApplicationIdempotencyKey(handler.Idempotency.Key) {
					addf(path+".idempotency.key", "must be a non-empty input.<field> path")
				}
				if strings.TrimSpace(handler.Idempotency.Scope) == "" {
					addf(path+".idempotency.scope", "is required for write and external handlers")
				}
			}
		}
		if handler.Idempotency != nil {
			switch handler.Idempotency.Scope {
			case "request", "session", "application":
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
			if !dispatchIntentExists(def, event.Dispatch.State, event.Dispatch.RoomInterface, event.Dispatch.Intent) {
				addf(path+".dispatch.intent", "%q does not resolve in state %q or the story intent library", event.Dispatch.Intent, event.Dispatch.State)
			}
		}
		if event.Dispatch.RoomInterface != "" {
			if _, ok := def.RoomInterfaces[event.Dispatch.RoomInterface]; !ok {
				addf(path+".dispatch.room_interface", "%q is not declared in room_interfaces", event.Dispatch.RoomInterface)
			}
			if event.Dispatch.Intent == "" {
				addf(path+".dispatch.room_interface", "requires an intent target")
			}
		}
		if targets != 1 {
			addf(path+".dispatch", "must name exactly one of handler or intent")
		}
	}

	return errs
}

func validApplicationIdempotencyKey(key string) bool {
	if key != strings.TrimSpace(key) || !strings.HasPrefix(key, "input.") {
		return false
	}
	segments := strings.Split(strings.TrimPrefix(key, "input."), ".")
	if len(segments) == 0 {
		return false
	}
	for _, segment := range segments {
		if segment == "" || segment != strings.TrimSpace(segment) {
			return false
		}
	}
	return true
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

func dispatchIntentExists(def *AppDef, statePath, roomInterface, intent string) bool {
	if roomInterface != "" {
		contract := def.RoomInterfaces[roomInterface]
		if contract == nil {
			return false
		}
		_, ok := contract.Intents[intent]
		return ok
	}
	return intentExists(def, statePath, intent)
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

var applicationFrameBindingPaths = map[string]struct{}{
	"application_id":           {},
	"session_id":               {},
	"revision":                 {},
	"page":                     {},
	"workflow.state":           {},
	"workflow.allowed_intents": {},
	"workflow.budget_state":    {},
	"workflow.degradation":     {},
}

func validateApplicationComponentBindings(
	def *AppDef,
	contract *ApplicationContract,
	cardPath string,
	card *ApplicationCard,
	addf func(string, string, ...any),
) {
	if card == nil || card.Bindings == nil {
		return
	}
	path := cardPath + ".bindings"
	if card.Component == "" {
		addf(path, "requires component")
		return
	}
	component := contract.Components[card.Component]
	if component == nil {
		return
	}
	if !strings.HasPrefix(component.Origin.Member, "component-package.components.") {
		addf(path, "requires a component selected from a lock-verified application package")
	}
	if len(card.Bindings.Props) > 0 && strings.TrimSpace(component.PropsSchema) == "" {
		addf(path+".props", "requires the selected component to declare props_schema")
	}
	for name, binding := range card.Bindings.Props {
		bindingPath := path + ".props." + name
		if !validApplicationDataName(name) {
			addf(bindingPath, "prop name %q must start with a lowercase letter and contain only lowercase letters, digits, underscores, or hyphens", name)
		}
		if _, legacy := card.Props[name]; legacy {
			addf(bindingPath, "duplicates legacy props.%s", name)
		}
		validateApplicationValueBinding(contract, bindingPath, binding, false, addf)
	}
	for event, eventBinding := range card.Bindings.Events {
		eventPath := path + ".events." + event
		if !validApplicationDataName(event) {
			addf(eventPath, "event name %q must start with a lowercase letter and contain only lowercase letters, digits, underscores, or hyphens", event)
		}
		if _, declared := component.Events[event]; !declared {
			addf(eventPath, "%q is not declared by component %q", event, card.Component)
		}
		if eventBinding == nil {
			addf(eventPath, "empty definition")
			continue
		}
		action := contract.Actions[eventBinding.Action]
		if action == nil {
			addf(eventPath+".action", "%q is not declared in application.actions", eventBinding.Action)
		}
		for input, binding := range eventBinding.Input {
			inputPath := eventPath + ".input." + input
			if !validApplicationDataName(input) {
				addf(inputPath, "input name %q must start with a lowercase letter and contain only lowercase letters, digits, underscores, or hyphens", input)
			}
			validateApplicationValueBinding(contract, inputPath, binding, true, addf)
			if action != nil && action.Intent != "" {
				if intent := applicationIntent(def, action.State, action.RoomInterface, action.Intent); intent != nil {
					if _, declared := intent.Slots[input]; !declared {
						addf(inputPath, "%q is not a slot of intent %q", input, action.Intent)
					}
				}
			}
		}
	}
}

func validateApplicationValueBinding(
	contract *ApplicationContract,
	path string,
	binding *ApplicationValueBinding,
	allowEvent bool,
	addf func(string, string, ...any),
) {
	if binding == nil {
		addf(path, "empty definition")
		return
	}
	switch binding.Source {
	case "literal":
		if binding.Key != "" || len(binding.Path) != 0 {
			addf(path, "literal binding may only declare value")
		}
		if !binding.ValueSet {
			addf(path+".value", "is required for a literal binding (use value: null for JSON null)")
		}
	case "data":
		if binding.Key == "" || contract.Data[binding.Key] == nil {
			addf(path+".key", "%q is not declared in application.data", binding.Key)
		}
		if len(binding.Path) != 0 {
			addf(path+".path", "data binding does not support nested paths")
		}
	case "frame":
		framePath := strings.Join(binding.Path, ".")
		if _, ok := applicationFrameBindingPaths[framePath]; !ok {
			addf(path+".path", "%q is not a supported canonical frame path", framePath)
		}
		if binding.Key != "" {
			addf(path+".key", "frame binding uses path, not key")
		}
	case "route":
		if !validApplicationDataName(binding.Key) {
			addf(path+".key", "%q is not a valid route parameter name", binding.Key)
		}
		if len(binding.Path) != 0 {
			addf(path+".path", "route binding does not support nested paths")
		}
	case "event":
		if !allowEvent {
			addf(path+".source", "event is only valid in component event input mappings")
		}
		if binding.Key != "" {
			addf(path+".key", "event binding uses path, not key")
		}
		for i, segment := range binding.Path {
			if !validApplicationDataName(segment) {
				addf(fmt.Sprintf("%s.path[%d]", path, i), "%q is not a valid event payload member", segment)
			}
		}
	default:
		expected := "literal|data|frame|route"
		if allowEvent {
			expected += "|event"
		}
		addf(path+".source", "%q is not one of %s", binding.Source, expected)
	}
	if binding.Source != "literal" && binding.ValueSet {
		addf(path+".value", "is only valid for a literal binding")
	}
}

func applicationIntent(def *AppDef, statePath, roomInterface, intent string) *Intent {
	if def == nil {
		return nil
	}
	if roomInterface != "" {
		if contract := def.RoomInterfaces[roomInterface]; contract != nil {
			if declared, ok := contract.Intents[intent]; ok {
				copy := declared
				return &copy
			}
		}
		return nil
	}
	if declared, ok := def.Intents[intent]; ok {
		copy := declared
		return &copy
	}
	var found *Intent
	walkStates(def.States, "", func(path string, state *State) {
		if found != nil || state == nil || (statePath != "" && path != statePath) {
			return
		}
		if declared, ok := state.Intents[intent]; ok {
			copy := declared
			found = &copy
		}
	})
	return found
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

func validApplicationDataName(name string) bool {
	if name == "" || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for _, r := range name[1:] {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func semanticOwnerAllowed(ref string, owners map[string]struct{}) bool {
	for owner := range owners {
		if owner != "" && (ref == owner || strings.HasPrefix(ref, owner+".")) {
			return true
		}
	}
	return false
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
		if src.Exports.Application != nil {
			if dst.Exports.Application == nil {
				dst.Exports.Application = &ApplicationExports{}
			}
			mergeExportList := func(kind string, dstList *[]string, srcList []string) {
				seen := make(map[string]struct{}, len(*dstList))
				for _, id := range *dstList {
					seen[id] = struct{}{}
				}
				for _, id := range srcList {
					if _, exists := seen[id]; exists {
						addErr(fmt.Sprintf("include: exported application %s %q is already declared", kind, id))
						continue
					}
					*dstList = append(*dstList, id)
					seen[id] = struct{}{}
				}
			}
			mergeExportList("navigation", &dst.Exports.Application.Navigation, src.Exports.Application.Navigation)
			mergeExportList("page", &dst.Exports.Application.Pages, src.Exports.Application.Pages)
			mergeExportList("data", &dst.Exports.Application.Data, src.Exports.Application.Data)
			mergeExportList("component", &dst.Exports.Application.Components, src.Exports.Application.Components)
			mergeExportList("action", &dst.Exports.Application.Actions, src.Exports.Application.Actions)
			mergeExportList("schema", &dst.Exports.Application.Schemas, src.Exports.Application.Schemas)
			mergeExportList("token", &dst.Exports.Application.Tokens, src.Exports.Application.Tokens)
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
	dst.Packages = append(dst.Packages, src.Packages...)
	for name, data := range src.Data {
		if _, exists := dst.Data[name]; exists {
			addErr(fmt.Sprintf("include: application data %q is already declared", name))
			continue
		}
		if dst.Data == nil {
			dst.Data = map[string]*ApplicationData{}
		}
		dst.Data[name] = data
	}
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
	for name, schema := range src.Schemas {
		if _, exists := dst.Schemas[name]; exists {
			addErr(fmt.Sprintf("include: application schema %q is already declared", name))
			continue
		}
		if dst.Schemas == nil {
			dst.Schemas = map[string]string{}
		}
		dst.Schemas[name] = schema
	}
	for name, token := range src.Tokens {
		if _, exists := dst.Tokens[name]; exists {
			addErr(fmt.Sprintf("include: application token %q is already declared", name))
			continue
		}
		if dst.Tokens == nil {
			dst.Tokens = map[string]string{}
		}
		dst.Tokens[name] = token
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
