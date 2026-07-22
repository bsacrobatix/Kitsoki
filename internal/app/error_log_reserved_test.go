package app

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestErrorLog_RejectsStorySet proves the load-time half of Task B's
// unclearable-from-story-YAML invariant (.context/troubleshooting-agent-
// and-error-integrity.md Part 1): a story `set:`-ing world.error_log is
// rejected at load time, so a story author can never forge or erase the
// durable error history. Contrast with last_error, which stories DO reset
// 200+ times across stories/ and stays fully permitted.
func TestErrorLog_RejectsStorySet(t *testing.T) {
	yaml := `app:
  id: error-log-forge
  version: 0.1.0
intents:
  go:
    title: Go
root: chat
states:
  chat:
    mode: conversational
    on:
      go:
        - target: chat
          effects:
            - set: { error_log: [] }
`
	_, err := LoadBytes([]byte(yaml))
	require.Error(t, err)
	require.Contains(t, err.Error(), "error_log")
	require.Contains(t, err.Error(), "engine-reserved")
}

// TestErrorLog_RejectsStorySetOnOnEnter proves the same rejection fires for
// an on_enter: effect, not just a transition effect — checkEngineReservedSet
// is called from both walk sites in validateWriteMode.
func TestErrorLog_RejectsStorySetOnOnEnter(t *testing.T) {
	yaml := `app:
  id: error-log-forge-on-enter
  version: 0.1.0
root: chat
states:
  chat:
    on_enter:
      - set: { error_log: [] }
`
	_, err := LoadBytes([]byte(yaml))
	require.Error(t, err)
	require.Contains(t, err.Error(), "error_log")
	require.Contains(t, err.Error(), "engine-reserved")
}

// TestErrorLog_RejectsStoryIncrement proves the increment: form is rejected
// too (append-only — a story must not be able to nudge the log via
// increment: either, even though "increment a list" makes no semantic sense
// today; the guard is defense against a future/typo'd schema).
func TestErrorLog_RejectsStoryIncrement(t *testing.T) {
	yaml := `app:
  id: error-log-forge-increment
  version: 0.1.0
intents:
  go:
    title: Go
root: chat
states:
  chat:
    mode: conversational
    on:
      go:
        - target: chat
          effects:
            - increment: { error_log: 1 }
`
	_, err := LoadBytes([]byte(yaml))
	require.Error(t, err)
	require.Contains(t, err.Error(), "error_log")
	require.Contains(t, err.Error(), "engine-reserved")
}

// TestErrorLog_RejectsStoryBind proves bind: targeting error_log is rejected
// — a host result must not be able to overwrite the log wholesale either.
func TestErrorLog_RejectsStoryBind(t *testing.T) {
	yaml := `app:
  id: error-log-forge-bind
  version: 0.1.0
hosts:
  - host.run
intents:
  go:
    title: Go
root: chat
states:
  chat:
    mode: conversational
    on_enter:
      - invoke: host.run
        with: { cmd: "true" }
        bind: { error_log: output }
`
	_, err := LoadBytes([]byte(yaml))
	require.Error(t, err)
	require.Contains(t, err.Error(), "error_log")
	require.Contains(t, err.Error(), "engine-reserved")
}

// TestErrorLog_RejectsStorySetOfErrorOrigin mirrors the error_log rejection
// for the sibling reserved key error_origin.
func TestErrorLog_RejectsStorySetOfErrorOrigin(t *testing.T) {
	yaml := `app:
  id: error-origin-forge
  version: 0.1.0
intents:
  go:
    title: Go
root: chat
states:
  chat:
    mode: conversational
    on:
      go:
        - target: chat
          effects:
            - set: { error_origin: "chat" }
`
	_, err := LoadBytes([]byte(yaml))
	require.Error(t, err)
	require.Contains(t, err.Error(), "error_origin")
	require.Contains(t, err.Error(), "engine-reserved")
}

// TestErrorLog_LastErrorClearStillLoads proves the 200+ `last_error: ""`
// story sites are NOT affected by the error_log guard — last_error is not a
// reserved key and stories remain free to reset it.
func TestErrorLog_LastErrorClearStillLoads(t *testing.T) {
	yaml := `app:
  id: last-error-clear-ok
  version: 0.1.0
intents:
  go:
    title: Go
root: chat
states:
  chat:
    mode: conversational
    on:
      go:
        - target: chat
          effects:
            - set: { last_error: "" }
`
	_, err := LoadBytes([]byte(yaml))
	require.NoError(t, err)
}

// TestErrorLog_ReadReferenceDoesNotRequireDeclaration proves world.error_log
// / world.error_origin can be READ (e.g. in a view or relevant_world) without
// the story declaring them in its own world: block — same treatment as
// last_error / host_error (ReservedWorldKeys folds into worldKeys at load
// time).
func TestErrorLog_ReadReferenceDoesNotRequireDeclaration(t *testing.T) {
	yaml := `app:
  id: error-log-read-ok
  version: 0.1.0
root: chat
states:
  chat:
    relevant_world: [error_log, error_origin]
    view: "log has {{ len(world.error_log) }} entries, origin={{ world.error_origin }}"
`
	_, err := LoadBytes([]byte(yaml))
	require.NoError(t, err)
}
