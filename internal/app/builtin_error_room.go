// Package app — the builtin error room: "there's always some basic
// on_error, then people can customize from there"
// (.context/troubleshooting-agent-and-error-integrity.md, the design doc
// this feature grew out of).
//
// injectBuiltinErrorRoom synthesizes ErrorRoomState the same way
// materialiseStandaloneExits (loader.go) synthesizes `__exit__<name>`
// terminal states — same synthesized-state naming family, a leading/trailing
// double-underscore name no hand-authored room would plausibly collide with
// by accident — and the same way builtinMetaModes / injectBuiltinStoryAuthoringRoom
// inject a default every app gets without YAML: an app declaring the same
// key wins over the builtin.
//
// This room deliberately does NOT follow those two precedents' "silently
// skip if already present" rule. A hand-authored state named __error__ that
// collides with the synthesized room is a load error (see
// injectBuiltinErrorRoom below) — the room is load-bearing for the
// unhandled-invoke fallback (see resolveOnErrorDefaults in
// on_error_default.go), so silently shadowing it would silently change
// error-routing behavior instead of failing loudly the way a bad
// on_error_default target already does.
package app

import (
	"fmt"
	"strings"

	goyaml "github.com/goccy/go-yaml"
)

const (
	// ErrorRoomState is the synthesized builtin error room's state name.
	// Non-terminal (see builtinErrorRoomState) — a story lands here to see
	// what failed, then explicitly retries or escalates. It is never a dead
	// end.
	ErrorRoomState = "__error__"

	// ErrorRoomRetryIntent routes back to world.error_origin (the state
	// path the failing invoke ran in, written by the engine per host/
	// machine-fatal failure — see ErrorOriginWorldKey) when known, else
	// stays put. This is the "way back".
	ErrorRoomRetryIntent = "error_retry"

	// ErrorRoomEscalateIntent routes to the story's own `needs_human` state
	// when the story declares one, else stays put (there is nowhere honest
	// to escalate to, so it parks rather than guessing a target). This is
	// the "way to escalate". See builtinErrorRoomState for the conditional
	// resolution — the target is never hard-coded to a state that might not
	// exist.
	ErrorRoomEscalateIntent = "error_escalate"

	// OnErrorDefaultBuiltin is the on_error_default: sentinel value that
	// selects the builtin ErrorRoomState as the story's fallback error
	// target, without the author having to spell out the synthesized state
	// name. resolveOnErrorDefaults never sees this literal value —
	// injectBuiltinErrorRoom rewrites it to ErrorRoomState before that pass
	// runs (see runLoadPipeline / LoadBytes call ordering).
	OnErrorDefaultBuiltin = "builtin"

	// OnErrorDefaultNone is the on_error_default: sentinel value that opts
	// a story OUT of the builtin error room entirely: no room is injected,
	// and (mirroring how an empty on_error_default: already behaves)
	// resolveOnErrorDefaults fills nothing. Silence (on_error_default: left
	// unset) is NOT this — see builtinErrorRoomDefaultOn.
	OnErrorDefaultNone = "none"

	// exitStatePrefix is the prefix materialiseStandaloneExits (loader.go)
	// uses for loader-synthesized `@exit:<name>` terminal pseudo-states
	// (`__exit__<name>`). Kept unexported here — IsSynthesizedState is the
	// public surface external packages should use instead of re-deriving
	// this prefix themselves.
	exitStatePrefix = "__exit__"
)

// IsSynthesizedState reports whether name is a loader-synthesized
// pseudo-state — a `__exit__<name>` terminal sink (materialiseStandaloneExits,
// loader.go) or the builtin `__error__` room (this file) — rather than a
// hand-authored room. Both are graph sinks/fallbacks injected at load time,
// never authored directly, and named from the same double-underscore family
// documented at the top of this file. Callers outside package app that build
// read-only views over a loaded app.App/AppDef (the story-editor graph in
// internal/app/graph) use this to exclude them from the room/state set the
// same way they already exclude `@exit:`/`@import` synthetic transition
// targets. NOT used by internal/viz or internal/app/render: those surfaces
// already show `__exit__<name>` sinks as real rooms/states (they're a
// legitimate part of the story's shape a human wants to see), so hiding
// `__error__` there would be an inconsistent new exclusion rather than an
// extension of an existing one — see this feature's task report.
func IsSynthesizedState(name string) bool {
	return name == ErrorRoomState || strings.HasPrefix(name, exitStatePrefix)
}

