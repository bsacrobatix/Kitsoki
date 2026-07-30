package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	yaml "github.com/goccy/go-yaml"

	"kitsoki/internal/componentpackage"
	"kitsoki/internal/kitgit"
	"kitsoki/internal/kitlock"
)

func resolveApplicationPackages(def *AppDef, file, baseDir string, resolver ImportResolver) []error {
	if def == nil || def.Application == nil || len(def.Application.Packages) == 0 {
		return nil
	}
	lockRoot, lock, err := findApplicationPackageLock(baseDir)
	if err != nil {
		return []error{&ValidationError{File: file, Message: "application.packages: " + err.Error()}}
	}
	var errs []error
	seenPackages := map[string]struct{}{}
	for i, use := range def.Application.Packages {
		path := fmt.Sprintf("application.packages[%d]", i)
		if _, duplicate := seenPackages[use.Package]; duplicate {
			errs = append(errs, &ValidationError{File: file, Message: path + ": duplicate package " + use.Package})
			continue
		}
		seenPackages[use.Package] = struct{}{}
		entry := lock.Kits[use.Package]
		if entry == nil {
			errs = append(errs, &ValidationError{File: file, Message: fmt.Sprintf("%s: package %q is not pinned in kits.lock", path, use.Package)})
			continue
		}
		packageDir, resolveErr := resolveApplicationPackageDir(entry, lockRoot, baseDir, resolver)
		if resolveErr != nil {
			errs = append(errs, &ValidationError{File: file, Message: fmt.Sprintf("%s: %v", path, resolveErr)})
			continue
		}
		manifest, loadErr := componentpackage.LoadDir(packageDir)
		if loadErr != nil {
			errs = append(errs, &ValidationError{File: file, Message: fmt.Sprintf("%s: %v", path, loadErr)})
			continue
		}
		if manifest.Identity() != use.Package {
			errs = append(errs, &ValidationError{File: file, Message: fmt.Sprintf("%s: resolved package identity %q, want %q", path, manifest.Identity(), use.Package)})
			continue
		}
		if verifyErr := manifest.VerifyLock(lock); verifyErr != nil {
			errs = append(errs, &ValidationError{File: file, Message: fmt.Sprintf("%s: %v", path, verifyErr)})
			continue
		}
		selection, selectErr := manifest.Select(use.Select)
		if selectErr != nil {
			errs = append(errs, &ValidationError{File: file, Message: fmt.Sprintf("%s: %v", path, selectErr)})
			continue
		}
		if applyErrs := applyApplicationPackageSelection(def, selection, manifest.Identity(), packageDir, file, path); len(applyErrs) > 0 {
			errs = append(errs, applyErrs...)
			continue
		}
		if def.ImportedApplicationOwners == nil {
			def.ImportedApplicationOwners = map[string]struct{}{}
		}
		def.ImportedApplicationOwners[manifest.Identity()] = struct{}{}
		def.ApplicationPackageRoots = appendUnique(def.ApplicationPackageRoots, packageDir)
	}
	return errs
}

func findApplicationPackageLock(start string) (string, *kitlock.Lockfile, error) {
	current, err := filepath.Abs(start)
	if err != nil {
		return "", nil, err
	}
	for {
		path := kitlock.Path(current)
		if kitlock.Exists(path) {
			lock, err := kitlock.Load(path)
			return current, lock, err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", nil, fmt.Errorf("no %s found walking upward from %s", filepath.Join(kitlock.DirName, kitlock.FileName), start)
		}
		current = parent
	}
}

func resolveApplicationPackageDir(entry *kitlock.Entry, lockRoot, importerDir string, resolver ImportResolver) (string, error) {
	if entry == nil {
		return "", fmt.Errorf("package lock entry is required")
	}
	source := entry.Source
	if url, ref, ok := kitgit.ParseSource(source); ok {
		resolved, cached, err := kitgit.CachedResult(entry.Commit)
		if err != nil {
			return "", err
		}
		if !cached {
			resolved, err = kitgit.Materialize(context.Background(), kitgit.DefaultRunner, url, ref)
			if err != nil {
				return "", err
			}
		}
		if entry.Commit != "" && resolved.Commit != entry.Commit {
			return "", fmt.Errorf("package source %q resolved commit %q, want locked %q", source, resolved.Commit, entry.Commit)
		}
		if resolved.TreeHash != entry.TreeHash {
			return "", fmt.Errorf("package source %q resolved tree %q, want locked %q", source, resolved.TreeHash, entry.TreeHash)
		}
		return requireComponentPackageDir(source, resolved.Root)
	}

	var resolved string
	if strings.HasPrefix(source, "@kitsoki/") {
		if resolver == nil {
			return "", fmt.Errorf("package source %q requires an import resolver", source)
		}
		name := strings.TrimPrefix(source, "@kitsoki/")
		var err error
		resolved, err = resolver(name, importerDir, true)
		if err != nil {
			return "", err
		}
		if resolved == "" {
			resolved, err = resolver(name, importerDir, false)
			if err != nil {
				return "", err
			}
		}
		if resolved == "" {
			return "", fmt.Errorf("package source %q did not resolve", source)
		}
	} else {
		resolved = source
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(lockRoot, resolved)
		}
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("package source %q: %w", source, err)
	}
	if !info.IsDir() {
		resolved = filepath.Dir(resolved)
	}
	actualHash, err := kitgit.DirTreeHash(resolved)
	if err != nil {
		return "", fmt.Errorf("package source %q hash: %w", source, err)
	}
	if actualHash != entry.TreeHash {
		if cached, ok, cacheErr := kitgit.CachedTree(entry.TreeHash); cacheErr == nil && ok {
			return requireComponentPackageDir(source, cached)
		}
		return "", fmt.Errorf("package source %q resolved tree %q, want locked %q", source, actualHash, entry.TreeHash)
	}
	return requireComponentPackageDir(source, resolved)
}

