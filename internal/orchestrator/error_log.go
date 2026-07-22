// Package orchestrator — world.error_log: the append-only, engine-reserved
// error history (.context/troubleshooting-agent-and-error-integrity.md,
// Part 1). Every host_infra / host_domain host-dispatch failure (see
// dispatchHostCalls in host_dispatch.go) and every machine-fatal Turn error
// (see journalTurnError in journal_write.go) appends one entry here.
//
// Unlike last_error / host_error — single-slot "most recent" convenience
// views that a story may freely reset (200+ `last_error: ""` sites in
// stories/) — error_log never loses history within a session, and a story
// cannot clear it: internal/app/loader.go's checkEngineReservedSet rejects
// any set:/increment:/bind: effect that targets it at load time.
package orchestrator

import (
	"time"

	"kitsoki/internal/app"
	"kitsoki/internal/clock"
	"kitsoki/internal/world"
)

// Error classes recorded on a world.error_log entry's "class" field.
const (
	// ErrorLogClassHostInfra marks a host call that could not run at all
	// (handler not registered, subprocess failed to spawn, ...) — the err
	// != nil branch of dispatchHostCalls.
	ErrorLogClassHostInfra = "host_infra"
	// ErrorLogClassHostDomain marks a host call that ran but reported a
	// domain failure (non-zero exit / an explicit Result.Error) — the
	// res.Error != "" branch of dispatchHostCalls.
	ErrorLogClassHostDomain = "host_domain"
	// ErrorLogClassMachine marks a machine-fatal error: a guard or effect
	// expression that failed to compile/evaluate and aborted the whole
	// Turn before any host call was ever reached (journalTurnError).
	ErrorLogClassMachine = "machine"
)

// nextErrorLogSeq derives the next monotonic sequence number for a new
// world.error_log entry from the current log's length, so replay
// reconstructs the identical seq purely from world state — no separate
// counter needs its own persistence, and the value stays deterministic
// under cassette/flow replay.
func nextErrorLogSeq(w world.World) int {
	existing, _ := w.Vars[app.ErrorLogWorldKey].([]any)
	return len(existing) + 1
}

// newErrorLogEntry builds one world.error_log entry:
//
//	{ seq, state, namespace?, effect?, message, exit_code?, stderr?, ts,
//	  class, handled }
//
// namespace/effect are omitted when empty; exitCode/stderr are omitted when
// nil. ts is read from clk — the orchestrator's injected clock.Clock (real
// in production, a clock.Fake pinned by the flow-test harness) — rather than
// time.Now(), so the field stays reproducible under flow/cassette replay.
// handled is always false at append time: an entry becomes handled only via
// an explicit acknowledging effect, which is out of scope for this change
// (see the design doc's Part 1 fix #1/#2 — the @error intent and its
// handling are owned elsewhere).
func newErrorLogEntry(clk clock.Clock, seq int, class string, state app.StatePath, namespace, effect, message string, exitCode, stderr any, handled bool) map[string]any {
	entry := map[string]any{
		"seq":     seq,
		"state":   string(state),
		"message": message,
		"class":   class,
		"handled": handled,
		"ts":      clk.Now().UTC().Format(time.RFC3339),
	}
	if namespace != "" {
		entry["namespace"] = namespace
	}
	if effect != "" {
		entry["effect"] = effect
	}
	if exitCode != nil {
		entry["exit_code"] = exitCode
	}
	if stderr != nil {
		entry["stderr"] = stderr
	}
	return entry
}

// appendErrorLog returns world.error_log with entry appended. Append-only:
// existing entries are copied forward untouched, never mutated or dropped.
func appendErrorLog(w world.World, entry map[string]any) []any {
	existing, _ := w.Vars[app.ErrorLogWorldKey].([]any)
	out := make([]any, 0, len(existing)+1)
	out = append(out, existing...)
	out = append(out, entry)
	return out
}
