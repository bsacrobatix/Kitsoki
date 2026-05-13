// Package journal implements the kitsoki session journal — a durable, ordered
// append-only event log that is the canonical source of truth for resumable
// sessions (the "continue mode" proposal).
//
// # One log, two consumers
//
// The journal is designed around the "one log, two consumers" principle: the
// same JSONL stream that --trace emits for human debugging is also the feed
// that --continue reads to reconstruct session state. There is no separate
// resume data path — resume is pure replay of the journal.
//
// # Hybrid patch + typed entries
//
// Entries come in two shapes (§2.2 of the proposal):
//
//   - Patch entries — atomic RFC 6902 JSON-Patch ops against one of the four
//     physical documents: "world", "state", "chats/<id>", "jobs/<id>". Kind
//     values: [KindWorldPatch], [KindStateTransition], [KindChatsAppend],
//     [KindJobsUpdate].
//
//   - Typed entries — semantic events whose payloads cannot be reconstructed
//     from a patch sequence: host invocations, clarification schemas, timeout
//     arms, inbox-item lifecycle, meta-mode proposal lifecycle, off-path
//     question/answer pairs. Kind values: [KindHostInvoked] and siblings.
//
// Checkpoint entries ([KindWorldCheckpoint] and siblings) carry a full
// document snapshot and bound replay cost (§2.3, §4.4).
//
// # Schema-aware applier
//
// Patch application for the "world" document is schema-aware: after every
// JSON-Patch apply the [Applier] re-coerces numeric values declared as
// "int" in [kitsoki/internal/app.WorldSchema] back to int64, preventing
// float64 drift through encoding/json. This mirrors the coerceWorldVar logic
// in internal/store/replay.go (requirement R1 of the proposal spike).
//
// # Resume is pure replay
//
// [Reader.LoadDocument] + [Reader.ReplayFrom] + [Reader.ReplayTyped] are
// pure-data operations: no Harness, no host.Registry, no transport, no LLM
// call, no time.Now() inside the apply loop. The [Applier] and [Reader]
// constructors intentionally accept no such parameters.
//
// # Reference
//
// docs/proposals/continue-mode-proposal.md §2.2, §4.1, §4.4, §4.5
package journal