func requireComponentPackageDir(source, resolved string) (string, error) {
	if _, err := os.Stat(filepath.Join(resolved, componentpackage.FileName)); err != nil {
		return "", fmt.Errorf("package source %q has no %s: %w", source, componentpackage.FileName, err)
	}
	return resolved, nil
}

func applyApplicationPackageSelection(def *AppDef, selection *componentpackage.Selection, owner, packageDir, file, sourcePath string) []error {
	var errs []error
	add := func(message string) {
		errs = append(errs, &ValidationError{File: file, Message: sourcePath + ": " + message})
	}
	if def.Application.Components == nil {
		def.Application.Components = map[string]*ApplicationComponent{}
	}
	for _, id := range sortedKeys(selection.Components) {
		if _, exists := def.Application.Components[id]; exists {
			add(fmt.Sprintf("component %q collides with an existing declaration", id))
			continue
		}
		source := selection.Components[id]
		component := &ApplicationComponent{
			Name: source.Name, Description: source.Description,
			SemanticRef: source.SemanticRef, SemanticAliases: append([]string(nil), source.SemanticAliases...),
			PropsSchema: source.PropsSchema, ResolvedPropsSchema: rebaseApplicationPath(source.PropsSchema, packageDir),
			Events: map[string]string{}, ResolvedEvents: map[string]string{},
			Origin: ApplicationMemberOrigin{
				Story: owner, Member: "component-package.components." + strings.TrimPrefix(id, owner+"."),
			},
		}
		for event, schema := range source.Events {
			component.Events[event] = schema
			component.ResolvedEvents[event] = rebaseApplicationPath(schema, packageDir)
		}
		if len(component.Events) == 0 {
			component.Events = nil
			component.ResolvedEvents = nil
		}
		if source.Web != nil {
			component.Web = &ApplicationWebComponent{
				Module: source.Web.Module, Export: source.Web.Export,
				ResolvedModule: rebaseApplicationPath(source.Web.Module, packageDir),
			}
		}
		if source.Fallback != nil {
			component.Fallback = &ApplicationComponentFallback{
				Element: source.Fallback.Element, ValueProp: source.Fallback.ValueProp,
			}
		}
		def.Application.Components[id] = component
	}
	if def.Application.Schemas == nil {
		def.Application.Schemas = map[string]string{}
	}
	if def.Application.ResolvedSchemas == nil {
		def.Application.ResolvedSchemas = map[string]string{}
	}
	for _, id := range sortedKeys(selection.Schemas) {
		if _, exists := def.Application.Schemas[id]; exists {
			add(fmt.Sprintf("schema %q collides with an existing declaration", id))
			continue
		}
		def.Application.Schemas[id] = selection.Schemas[id]
		def.Application.ResolvedSchemas[id] = rebaseApplicationPath(selection.Schemas[id], packageDir)
	}
	if def.Application.Tokens == nil {
		def.Application.Tokens = map[string]string{}
	}
	for _, id := range sortedKeys(selection.Tokens) {
		if _, exists := def.Application.Tokens[id]; exists {
			add(fmt.Sprintf("token %q collides with an existing declaration", id))
			continue
		}
		def.Application.Tokens[id] = rebaseApplicationPath(selection.Tokens[id], packageDir)
	}
	decodeMap(selection.RoomTemplates, &def.PhaseTemplates, "room_template", add)
	decodeMap(selection.Intents, &def.Intents, "intent", add)
	decodeMap(selection.Toolboxes, &def.Toolboxes, "toolbox", add)
	decodeMap(selection.Providers, &def.Providers, "provider", add)
	decodeMap(selection.HostInterfaces, &def.HostInterfaces, "host_interface", add)

	selectedAgents := map[string]*AgentDecl{}
	decodeMap(selection.Agents, &selectedAgents, "agent", add)
	if len(selectedAgents) > 0 {
		for _, id := range sortedKeys(selectedAgents) {
			agent := selectedAgents[id]
			if agent == nil {
				continue
			}
			agent.Toolbox = qualifySelectedPackageRef(owner, agent.Toolbox, selection.Toolboxes)
			agent.Provider = qualifySelectedPackageRef(owner, agent.Provider, selection.Providers)
		}
		// Propagate the parent def's envLookup so a package-selected agent's
		// cwd: expansion sees the same per-load override the importer's own
		// agents: block does (see AppDef.envLookup).
		temp := &AppDef{Agents: selectedAgents, Toolboxes: def.Toolboxes, envLookup: def.envLookup}
		if agentErrs := resolveAgentDecls(temp, file, packageDir); len(agentErrs) > 0 {
			for _, err := range agentErrs {
				add(err.Error())
			}
		} else {
			if def.Agents == nil {
				def.Agents = map[string]*AgentDecl{}
			}
			for id, agent := range temp.Agents {
				if _, exists := def.Agents[id]; exists {
					add(fmt.Sprintf("agent %q collides with an existing declaration", id))
					continue
				}
				def.Agents[id] = agent
			}
		}
	}
	for _, id := range sortedKeys(selection.RoomTemplates) {
		template := def.PhaseTemplates[id]
		if template == nil {
			continue
		}
		for _, state := range template.States {
			qualifyPackageState(state, owner, selection)
		}
	}
	return errs
}

