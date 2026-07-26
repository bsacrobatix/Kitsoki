// Package app — story-imports overrides.
//
// See docs/stories/imports.md for the import/override authoring model.
//
// Overrides patch a child app's states / intents / prompts at import time.
// The import declares:
//
//	imports:
//	  bf:
//	    overrides:
//	      states:   { applying: {...replacement state...} }
//	      intents:  { trigger_deploy: {...replacement intent...} }
//	      prompts:  { shell_repair.md: ./prompts/custom_shell_repair.md }
//
// Semantics: whole-element replacement, not deep-merge. Validation fails
// when an override targets a name the child does not actually declare —
// this catches typos at load time rather than letting them silently no-op.
//
// Override is applied BEFORE the child is namespace-flattened so the
// override.states / override.intents keys reference child-local names
// (not <alias>/<name>). The child rewriter then walks the overridden
// shape during the normal fold pass.
package app

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// applyOverrides walks imp.Overrides and patches child in place. Errors
// are aggregated.
//
//	parentBaseDir  — directory of the parent app.yaml (for prompt path
//	                 resolution; override.prompts paths are author-relative
//	                 to the parent).
//	childBaseDir   — directory of the child app.yaml (where the prompt
//	                 file would live unaltered; we replace its contents on
//	                 disk-relative reads at load time by remapping the
//	                 path the child loader reads from).
func applyOverrides(child *AppDef, ov *ImportOverrides, file, alias, parentStory, parentBaseDir, childBaseDir string) []error {
	if child == nil || ov == nil {
		return nil
	}
	var errs []error
	addErr := func(msg string) {
		errs = append(errs, &ValidationError{File: file, Message: fmt.Sprintf("imports.%s: overrides: %s", alias, msg)})
	}

	// State overrides — replace by child-local name. The match must exist
	// somewhere in the child's state tree (top-level or nested); we only
	// replace top-level matches in v1 because nested replacement gets
	// surprising (the override contract says "replaces the child's state of
	// that name" without disambiguating nesting; we pick the safe rule).
	for _, name := range sortedKeys(ov.States) {
		newState := ov.States[name]
		if _, ok := child.States[name]; !ok {
			addErr(fmt.Sprintf("states.%s: child does not declare a top-level state named %q", name, name))
			continue
		}
		child.States[name] = newState
	}

	// Intent overrides — replace named child intents. The intent must
	// already exist in the child's library; if not, this is a typo.
	for _, name := range sortedKeys(ov.Intents) {
		if _, ok := child.Intents[name]; !ok {
			addErr(fmt.Sprintf("intents.%s: child does not declare an intent named %q", name, name))
			continue
		}
		if child.Intents == nil {
			child.Intents = make(map[string]Intent)
		}
		child.Intents[name] = ov.Intents[name]
	}

	// Prompt overrides — copy parent's file into the location the child
	// reads from. The simplest correct implementation: read the override
	// file from the parent's dir, write it to the child's expected path
	// in a temp area, then point any host.agent.* invocation that reads
	// that path at the override.
	//
	// In practice prompt-bearing invocations carry the path as a
	// `with: { prompt: "<relative>" }` arg. We rewrite those arg values
	// from <relative> to the override's resolved path when a match exists.
	if len(ov.Prompts) > 0 {
		// Validate every override path exists; resolve to absolute.
		resolved := make(map[string]string, len(ov.Prompts))
		for _, rel := range sortedKeys(ov.Prompts) {
			overridePath := ov.Prompts[rel]
			if !filepath.IsAbs(overridePath) {
				overridePath = filepath.Join(parentBaseDir, overridePath)
			}
			if _, statErr := os.Stat(overridePath); statErr != nil {
				addErr(fmt.Sprintf("prompts.%s: %v", rel, statErr))
				continue
			}
			// child path resolves relative to childBaseDir for matching.
			childRel := rel
			if !filepath.IsAbs(childRel) {
				childRel = filepath.Join(childBaseDir, rel)
			}
			resolved[childRel] = overridePath
			// Also key by the bare relative form so authors can write
			// either form in `with: { prompt: ... }`.
			resolved[rel] = overridePath
		}
		if len(resolved) > 0 {
			applyPromptOverridesToStates(child.States, resolved)
		}
	}
	if ov.Application != nil {
		applyApplicationOverrides(child, ov.Application, parentStory, parentBaseDir, addErr)
	}

	return errs
}

