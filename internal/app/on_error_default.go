// Package app — story-level `on_error_default:` resolution and the
// unhandled-invoke lint.
//
// Both pieces are pure load-time / static-analysis mechanisms: neither
// touches internal/orchestrator or internal/machine. See AppDef.OnErrorDefault
// for the field's semantics and import-boundary rule.
package app

import (
	"fmt"
	"strings"
)

// resolveOnErrorDefaults applies def.OnErrorDefault to every Invoke effect
// reachable from def's own on_enter / transition-effect chains whose
// on_error: is still empty, and validates that a non-empty OnErrorDefault
// resolves to a declared state in def's own (at-this-point) state tree.
//
// Call sites and ordering (both matter for the import-boundary rule):
//
//   - loadImportedChild calls this on the child's own AppDef, right after
//     expandPhases and BEFORE the child is folded into its importer. Any
//     grandchildren the child itself imports have already run this same
//     pass (recursively, bottom-up) and already been folded into the
//     child's state tree by that point, so this call fills gaps in the
//     child's OWN invoke sites only.
//   - runLoadPipeline calls this on the fully-merged root AppDef, after
//     every state-synthesizing pass (imports, phases, workbenches, off-ramp
//     captures, builtin rooms, standalone exits) so it sees every root-owned
//     invoke site — but the walk stops descending at any State with
//     ImportAlias != "" (the marker resolveImports stamps on a folded
//     child's compound wrapper). That subtree was already resolved by the
//     call above, using the CHILD's own on_error_default (or none); the
//     root's default must not reach into it, and — because that call
//     already ran and returned before fold — the child's default cannot
//     leak out either. This mirrors how AppDef.Routing is documented as
//     "not merged across imports" and how expandWorkbenches uses the same
//     ImportAlias marker to track the accumulated world-key prefix instead
//     of stopping at it (workbench: is authored per-state so no boundary
//     concern exists there; on_error_default is authored per-AppDef so the
//     boundary is exactly the fold seam).
//
// Because both call sites run BEFORE foldChild's rewriteChildStateTransitions
// pass, a newly-defaulted OnError value (still in the child's bare pre-fold
// namespace) is rewritten to the alias-qualified form the same way a
// hand-authored on_error: is — no special-casing needed there.
func resolveOnErrorDefaults(def *AppDef, file string) []error {
	if def == nil {
		return nil
	}
	var errs []error

	if def.OnErrorDefault != "" && !strings.Contains(def.OnErrorDefault, "{{") {
		allPaths := make(map[string]struct{})
		collectStatePaths("", def.States, allPaths)
		resolved := resolveTarget("", def.OnErrorDefault)
		if _, ok := allPaths[resolved]; !ok {
			errs = append(errs, &ValidationError{
				File:    file,
				Message: fmt.Sprintf("on_error_default %q (resolved: %q) does not exist", def.OnErrorDefault, resolved),
			})
		}
	}

	if def.OnErrorDefault == "" {
		return errs
	}

	applyDefault := func(eff *Effect) {
		// AckError is an explicit per-call acknowledgement ("log it and
		// keep going, on purpose") — it outranks a story-level default.
		// Filling OnError here would silently convert a deliberate,
		// documented degrade into a hard redirect, which is exactly the
		// regression that blocked defaulting the builtin error room on
		// (see .context/troubleshooting-agent-and-error-integrity.md).
		if eff.Invoke != "" && eff.OnError == "" && eff.AckError == "" {
			eff.OnError = def.OnErrorDefault
		}
		// on_complete: entries can themselves invoke (async job → chained
		// follow-up call); apply the same default to those.
		for i := range eff.OnComplete {
			c := &eff.OnComplete[i]
			if c.Invoke != "" && c.OnError == "" && c.AckError == "" {
				c.OnError = def.OnErrorDefault
			}
		}
	}

	var walk func(states map[string]*State)
	walk = func(states map[string]*State) {
		for _, s := range states {
			if s == nil {
				continue
			}
			// Import boundary: this subtree is a folded-in child, already
			// resolved against ITS OWN on_error_default (or none) before
			// fold. Don't apply the importer's default here.
			if s.ImportAlias != "" {
				continue
			}
			for i := range s.OnEnter {
				applyDefault(&s.OnEnter[i])
			}
			for _, arcs := range s.On {
				for ai := range arcs {
					for ei := range arcs[ai].Effects {
						applyDefault(&arcs[ai].Effects[ei])
					}
				}
			}
			walk(s.States)
		}
	}
	walk(def.States)

	// proposals:.<kind>.execute is a separate dispatch mechanism outside
	// the state tree (see docs/embedded/app-schema.md ProposalExecute).
	// def.Proposals holds only this AppDef's own declared kinds — imports.go
	// does not fold a child's Proposals into the importer, so there is no
	// import-boundary concern here the way there is for the state tree walk
	// above.
	for _, pk := range def.Proposals {
		if pk == nil || pk.Execute == nil {
			continue
		}
		ex := pk.Execute
		if ex.Invoke != "" && ex.OnError == "" {
			ex.OnError = def.OnErrorDefault
		}
		for i := range ex.OnComplete {
			c := &ex.OnComplete[i]
			if c.Invoke != "" && c.OnError == "" {
				c.OnError = def.OnErrorDefault
			}
		}
	}
	return errs
}