func qualifySelectedPackageRef[V any](owner, ref string, selected map[string]V) string {
	if ref == "" || strings.Contains(ref, "{{") {
		return ref
	}
	qualified := owner + "." + ref
	if _, ok := selected[qualified]; ok {
		return qualified
	}
	return ref
}

func qualifyPackageState(state *State, owner string, selection *componentpackage.Selection) {
	if state == nil || selection == nil {
		return
	}
	if len(state.On) > 0 {
		qualified := make(map[string][]Transition, len(state.On))
		for name, transitions := range state.On {
			qualified[qualifySelectedPackageRef(owner, name, selection.Intents)] = transitions
		}
		state.On = qualified
	}
	for i, name := range state.Menu {
		state.Menu[i] = qualifySelectedPackageRef(owner, name, selection.Intents)
	}
	state.DefaultIntent = qualifySelectedPackageRef(owner, state.DefaultIntent, selection.Intents)
	for i := range state.OnEnter {
		qualifyPackageEffect(&state.OnEnter[i], owner, selection)
	}
	for name, transitions := range state.On {
		for i := range transitions {
			for j := range transitions[i].Effects {
				qualifyPackageEffect(&transitions[i].Effects[j], owner, selection)
			}
		}
		state.On[name] = transitions
	}
	for _, child := range state.States {
		qualifyPackageState(child, owner, selection)
	}
}

func qualifyPackageEffect(effect *Effect, owner string, selection *componentpackage.Selection) {
	if effect == nil {
		return
	}
	if value, ok := effect.With["agent"].(string); ok {
		effect.With["agent"] = qualifySelectedPackageRef(owner, value, selection.Agents)
	}
	if value, ok := effect.With["provider"].(string); ok {
		effect.With["provider"] = qualifySelectedPackageRef(owner, value, selection.Providers)
	}
	effect.EmitIntent = qualifySelectedPackageRef(owner, effect.EmitIntent, selection.Intents)
	if strings.HasPrefix(effect.Invoke, "iface.") {
		rest := strings.TrimPrefix(effect.Invoke, "iface.")
		if dot := strings.IndexByte(rest, '.'); dot > 0 {
			name, operation := rest[:dot], rest[dot+1:]
			qualified := qualifySelectedPackageRef(owner, name, selection.HostInterfaces)
			effect.Invoke = "iface." + qualified + "." + operation
		}
	}
	for i := range effect.OnComplete {
		qualifyPackageEffect(&effect.OnComplete[i], owner, selection)
	}
	for i := range effect.Effects {
		qualifyPackageEffect(&effect.Effects[i], owner, selection)
	}
}

func decodeMap[T any](source map[string]any, target *map[string]T, kind string, add func(string)) {
	if len(source) == 0 {
		return
	}
	if *target == nil {
		*target = map[string]T{}
	}
	for _, id := range sortedKeys(source) {
		if _, exists := (*target)[id]; exists {
			add(fmt.Sprintf("%s %q collides with an existing declaration", kind, id))
			continue
		}
		data, err := yaml.Marshal(source[id])
		if err != nil {
			add(fmt.Sprintf("%s %q encode: %v", kind, id, err))
			continue
		}
		var value T
		destination := any(&value)
		valueRef := reflect.ValueOf(&value).Elem()
		if valueRef.Kind() == reflect.Pointer {
			valueRef.Set(reflect.New(valueRef.Type().Elem()))
			destination = valueRef.Interface()
		}
		if err := yaml.Unmarshal(data, destination); err != nil {
			add(fmt.Sprintf("%s %q decode: %v", kind, id, err))
			continue
		}
		(*target)[id] = value
	}
}
