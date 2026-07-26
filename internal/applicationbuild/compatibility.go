package applicationbuild

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"kitsoki/internal/app"
)

type stateShape struct {
	Path       string
	Type       string
	Initial    string
	Terminal   bool
	Implements []string
	Intents    map[string]map[string]app.Slot
	On         map[string][]transitionShape
	OnEnter    []app.Effect
}

type transitionShape struct {
	Target  string
	When    string
	Default bool
	Effects []app.Effect
	Emit    []string
}

func componentModules(def *app.AppDef, storyRoot string) ([]ComponentModule, error) {
	contract := def.Application
	ownedRoots := append([]string{storyRoot}, def.ApplicationPackageRoots...)
	components := make([]ComponentModule, 0)
	componentIDs := make([]string, 0, len(contract.Components))
	for id := range contract.Components {
		componentIDs = append(componentIDs, id)
	}
	sort.Strings(componentIDs)
	for _, id := range componentIDs {
		component := contract.Components[id]
		if component == nil || component.Web == nil {
			continue
		}
		module, err := resolveOwnedPathInRoots(ownedRoots, component.Web.Module)
		if err != nil {
			return nil, fmt.Errorf("application build: component %q: %w", id, err)
		}
		exportName := component.Web.Export
		if exportName == "" {
			exportName = "default"
		}
		components = append(components, ComponentModule{ID: id, Module: module, Export: exportName})
	}
	return components, nil
}

func compatibilityForDefinition(def *app.AppDef, storyRoot string, components []ComponentModule, theme map[string]string) (Compatibility, error) {
	shapeDigest, err := digestValue(struct {
		App      app.AppMeta
		Root     any
		World    map[string]app.VarDef
		States   []stateShape
		Intents  map[string]map[string]app.Slot
		Handlers *app.ExportsBlock
		Events   map[string]*app.ApplicationEvent
		Sources  map[string]string
	}{
		App: def.App, Root: def.Root, World: def.World, States: applicationStateShapes(def.States),
		Intents: applicationIntentShapes(def.Intents), Handlers: def.Exports, Events: def.Events,
		Sources: referencedRuntimeSources(def, storyRoot),
	})
	if err != nil {
		return Compatibility{}, err
	}
	definitionDigest, err := digestValue(struct {
		States  map[string]*app.State
		Intents map[string]app.Intent
	}{States: def.States, Intents: def.Intents})
	if err != nil {
		return Compatibility{}, err
	}
	runtimeDigest, err := digestValue(struct {
		Shape      string
		Definition string
	}{Shape: shapeDigest, Definition: definitionDigest})
	if err != nil {
		return Compatibility{}, err
	}
	presentationSources := make(map[string]string, len(components))
	for _, component := range components {
		presentationSources[component.Module] = fileDigest(component.Module)
	}
	presentationDigest, err := digestValue(struct {
		Application *app.ApplicationContract
		Components  []ComponentModule
		Theme       map[string]string
		Sources     map[string]string
	}{Application: def.Application, Components: components, Theme: theme, Sources: presentationSources})
	if err != nil {
		return Compatibility{}, err
	}
	return Compatibility{
		Schema: CompatibilitySchema, ShapeDigest: shapeDigest,
		DefinitionDigest: definitionDigest, RuntimeDigest: runtimeDigest,
		PresentationDigest: presentationDigest, Status: "compatible",
		Message: "Presentation changes use Vite HMR; structural story and schema changes require an explicit session reload.",
	}, nil
}

func applicationStateShapes(states map[string]*app.State) []stateShape {
	var out []stateShape
	var walk func(string, map[string]*app.State)
	walk = func(prefix string, values map[string]*app.State) {
		names := make([]string, 0, len(values))
		for name := range values {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			state := values[name]
			path := name
			if prefix != "" {
				path = prefix + "." + name
			}
			if state == nil {
				out = append(out, stateShape{Path: path})
				continue
			}
			transitions := make(map[string][]transitionShape, len(state.On))
			for intent, declared := range state.On {
				items := make([]transitionShape, 0, len(declared))
				for _, transition := range declared {
					items = append(items, transitionShape{
						Target: transition.Target, When: transition.When, Default: transition.Default,
						Effects: transition.Effects, Emit: transition.Emit,
					})
				}
				transitions[intent] = items
			}
			out = append(out, stateShape{
				Path: path, Type: state.Type, Initial: state.Initial, Terminal: state.Terminal,
				Implements: append([]string(nil), state.Implements...),
				Intents:    applicationIntentShapes(state.Intents), On: transitions, OnEnter: state.OnEnter,
			})
			walk(path, state.States)
		}
	}
	walk("", states)
	return out
}

func applicationIntentShapes(intents map[string]app.Intent) map[string]map[string]app.Slot {
	out := make(map[string]map[string]app.Slot, len(intents))
	for name, intent := range intents {
		out[name] = intent.Slots
	}
	return out
}

func referencedRuntimeSources(def *app.AppDef, storyRoot string) map[string]string {
	paths := make(map[string]struct{})
	if def.Exports != nil {
		for _, handler := range def.Exports.Handlers {
			if handler == nil {
				continue
			}
			paths[handler.InputSchema] = struct{}{}
			paths[handler.OutputSchema] = struct{}{}
			if handler.Starlark != nil {
				paths[handler.Starlark.Script] = struct{}{}
			}
		}
	}
	for _, event := range def.Events {
		if event != nil {
			paths[event.InputSchema] = struct{}{}
		}
	}
	out := make(map[string]string)
	for declared := range paths {
		if declared == "" {
			continue
		}
		resolved, err := resolveOwnedPath(storyRoot, declared)
		if err != nil {
			out[declared] = "missing:" + err.Error()
			continue
		}
		out[declared] = fileDigest(resolved)
	}
	return out
}

func fileDigest(path string) string {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "missing:" + err.Error()
	}
	digest, err := digestValue(raw)
	if err != nil {
		return "invalid:" + err.Error()
	}
	return digest
}
