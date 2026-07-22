package app

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBuiltinErrorRoom_NotInjectedWithoutOptIn asserts that a story which
// declares no on_error_default: at all does NOT get the __error__ room
// synthesized while builtinErrorRoomDefaultOn is false (Step A/B — see that
// const's doc comment for why Step B tried flipping it and kept it false
// after measuring: real rooms rely on today's no-on_error:-means-continue
// behavior for calls designed to degrade gracefully, and there is no
// per-invoke way to opt back into that once the default fills every gap).
// This keeps a story that never asked for the room byte-for-byte
// unaffected: no new graph node, no new state in rendered docs. See
// TestBuiltinErrorRoom_OptInActivatesFallback for the opted-in shape.
func TestBuiltinErrorRoom_NotInjectedWithoutOptIn(t *testing.T) {
	yaml := []byte(`app:
  id: builtin-error-room-absent-by-default
  version: 0.1.0
hosts: [host.run]
root: start
states:
  start:
    terminal: true
`)
	def, err := LoadBytes(yaml)
	require.NoError(t, err)
	require.NotContains(t, def.States, ErrorRoomState)
}

// TestBuiltinErrorRoom_OptInActivatesFallback asserts that
// `on_error_default: builtin` fills every unhandled invoke:'s on_error: with
// the synthesized error room's state name.
func TestBuiltinErrorRoom_OptInActivatesFallback(t *testing.T) {
	yaml := []byte(`app:
  id: builtin-error-room-optin
  version: 0.1.0
hosts: [host.run]
on_error_default: builtin
root: start
states:
  start:
    on_enter:
      - invoke: host.run
`)
	def, err := LoadBytes(yaml)
	require.NoError(t, err)
	require.Equal(t, ErrorRoomState, def.States["start"].OnEnter[0].OnError)
	require.Contains(t, def.States, ErrorRoomState)
}

// TestBuiltinErrorRoom_PerInvokeStillWinsOverBuiltin mirrors
// TestOnErrorDefault_PerInvokeWins for the builtin sentinel specifically: a
// hand-authored on_error: is never overwritten, even when the story-level
// default is "builtin".
func TestBuiltinErrorRoom_PerInvokeStillWinsOverBuiltin(t *testing.T) {
	yaml := []byte(`app:
  id: builtin-error-room-per-invoke-wins
  version: 0.1.0
hosts: [host.run]
on_error_default: builtin
root: start
states:
  start:
    on_enter:
      - invoke: host.run
        on_error: specific_handler
  specific_handler:
    terminal: true
`)
	def, err := LoadBytes(yaml)
	require.NoError(t, err)
	require.Equal(t, "specific_handler", def.States["start"].OnEnter[0].OnError)
}

// TestBuiltinErrorRoom_OptOutSuppressesInjection asserts
// `on_error_default: none` both suppresses the room's injection and leaves
// unhandled invokes unhandled, exactly like today's no-on_error_default:
// behavior.
func TestBuiltinErrorRoom_OptOutSuppressesInjection(t *testing.T) {
	yaml := []byte(`app:
  id: builtin-error-room-optout
  version: 0.1.0
hosts: [host.run]
on_error_default: none
root: start
states:
  start:
    on_enter:
      - invoke: host.run
`)
	def, err := LoadBytes(yaml)
	require.NoError(t, err)
	require.NotContains(t, def.States, ErrorRoomState, "on_error_default: none must suppress injection entirely")
	require.Empty(t, def.States["start"].OnEnter[0].OnError)

	unhandled := UnhandledInvokes(def, "test.yaml")
	require.Len(t, unhandled, 1)
}

// TestBuiltinErrorRoom_NameCollisionIsLoadError asserts a hand-authored
// state named __error__ is a clear load error, not a silent shadow — unlike
// injectBuiltinStoryAuthoringRoom's silent-skip-if-present precedent, this
// room is load-bearing for the fallback mechanism.
func TestBuiltinErrorRoom_NameCollisionIsLoadError(t *testing.T) {
	yaml := []byte(`app:
  id: builtin-error-room-collision
  version: 0.1.0
hosts: [host.run]
on_error_default: builtin
root: start
states:
  start:
    terminal: true
  __error__:
    terminal: true
`)
	_, err := LoadBytes(yaml)
	require.Error(t, err)
	require.Contains(t, err.Error(), "__error__")
	require.Contains(t, err.Error(), "reserved")
}

// TestBuiltinErrorRoom_EscalateResolvesToDeclaredNeedsHuman asserts the
// escalate route targets the story's own needs_human room when one is
// declared, rather than a hard-coded name that might not exist.
func TestBuiltinErrorRoom_EscalateResolvesToDeclaredNeedsHuman(t *testing.T) {
	yaml := []byte(`app:
  id: builtin-error-room-escalate-declared
  version: 0.1.0
hosts: [host.run]
on_error_default: builtin
root: start
states:
  start:
    terminal: true
  needs_human:
    terminal: true
`)
	def, err := LoadBytes(yaml)
	require.NoError(t, err)
	arcs := def.States[ErrorRoomState].On[ErrorRoomEscalateIntent]
	require.Len(t, arcs, 1)
	require.Equal(t, "needs_human", arcs[0].Target)
}