func applyApplicationOverrides(child *AppDef, overrides *ApplicationOverrides, parentStory, parentBaseDir string, addErr func(string)) {
	if child == nil || overrides == nil {
		return
	}
	if child.Application == nil && (len(overrides.Pages) > 0 || len(overrides.Components) > 0 || len(overrides.Actions) > 0) {
		addErr("application: child does not declare application:")
		return
	}
	for _, id := range sortedKeys(overrides.Pages) {
		replacement := overrides.Pages[id]
		base, exists := child.Application.Pages[id]
		if !exists || base == nil {
			addErr(fmt.Sprintf("application.pages.%s: child does not declare page %q", id, id))
			continue
		}
		if err := compatiblePageOverride(base, replacement); err != nil {
			addErr(fmt.Sprintf("application.pages.%s: %v", id, err))
			continue
		}
		stampOverridePageOrigin(replacement, parentStory, "overrides.application.pages."+id)
		child.Application.Pages[id] = replacement
	}
	for _, id := range sortedKeys(overrides.Components) {
		replacement := overrides.Components[id]
		base, exists := child.Application.Components[id]
		if !exists || base == nil {
			addErr(fmt.Sprintf("application.components.%s: child does not declare component %q", id, id))
			continue
		}
		if err := compatibleComponentOverride(base, replacement); err != nil {
			addErr(fmt.Sprintf("application.components.%s: %v", id, err))
			continue
		}
		clone := *replacement
		if replacement.Web != nil {
			web := *replacement.Web
			web.ResolvedModule = rebaseApplicationPath(web.Module, parentBaseDir)
			clone.Web = &web
		}
		clone.ResolvedPropsSchema = rebaseApplicationPath(clone.PropsSchema, parentBaseDir)
		clone.ResolvedEvents = make(map[string]string, len(clone.Events))
		for event, schema := range clone.Events {
			clone.ResolvedEvents[event] = rebaseApplicationPath(schema, parentBaseDir)
		}
		clone.Origin = ApplicationMemberOrigin{
			Story: parentStory, Member: "overrides.application.components." + id,
		}
		child.Application.Components[id] = &clone
	}
	for _, id := range sortedKeys(overrides.Actions) {
		replacement := overrides.Actions[id]
		base, exists := child.Application.Actions[id]
		if !exists || base == nil {
			addErr(fmt.Sprintf("application.actions.%s: child does not declare action %q", id, id))
			continue
		}
		if err := compatibleActionOverride(base, replacement); err != nil {
			addErr(fmt.Sprintf("application.actions.%s: %v", id, err))
			continue
		}
		clone := *replacement
		clone.ResolvedInputSchema = rebaseApplicationPath(clone.InputSchema, parentBaseDir)
		clone.Origin = ApplicationMemberOrigin{
			Story: parentStory, Member: "overrides.application.actions." + id,
		}
		child.Application.Actions[id] = &clone
	}
	for _, id := range sortedKeys(overrides.Handlers) {
		replacement := overrides.Handlers[id]
		if child.Exports == nil {
			addErr(fmt.Sprintf("application.handlers.%s: child does not declare exported handlers", id))
			continue
		}
		base, exists := child.Exports.Handlers[id]
		if !exists || base == nil {
			addErr(fmt.Sprintf("application.handlers.%s: child does not declare handler %q", id, id))
			continue
		}
		if err := compatibleHandlerOverride(base, replacement); err != nil {
			addErr(fmt.Sprintf("application.handlers.%s: %v", id, err))
			continue
		}
		clone := *replacement
		clone.ResolvedInputSchema = rebaseApplicationPath(clone.InputSchema, parentBaseDir)
		clone.ResolvedOutputSchema = rebaseApplicationPath(clone.OutputSchema, parentBaseDir)
		if replacement.Starlark != nil {
			starlark := *replacement.Starlark
			starlark.Script = rebaseApplicationPath(starlark.Script, parentBaseDir)
			clone.Starlark = &starlark
		}
		clone.Origin = ApplicationMemberOrigin{
			Story: parentStory, Member: "overrides.application.handlers." + id,
		}
		child.Exports.Handlers[id] = &clone
	}
}