// stampOriginFile records file as State.OriginFile on every state directly
// owned by def — i.e. authored in file itself, not folded in from an
// already-processed import. It stops descending at any state whose
// OriginFile is already set (the same s.ImportAlias != "" boundary
// resolveOnErrorDefaults' walk stops at, expressed here as "already
// stamped" since a folded-in child's states were stamped with the CHILD's
// own file by the recursive loadImportedChild call that ran before fold).
//
// Call sites mirror resolveOnErrorDefaults' exactly: loadImportedChild
// stamps a child's own tree with the child's file right before it is
// folded into its importer; the root load pipeline (both the file-backed
// Load path and the in-memory LoadBytes path) stamps the root's own tree
// with the root's file after every state-synthesizing pass. Idempotent —
// safe to no-op on a state whose OriginFile is already set.
func stampOriginFile(def *AppDef, file string) {
	if def == nil {
		return
	}
	var walk func(states map[string]*State)
	walk = func(states map[string]*State) {
		for _, s := range states {
			if s == nil || s.OriginFile != "" {
				continue
			}
			s.OriginFile = file
			walk(s.States)
		}
	}
	walk(def.States)
}

// UnhandledInvoke is one reportable finding from UnhandledInvokes: an
// invoke: effect with no effective on_error: (i.e. after on_error_default
// resolution has already run — a normal load leaves every gap it can fill
// already filled, so anything UnhandledInvokes finds truly has nowhere to
// route on failure).
type UnhandledInvoke struct {
	// StatePath is the dotted path of the state that owns the effect chain
	// (post-import-fold, so a folded child's states carry their alias
	// prefix — e.g. "troubleshoot.diagnose"). This is the display path:
	// it varies by which importing root pulled the fragment in, even when
	// the underlying gap is the same one authored a single time.
	StatePath string
	// SourceFile is the absolute path of the app.yaml that actually
	// authored the effect (State.OriginFile of the owning state — see
	// stampOriginFile), independent of how many importing roots go on to
	// fold it in under their own alias. A shared fragment imported by
	// several stories reports the SAME SourceFile every time.
	SourceFile string
	// SourceStatePath is the state path relative to SourceFile's own
	// tree — i.e. with any importer alias prefix stripped back to the
	// fragment's own authoring namespace. Combined with SourceFile and
	// Location it is stable across every importing root. Empty for
	// proposal-kind findings (ProposalKind + Location identify those).
	SourceStatePath string
	// Location is a human-readable pointer to the effect within the state
	// (e.g. `on_enter[0]`, `on.retry.arc[0].effect[1]`), matching the
	// location strings validateAgentVerbCrossChecks already uses.
	Location string
	// Invoke is the host handler name the unhandled effect calls.
	Invoke string
	// ProposalKind is non-empty when this finding is a
	// `proposals:.<kind>.execute` site rather than a state-tree effect;
	// StatePath is empty in that case (proposals live outside the state
	// tree) and Location distinguishes the execute block itself from one
	// of its on_complete: entries. def.Proposals only ever holds a root's
	// OWN declared kinds (never folded in from an import — see
	// UnhandledInvokes' doc comment), so a proposal finding's SourceFile
	// is always the root's own file; there is no cross-root duplication
	// to de-dupe for this branch, but SourceFile is still populated for a
	// uniform SourceKey().
	ProposalKind string
}