// TestBuiltinErrorRoom_EscalateParksWithoutNeedsHuman asserts the escalate
// route does NOT hard-code a target when the story declares no needs_human
// room — it stays put (parks) instead of guessing.
func TestBuiltinErrorRoom_EscalateParksWithoutNeedsHuman(t *testing.T) {
	yaml := []byte(`app:
  id: builtin-error-room-escalate-park
  version: 0.1.0
hosts: [host.run]
on_error_default: builtin
root: start
states:
  start:
    terminal: true
`)
	def, err := LoadBytes(yaml)
	require.NoError(t, err)
	arcs := def.States[ErrorRoomState].On[ErrorRoomEscalateIntent]
	require.Len(t, arcs, 1)
	require.Equal(t, ".", arcs[0].Target)
}

// TestBuiltinErrorRoom_RetryRoutesToErrorOrigin asserts the retry route
// targets world.error_origin when known and stays put otherwise — the "way
// back" and its headless-safe fallback (park, never silently continue).
func TestBuiltinErrorRoom_RetryRoutesToErrorOrigin(t *testing.T) {
	yaml := []byte(`app:
  id: builtin-error-room-retry
  version: 0.1.0
hosts: [host.run]
on_error_default: builtin
root: start
states:
  start:
    terminal: true
`)
	def, err := LoadBytes(yaml)
	require.NoError(t, err)
	arcs := def.States[ErrorRoomState].On[ErrorRoomRetryIntent]
	require.Len(t, arcs, 2)
	require.Equal(t, "world.error_origin != ''", arcs[0].When)
	require.Equal(t, "{{ world.error_origin }}", arcs[0].Target)
	require.True(t, arcs[1].Default)
	require.Equal(t, ".", arcs[1].Target)
}

// TestBuiltinErrorRoom_NotTerminalAndReachable asserts the room is a real,
// non-terminal, transition-reachable state — not a dead end.
func TestBuiltinErrorRoom_NotTerminalAndReachable(t *testing.T) {
	yaml := []byte(`app:
  id: builtin-error-room-reachable
  version: 0.1.0
hosts: [host.run]
on_error_default: builtin
root: start
states:
  start:
    on_enter:
      - invoke: host.run
`)
	def, err := LoadBytes(yaml)
	require.NoError(t, err)
	room := def.States[ErrorRoomState]
	require.NotNil(t, room)
	require.False(t, room.Terminal)
	require.NotEmpty(t, room.On[ErrorRoomRetryIntent])
	require.NotEmpty(t, room.On[ErrorRoomEscalateIntent])
}

// TestBuiltinErrorRoom_ImportedFragmentGetsItsOwnRoom asserts an imported
// child fragment that declares its OWN `on_error_default: builtin` resolves
// cleanly: loadImportedChild (imports.go) injects the child's own __error__
// room into the child's own pre-fold tree before resolving the child's own
// on_error_default against it — mirroring the root pipeline's
// injectBuiltinErrorRoom → resolveOnErrorDefaults ordering instead of
// leaving resolveOnErrorDefaults to see the literal, un-rewritten "builtin"
// sentinel (which used to fail to resolve as a state — see this feature's
// task report). The folded room lands under the importer's alias prefix
// like any other child-owned state.
func TestBuiltinErrorRoom_ImportedFragmentGetsItsOwnRoom(t *testing.T) {
	root := t.TempDir()
	childDir := mkdirT(t, root, "child")
	mustWrite(t, childDir, "app.yaml", `app: { id: child, version: 0.1.0 }
hosts: [host.run]
on_error_default: builtin
root: idle
states:
  idle:
    on_enter:
      - invoke: host.run
`)
	parentDir := mkdirT(t, root, "parent")
	mustWrite(t, parentDir, "app.yaml", `app: { id: parent, version: 0.1.0 }
intents:
  go: {}
imports:
  bf:
    source: ../child
    entry: idle
root: start
states:
  start:
    on:
      go:
        - target: bf
`)
	def, err := Load(filepath.Join(parentDir, "app.yaml"))
	require.NoError(t, err)

	require.Contains(t, def.States["bf"].States, ErrorRoomState,
		"the child's own error room must be folded in under the importer's alias")
	require.NotEmpty(t, def.States["bf"].States["idle"].OnEnter[0].OnError,
		"the child's own on_error_default: builtin must have resolved against the child's own room")
	// The parent itself declared no on_error_default: of its own and
	// builtinErrorRoomDefaultOn is false, so the parent gets no room of its
	// own — this is purely the child's private, self-contained resolution.
	require.NotContains(t, def.States, ErrorRoomState)
}