func stampOverridePageOrigin(page *ApplicationPage, story, member string) {
	if page == nil {
		return
	}
	page.Origin = ApplicationMemberOrigin{Story: story, Member: member}
	for regionID, region := range page.Regions {
		if region == nil {
			continue
		}
		regionMember := member + ".regions." + regionID
		region.Origin = ApplicationMemberOrigin{Story: story, Member: regionMember}
		for index := range region.Items {
			if card := region.Items[index].Card; card != nil {
				card.Origin = ApplicationMemberOrigin{
					Story: story, Member: fmt.Sprintf("%s.items[%d].card", regionMember, index),
				}
			}
		}
	}
}

func compatiblePageOverride(base, replacement *ApplicationPage) error {
	if replacement == nil {
		return fmt.Errorf("replacement is empty")
	}
	if !semanticReplacementCompatible(base.SemanticRef, replacement.SemanticRef, replacement.SemanticAliases) {
		return fmt.Errorf("semantic_ref %q must remain %q or preserve it in semantic_aliases", replacement.SemanticRef, base.SemanticRef)
	}
	for regionID, baseRegion := range base.Regions {
		replacementRegion := replacement.Regions[regionID]
		if replacementRegion == nil {
			return fmt.Errorf("replacement removes required region %q", regionID)
		}
		if baseRegion != nil && replacementRegion.SemanticRef != baseRegion.SemanticRef {
			return fmt.Errorf("region %q semantic_ref changes from %q to %q", regionID, baseRegion.SemanticRef, replacementRegion.SemanticRef)
		}
	}
	return nil
}

func compatibleComponentOverride(base, replacement *ApplicationComponent) error {
	if replacement == nil {
		return fmt.Errorf("replacement is empty")
	}
	if !semanticReplacementCompatible(base.SemanticRef, replacement.SemanticRef, replacement.SemanticAliases) {
		return fmt.Errorf("semantic_ref %q must remain %q or preserve it in semantic_aliases", replacement.SemanticRef, base.SemanticRef)
	}
	if base.PropsSchema != replacement.PropsSchema {
		return fmt.Errorf("props_schema %q is incompatible with %q", replacement.PropsSchema, base.PropsSchema)
	}
	if !reflect.DeepEqual(base.Events, replacement.Events) {
		return fmt.Errorf("component events are incompatible")
	}
	baseFallback, replacementFallback := "", ""
	baseFallbackValue, replacementFallbackValue := "", ""
	if base.Fallback != nil {
		baseFallback = base.Fallback.Element
		baseFallbackValue = base.Fallback.ValueProp
	}
	if replacement.Fallback != nil {
		replacementFallback = replacement.Fallback.Element
		replacementFallbackValue = replacement.Fallback.ValueProp
	}
	if baseFallback != replacementFallback {
		return fmt.Errorf("fallback element %q is incompatible with %q", replacementFallback, baseFallback)
	}
	if baseFallbackValue != replacementFallbackValue {
		return fmt.Errorf("fallback value_prop %q is incompatible with %q", replacementFallbackValue, baseFallbackValue)
	}
	return nil
}

func compatibleActionOverride(base, replacement *ApplicationAction) error {
	if replacement == nil {
		return fmt.Errorf("replacement is empty")
	}
	if !semanticReplacementCompatible(base.SemanticRef, replacement.SemanticRef, replacement.SemanticAliases) {
		return fmt.Errorf("semantic_ref %q must remain %q or preserve it in semantic_aliases", replacement.SemanticRef, base.SemanticRef)
	}
	if base.InputSchema != replacement.InputSchema {
		return fmt.Errorf("input_schema %q is incompatible with %q", replacement.InputSchema, base.InputSchema)
	}
	if base.Handler != replacement.Handler || base.Intent != replacement.Intent ||
		base.State != replacement.State || base.RoomInterface != replacement.RoomInterface {
		return fmt.Errorf("replacement changes the action target contract")
	}
	if base.RoutingMode != replacement.RoutingMode {
		return fmt.Errorf("routing_mode %q is incompatible with %q", replacement.RoutingMode, base.RoutingMode)
	}
	return nil
}

