// Package trace tests cover the event-name conventions that the
// JSONL pretty-printer depends on (every Ev* constant is a
// dotted-lowercase string with no whitespace and no empty segments).
package trace

import (
	"strings"
	"testing"
)

// TestEventNamesAreDotted asserts that every event-name constant exported by
// this package is a well-formed dotted identifier: lowercase ASCII, dot-
// separated, no empty segments, no whitespace. The pretty-printer keys off
// the leading segment (turn., harness., offpath., …) to assign topic chips
// — a malformed name would silently fall through to the dim DEBUG bucket.
func TestEventNamesAreDotted(t *testing.T) {
	// The list mirrors every Ev* constant declared in trace.go. New
	// constants must be added here when they're added to the taxonomy —
	// the trace.go package doc references it as the source of truth.
	names := []string{
		EvTurnStart, EvTurnRouted, EvTurnStepped, EvTurnPersisted, EvTurnDone,
		EvHarnessRequest, EvHarnessResponseRaw, EvHarnessResponseParsed,
		EvHarnessRetry, EvHarnessError, EvHarnessExec,
		EvHarnessRecordingHit, EvHarnessRecordingMiss,
		EvMachineGuardEval, EvMachineGuardWinner, EvMachineEffectApplied,
		EvMachineTransition, EvMachineValidationRejected,
		EvExprCompileError, EvExprEvalError,
		EvStoreEventsAppended,
		EvTurnDeterministicHit, EvTurnDeterministicMiss,
		EvOffPathEnter, EvOffPathExit, EvOffPathAskStart, EvOffPathAskDone,
		EvOffPathAskError, EvOffPathChatResolved,
		EvTimeoutArmed, EvTimeoutCancelled, EvTimeoutFired, EvTimeoutError, EvTimeoutRearmed,
		EvTeleportStart, EvTeleportDone,
		EvJobSubmitted, EvJobTerminal, EvJobAwaitingInput,
		EvJobClarificationAnswered, EvJobOnCompleteRun, EvJobError,
		EvSlotFillRequested, EvSlotFillContinued,
		EvDisambigPresented, EvDisambigChosen,
		EvInboxNotificationPosted, EvInboxItemOpened, EvInboxItemDismissed,
	}

	seen := make(map[string]bool, len(names))
	for _, n := range names {
		if n == "" {
			t.Errorf("event name is empty (taxonomy gap)")
			continue
		}
		if strings.ContainsAny(n, " \t\n") {
			t.Errorf("event %q contains whitespace", n)
		}
		if !strings.Contains(n, ".") {
			t.Errorf("event %q is missing the topic prefix dot", n)
		}
		for _, seg := range strings.Split(n, ".") {
			if seg == "" {
				t.Errorf("event %q has empty segment", n)
			}
			if strings.ToLower(seg) != seg {
				t.Errorf("event %q has non-lowercase segment %q", n, seg)
			}
		}
		if seen[n] {
			t.Errorf("event %q is duplicated in the taxonomy", n)
		}
		seen[n] = true
	}
}

// TestEventNamePrefixes asserts that each new event family lives under its
// expected prefix so the pretty-printer's HasPrefix dispatch lines up.
func TestEventNamePrefixes(t *testing.T) {
	cases := map[string]string{
		"offpath.":  EvOffPathEnter,
		"timeout.":  EvTimeoutArmed,
		"teleport.": EvTeleportStart,
		"job.":      EvJobSubmitted,
		"slotfill.": EvSlotFillRequested,
		"disambig.": EvDisambigPresented,
		"inbox.":    EvInboxNotificationPosted,
	}
	for prefix, ev := range cases {
		if !strings.HasPrefix(ev, prefix) {
			t.Errorf("expected %q to have prefix %q", ev, prefix)
		}
	}
}