// builtinErrorRoomDefaultOn controls whether a story with NO
// on_error_default: declared at all falls back to the builtin error room
// automatically. This is the opt-in/opt-out toggle the design's Step A
// (measure) / Step B (decide) process gates:
//
//   - Step A shipped this false: the builtin room is synthesized and always
//     reachable, but only ACTIVE as a story's fallback when the author
//     explicitly writes `on_error_default: builtin`. An undeclared
//     on_error_default: behaves exactly as it did before this feature —
//     zero behavior change for the existing corpus.
//   - Flipping this to true makes EVERY story's every still-unhandled
//     invoke: (of which stories/ has on the order of a thousand) route to
//     the error room by default instead of silently continuing. See
//     .context/troubleshooting-agent-and-error-integrity.md and the
//     load-test-count report this change shipped with for whether that flip
//     happened and why.
//
// Step B tried this (kept false again after measuring): flipping it surfaced
// a real architectural gap, not just stale test goldens. Filling every gap
// resolveOnErrorDefaults finds ALWAYS redirects the whole state on failure —
// there is no per-invoke "acknowledge and keep running the rest of this
// on_enter: chain" primitive (the design doc's future `ack_error:`). Several
// rooms deliberately rely on today's no-on_error:-means-continue-in-chain
// behavior for calls whose failure is meant to be a soft degrade, not a hard
// stop — e.g. stories/bugfix/rooms/done.yaml's `done_clean_tree_check`
// ("NO on_error: this check is a non-critical backstop... If git/host.run
// can't run, worktree_dirty stays at its default (false) and the close-out
// proceeds — degrade to 'assume clean' rather than bounce the whole room to
// idle") and the identical pattern in stories/implementation/rooms/
// handoff.yaml. Flipping the default silently converts that documented
// degrade-gracefully design into a hard redirect + park, which is a real
// regression for an unattended drive, not a golden to update. Re-flip this
// only after the engine grows a way for a per-invoke site to opt back into
// "record it, keep going" (see the design doc's ack_error: sketch) so those
// rooms can be migrated explicitly instead of silently reinterpreted.
const builtinErrorRoomDefaultOn = false

// injectBuiltinErrorRoom synthesizes the builtin error room into def and, if
// applicable, rewrites def.OnErrorDefault so the following
// resolveOnErrorDefaults pass treats it as the fallback target. It must run
// AFTER every other state-synthesizing pass (so a hand-authored
// `needs_human` room, wherever it came from — including phase/workbench
// expansion — is visible for the escalate-target check below) and BEFORE
// resolveOnErrorDefaults (so a "builtin"/"" default resolves to a state that
// already exists in def.States by the time that pass's referential-
// integrity check runs).
//
// Call sites: runLoadPipeline (the Load(path) pipeline), LoadBytes (the
// no-imports single-file path), and loadImportedChild (imports.go) —
// runLoadPipeline/LoadBytes mirror injectBuiltinStoryAuthoringRoom and
// injectBuiltinMetaModes' call sites; loadImportedChild additionally calls
// this (unlike those two) right before its own resolveOnErrorDefaults call,
// so a child fragment that declares its OWN `on_error_default: builtin` (or
// picks up the flipped-on default) gets its own private __error__ room
// synthesized into its own, still-unaugmented state tree before fold —
// mirroring resolveOnErrorDefaults' documented import-boundary rule instead
// of being a second exception to it. Without this, resolveOnErrorDefaults
// would see the literal "builtin" sentinel un-rewritten and fail to resolve
// it as a state (there is no "the importer's room" to fall back to instead:
// imports resolve bottom-up, before the importer — or importers, plural,
// for a shared fragment — are known). A child fragment that declares
// no `on_error_default:` of its own (and builtinErrorRoomDefaultOn is
// false) gets nothing, exactly like a root manifest in the same position.
func injectBuiltinErrorRoom(def *AppDef, file string) []error {
	if def == nil {
		return nil
	}

	if def.OnErrorDefault == OnErrorDefaultNone {
		// Explicit opt-out: inject nothing, and normalise the sentinel to
		// "" so resolveOnErrorDefaults takes its own no-op early return
		// (on_error_default.go:65) rather than trying to resolve a state
		// literally named "none".
		def.OnErrorDefault = ""
		return nil
	}

	// Only synthesize (and wire in) the room when it will actually be USED
	// as the story's fallback target: either the author asked for it
	// explicitly ("builtin"), or the story left on_error_default: unset and
	// builtinErrorRoomDefaultOn has flipped that case to fall back too.
	//
	// A story with a custom on_error_default: (a real named state) or no
	// default at all while builtinErrorRoomDefaultOn is false does NOT get
	// the room synthesized. This is narrower than the injection-pattern
	// precedents this file's header comment cites (builtinMetaModes,
	// injectBuiltinStoryAuthoringRoom both inject unconditionally) —
	// deliberately so: unconditionally adding a graph node to every loaded
	// app broke exact node/edge-count assertions across the existing suite
	// (internal/app/graph's TestRoomGraphAcyclic /
	// TestRoomGraphPreservesCyclesAndSelfLoops, and a render golden file)
	// for stories that never asked for this room and never reference it.
	// Gating the synthesis itself, not just the default-routing wire-up,
	// keeps a story that doesn't opt in byte-for-byte unaffected — the same
	// zero-behavior-change bar the rest of on_error_default: already holds
	// itself to. See the design-doc pushback in this feature's task report.
	wantRoom := def.OnErrorDefault == OnErrorDefaultBuiltin ||
		(def.OnErrorDefault == "" && builtinErrorRoomDefaultOn)
	if !wantRoom {
		return nil
	}

	if def.States == nil {
		def.States = map[string]*State{}
	}
	if existing, exists := def.States[ErrorRoomState]; exists && existing != nil {
		return []error{&ValidationError{
			File: file,
			Message: fmt.Sprintf(
				"state %q is reserved for the builtin error room injected by on_error_default (see docs); rename this hand-authored state",
				ErrorRoomState,
			),
		}}
	}

	ensureErrorRoomIntents(def)
	def.States[ErrorRoomState] = builtinErrorRoomState(def)
	def.OnErrorDefault = ErrorRoomState
	return nil
}