func compatibleHandlerOverride(base, replacement *ApplicationHandler) error {
	if replacement == nil {
		return fmt.Errorf("replacement is empty")
	}
	if !semanticReplacementCompatible(base.SemanticRef, replacement.SemanticRef, replacement.SemanticAliases) {
		return fmt.Errorf("semantic_ref %q must remain %q or preserve it in semantic_aliases", replacement.SemanticRef, base.SemanticRef)
	}
	if base.InputSchema != replacement.InputSchema || base.OutputSchema != replacement.OutputSchema {
		return fmt.Errorf("input/output schemas are incompatible")
	}
	if base.Effect != replacement.Effect {
		return fmt.Errorf("effect %q is incompatible with %q", replacement.Effect, base.Effect)
	}
	if base.Session != replacement.Session || base.RoutingMode != replacement.RoutingMode {
		return fmt.Errorf("session/routing policy is incompatible")
	}
	if !sameStringSet(base.Outcomes, replacement.Outcomes) {
		return fmt.Errorf("outcomes %v are incompatible with %v", replacement.Outcomes, base.Outcomes)
	}
	if !stringSetContains(replacement.Expose, base.Expose) {
		return fmt.Errorf("expose %v removes a base adapter from %v", replacement.Expose, base.Expose)
	}
	if base.Idempotency != nil {
		if replacement.Idempotency == nil ||
			base.Idempotency.Scope != replacement.Idempotency.Scope ||
			base.Idempotency.Key != replacement.Idempotency.Key {
			return fmt.Errorf("replacement weakens the base idempotency policy")
		}
	}
	if base.Retry != nil && replacement.Retry != nil &&
		replacement.Retry.MaxAttempts > base.Retry.MaxAttempts {
		return fmt.Errorf("retry max_attempts %d weakens base limit %d", replacement.Retry.MaxAttempts, base.Retry.MaxAttempts)
	}
	if base.Compensation != "" && replacement.Compensation == "" {
		return fmt.Errorf("replacement removes base compensation %q", base.Compensation)
	}
	return nil
}

func semanticReplacementCompatible(base, replacement string, aliases []string) bool {
	if base == replacement {
		return true
	}
	for _, alias := range aliases {
		if alias == base {
			return true
		}
	}
	return false
}

func sameStringSet(a, b []string) bool {
	return len(a) == len(b) && stringSetContains(a, b)
}

func stringSetContains(have, required []string) bool {
	set := make(map[string]struct{}, len(have))
	for _, value := range have {
		set[value] = struct{}{}
	}
	for _, value := range required {
		if _, ok := set[value]; !ok {
			return false
		}
	}
	return true
}

// rebaseEffectPaths walks an imported child's state tree and rewrites
// every relative `prompt:` / `schema:` / `script:` arg in effect `with:` blocks to
// an absolute path rooted at the child's directory. Without this, the
// runtime joins the relative path against $KITSOKI_APP_DIR (the parent
// app's directory) and fails to find files that live in the child
// story's prompts/, schemas/, or scripts/ tree.
//
// Idempotent: paths already absolute or containing template syntax
// (`{{`) are left alone — the latter because we can't resolve them
// statically and the runtime renders them at dispatch time.
func rebaseEffectPaths(states map[string]*State, childDir string) {
	if childDir == "" {
		return
	}
	// Absolutize childDir so rebased paths become absolute. This is what makes
	// the rebase idempotent across TRANSITIVE imports: when story A imports B
	// which imports C, C's prompt paths are first rebased to C's dir, then B
	// (with C folded in) is rebased again at A's level. If the first rebase
	// left a RELATIVE path (which it does when the app was loaded via a relative
	// path, e.g. `stories/pets-dev`), the second pass re-prefixes it with B's
	// dir — producing `stories/A/stories/C/prompts/...`. Making the first rebase
	// absolute means the second pass's filepath.IsAbs guard (in rebaseWithMap)
	// skips the already-rebased path, so C's prompts resolve to C's real dir.
	if !filepath.IsAbs(childDir) {
		if abs, err := filepath.Abs(childDir); err == nil {
			childDir = abs
		}
	}
	for _, s := range states {
		if s == nil {
			continue
		}
		rebaseWorkbenchPaths(s.Workbench, childDir)
		rebaseEffectPathsInEffects(s.OnEnter, childDir)
		for _, list := range s.On {
			for i := range list {
				rebaseEffectPathsInEffects(list[i].Effects, childDir)
			}
		}
		if len(s.States) > 0 {
			rebaseEffectPaths(s.States, childDir)
		}
	}
}