func (u UnhandledInvoke) String() string {
	if u.ProposalKind != "" {
		return fmt.Sprintf("proposal %q %s: invoke %s has no on_error:", u.ProposalKind, u.Location, u.Invoke)
	}
	return fmt.Sprintf("%s %s: invoke %s has no on_error:", u.StatePath, u.Location, u.Invoke)
}

// SourceKey returns the stable, importer-independent identity used to
// de-duplicate findings across multiple importing roots: two findings that
// share a SourceKey are the SAME underlying unhandled invoke:, discovered
// once per story that happens to import it. See DedupeUnhandledInvokes.
func (u UnhandledInvoke) SourceKey() string {
	if u.ProposalKind != "" {
		return fmt.Sprintf("proposal|%s|%s|%s", u.SourceFile, u.ProposalKind, u.Location)
	}
	return fmt.Sprintf("state|%s|%s|%s", u.SourceFile, u.SourceStatePath, u.Location)
}

// DedupeUnhandledInvokes collapses findings that share a SourceKey — the
// same underlying invoke: authored once but discovered once per importing
// root — into a single representative (the first one encountered, so
// ordering follows the input slice). Use this to report the real
// migration-size number across a batch of roots instead of summing each
// root's raw count, which over-counts every shared fragment once per
// importer.
func DedupeUnhandledInvokes(findings []UnhandledInvoke) []UnhandledInvoke {
	seen := make(map[string]struct{}, len(findings))
	out := make([]UnhandledInvoke, 0, len(findings))
	for _, f := range findings {
		key := f.SourceKey()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, f)
	}
	return out
}