func ensureErrorRoomIntents(def *AppDef) {
	if def.Intents == nil {
		def.Intents = map[string]Intent{}
	}
	if _, exists := def.Intents[ErrorRoomRetryIntent]; !exists {
		def.Intents[ErrorRoomRetryIntent] = Intent{
			Title:       "Retry",
			Description: "Return to the room the failing call was made from and try again.",
		}
	}
	if _, exists := def.Intents[ErrorRoomEscalateIntent]; !exists {
		def.Intents[ErrorRoomEscalateIntent] = Intent{
			Title:       "Escalate",
			Description: "Hand this off for human attention.",
		}
	}
}

// builtinErrorRoomState builds the synthesized room. It is intentionally:
//
//   - NOT terminal: a story that lands here must still be able to route
//     onward, and `@exit:`'s drained-log verdict rule (a future change per
//     the design doc) needs a live room to require handling from, not a
//     dead end that quietly ends the run.
//   - Non-swallowing: the view surfaces world.last_error, world.host_error,
//     world.error_origin, and the world.error_log tail directly rather than
//     summarising them away.
//   - Headless/autonomous-safe: neither route auto-fires. With no operator
//     to answer, the room simply waits for an intent the same way any other
//     room does — an unattended drive PARKS here rather than looping or
//     silently continuing. That is the documented behavior for "no operator
//     input": parking, never silent continuation.
func builtinErrorRoomState(def *AppDef) *State {
	escalateTarget := "."
	if _, ok := def.States["needs_human"]; ok {
		escalateTarget = "needs_human"
	}

	return &State{
		Description: "Builtin error room: the default landing spot for any invoke: effect with no on_error: of its own. Injected by on_error_default (see docs/embedded/app-schema.md).",
		// No explicit Transcript: "" already resolves to the same
		// "persistent" behavior for a non-conversational room (see
		// State.Transcript's doc comment) — and leaving it unset (rather
		// than "persistent") is required, not just equivalent, when this
		// room is injected into an imported child fragment (imports.go):
		// after fold the room is nested under the importer's alias
		// wrapper, and Transcript is only valid on a genuinely top-level
		// (root-tree) state.
		RelevantWorld: []string{
			"last_error",
			"host_error",
			ErrorLogWorldKey,
			ErrorOriginWorldKey,
		},
		Menu: []string{ErrorRoomRetryIntent, ErrorRoomEscalateIntent},
		View: View{
			Elements: []ViewElement{
				{Kind: "heading", Source: "Something went wrong"},
				{
					Kind:   "prose",
					Source: "A host call failed with no room-specific on_error:, so the story landed here — the builtin fallback error room. Nothing was swallowed; the detail is below. Retry to go back to where it failed, or escalate for human attention.",
				},
				{
					Kind: "kv",
					Pairs: goyaml.MapSlice{
						{Key: "Last error", Value: `{{ world.last_error|default:"(none)" }}`},
						{Key: "Host error", Value: `{{ world.host_error|default:"(none)" }}`},
						{Key: "Failed in", Value: `{{ world.error_origin|default:"(unknown)" }}`},
					},
				},
				{Kind: "heading", Source: "Error log", When: "(world.error_log ?? []) != []"},
				{Kind: "code", Source: "{{ world.error_log }}", When: "(world.error_log ?? []) != []"},
			},
		},
		On: map[string][]Transition{
			ErrorRoomRetryIntent: {
				{When: "world.error_origin != ''", Target: "{{ world.error_origin }}"},
				{Default: true, Target: "."},
			},
			ErrorRoomEscalateIntent: {
				{Target: escalateTarget},
			},
		},
	}
}