func rebaseWorkbenchPaths(decl *WorkbenchDecl, childDir string) {
	if decl == nil {
		return
	}
	decl.Prompt = rebasePathValue(decl.Prompt, childDir)
	decl.AcceptanceSchema = rebasePathValue(decl.AcceptanceSchema, childDir)
}

func rebaseEffectPathsInEffects(effs []Effect, childDir string) {
	for i := range effs {
		rebaseWithMap(effs[i].With, childDir)
		rebaseEffectPathsInEffects(effs[i].Effects, childDir)
		for j := range effs[i].OnComplete {
			rebaseWithMap(effs[i].OnComplete[j].With, childDir)
			rebaseEffectPathsInEffects(effs[i].OnComplete[j].Effects, childDir)
		}
	}
}

func rebaseWithMap(with map[string]any, childDir string) {
	for _, key := range []string{"prompt", "prompt_path", "schema", "script"} {
		raw, ok := with[key].(string)
		if !ok {
			continue
		}
		with[key] = rebasePathValue(raw, childDir)
	}
	// host.agent.task nests prompt/prompt_path under with.context and the
	// acceptance schema under with.acceptance.schema. Both must rebase to the
	// defining story's dir, else the runtime joins them against the PARENT
	// app dir ($KITSOKI_APP_DIR) and the file isn't found.
	if ctx, ok := with["context"].(map[string]any); ok {
		rebaseWithMap(ctx, childDir)
	}
	if acc, ok := with["acceptance"].(map[string]any); ok {
		rebaseWithMap(acc, childDir)
	}
}

func rebasePathValue(raw, childDir string) string {
	if raw == "" {
		return raw
	}
	if filepath.IsAbs(raw) {
		return raw
	}
	if containsTemplate(raw) {
		return raw
	}
	return filepath.Join(childDir, raw)
}

// containsTemplate reports whether s carries a pongo2/expr template
// delimiter — `{{` or `{%`. Used to guard static path rewrites from
// touching dynamic expressions the runtime renders at dispatch time.
func containsTemplate(s string) bool {
	return strings.Contains(s, "{{") || strings.Contains(s, "{%")
}

// containsCapabilityTemplate reports whether a nested CodeAct capability
// declaration has a dispatch-time expression. Static declarations are checked
// at load time; dynamic leaves are re-rendered to typed values and parsed by
// AgentCodeactHandler before any model call starts.
func containsCapabilityTemplate(v any) bool {
	switch value := v.(type) {
	case string:
		return containsTemplate(value)
	case map[string]any:
		for _, child := range value {
			if containsCapabilityTemplate(child) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if containsCapabilityTemplate(child) {
				return true
			}
		}
	}
	return false
}

// applyPromptOverridesToStates walks every Effect.With["prompt"] in the
// child's state tree and rewrites the value when a key matches `resolved`.
// `resolved` keys are both the relative path the child author wrote and
// the abs path the loader would resolve to; the lookup tries both.
func applyPromptOverridesToStates(states map[string]*State, resolved map[string]string) {
	for _, s := range states {
		if s == nil {
			continue
		}
		applyPromptOverridesToEffects(s.OnEnter, resolved)
		for _, list := range s.On {
			for i := range list {
				applyPromptOverridesToEffects(list[i].Effects, resolved)
			}
		}
		if len(s.States) > 0 {
			applyPromptOverridesToStates(s.States, resolved)
		}
	}
}

func applyPromptOverridesToEffects(effs []Effect, resolved map[string]string) {
	for i := range effs {
		if len(effs[i].With) > 0 {
			if raw, ok := effs[i].With["prompt"]; ok {
				if s, isStr := raw.(string); isStr {
					if newPath, hit := resolved[s]; hit {
						effs[i].With["prompt"] = newPath
					}
				}
			}
		}
		if len(effs[i].OnComplete) > 0 {
			applyPromptOverridesToEffects(effs[i].OnComplete, resolved)
		}
		if len(effs[i].Effects) > 0 {
			applyPromptOverridesToEffects(effs[i].Effects, resolved)
		}
	}
}