// UnhandledInvokes walks every on_enter / transition-effect chain reachable
// from def.States (including folded-in imported children — this is a
// whole-tree report, unlike resolveOnErrorDefaults' per-AppDef defaulting
// pass), plus every `proposals:.<kind>.execute` block declared on def, and
// returns one UnhandledInvoke per Invoke effect (or proposal execute) whose
// OnError is still empty. file is the path UnhandledInvokes' caller loaded
// def from (the root app.yaml); it is used as the SourceFile fallback for
// any state that reached here without an OriginFile stamp (proposal-kind
// findings, and defensively for any synthesized state that predates
// stampOriginFile) and as SourceFile for every proposal-kind finding.
//
// This is a REPORT, not a load error: as of this writing the story corpus
// has ~713 invoke: sites against ~597 on_error: occurrences (both counts
// inflated the same way raw per-root UnhandledInvokes totals are — see
// DedupeUnhandledInvokes), so treating this as a hard failure would break
// the repo outright. Callers decide what to do with the result —
// `kitsoki validate --report-unhandled-errors` prints it; nothing wires it
// into a failing gate.
//
// Scope note: this walks Effect chains (on_enter:, transition effects:, and
// their on_complete:/nested effects: children) — the same surface
// resolveOnErrorDefaults defaults into — plus each declared proposal kind's
// execute.invoke and execute.on_complete: chain. def.Proposals holds only
// this AppDef's own declared kinds (imports.go does not fold a child's
// Proposals into the importer), so there is no import-boundary handling
// needed for the proposal branch.
func UnhandledInvokes(def *AppDef, file string) []UnhandledInvoke {
	if def == nil {
		return nil
	}
	var out []UnhandledInvoke
	var walkEffects func(statePath, sourceStatePath, sourceFile, prefix string, effs []Effect)
	walkEffects = func(statePath, sourceStatePath, sourceFile, prefix string, effs []Effect) {
		for i, eff := range effs {
			loc := fmt.Sprintf("%s[%d]", prefix, i)
			// AckError is an explicit, machine-readable acknowledgement
			// that this invoke's failure is handled (record + continue) —
			// it is not an oversight the lint should flag.
			if eff.Invoke != "" && eff.OnError == "" && eff.AckError == "" {
				out = append(out, UnhandledInvoke{
					StatePath:       statePath,
					SourceFile:      sourceFile,
					SourceStatePath: sourceStatePath,
					Location:        loc,
					Invoke:          eff.Invoke,
				})
			}
			if len(eff.OnComplete) > 0 {
				walkEffects(statePath, sourceStatePath, sourceFile, loc+".on_complete", eff.OnComplete)
			}
			if len(eff.Effects) > 0 {
				walkEffects(statePath, sourceStatePath, sourceFile, loc+".effects", eff.Effects)
			}
		}
	}
	// walkStates tracks two parallel paths per state: prefix (the display
	// path, alias-prefixed post-fold, unchanged from before) and
	// originPrefix/originFile (the fragment's own authoring path,
	// resetting to "" every time OriginFile changes from the parent's —
	// i.e. every time the walk crosses an import-fold boundary). A state
	// with no OriginFile stamp (shouldn't happen post-load, but
	// defensively) inherits the parent's originFile so it never reports
	// an empty SourceFile.
	var walkStates func(prefix, originPrefix, originFile string, states map[string]*State)
	walkStates = func(prefix, originPrefix, originFile string, states map[string]*State) {
		for _, name := range sortedKeys(states) {
			s := states[name]
			if s == nil {
				continue
			}
			statePath := joinPath(prefix, name)
			sOriginFile := s.OriginFile
			if sOriginFile == "" {
				sOriginFile = originFile
			}
			childOriginPrefix := originPrefix
			if sOriginFile != originFile {
				// Crossed into a differently-authored fragment (an
				// import fold boundary) — the source-relative path
				// resets to this fragment's own root.
				childOriginPrefix = ""
			}
			sourceStatePath := joinPath(childOriginPrefix, name)
			walkEffects(statePath, sourceStatePath, sOriginFile, "on_enter", s.OnEnter)
			for _, intentName := range sortedKeys(s.On) {
				arcs := s.On[intentName]
				for ai, arc := range arcs {
					walkEffects(statePath, sourceStatePath, sOriginFile, fmt.Sprintf("on.%s.arc[%d].effect", intentName, ai), arc.Effects)
				}
			}
			walkStates(statePath, sourceStatePath, sOriginFile, s.States)
		}
	}
	walkStates("", "", file, def.States)

	for _, kindName := range sortedKeys(def.Proposals) {
		pk := def.Proposals[kindName]
		if pk == nil || pk.Execute == nil {
			continue
		}
		ex := pk.Execute
		if ex.Invoke != "" && ex.OnError == "" {
			out = append(out, UnhandledInvoke{ProposalKind: kindName, SourceFile: file, Location: "execute", Invoke: ex.Invoke})
		}
		for i, c := range ex.OnComplete {
			if c.Invoke != "" && c.OnError == "" {
				out = append(out, UnhandledInvoke{ProposalKind: kindName, SourceFile: file, Location: fmt.Sprintf("execute.on_complete[%d]", i), Invoke: c.Invoke})
			}
		}
	}
	return out
}
