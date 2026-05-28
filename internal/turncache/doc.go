// Package turncache provides storage for prior LLM-resolved turn verdicts
// keyed by a deterministic lexical signature, together with per-synonym hit
// counters used by the synonym-routing inspect views.
//
// The package is the storage abstraction for the semantic-routing
// design.  Phase 5 ships an in-memory implementation behind the
// [Cache] interface; Phase 6 will add a SQLite backend behind the
// same interface, so the interface here is load-bearing.
//
// The cache itself is intentionally dumb: it does not decode slot JSON, it
// does not re-validate verdicts against a [Machine], and it does not know
// anything about turn dispatching. The orchestrator owns all of that — the
// cache only stores rows, tracks hit counts, and enforces the eviction
// policies described in §7.1–§7.5.
//
// Keys are four-tuples of (App, AppHash, StatePath, Signature). App is the
// app id (e.g. "oregon-trail") while AppHash is the hash of the AppDef
// intent surface; the two together let [Cache.InvalidateOtherHashes] purge
// rows for an app's earlier hashes without touching unrelated apps that
// happen to share the same on-disk store (relevant for Phase 6).
//
// # Backends
//
// Two implementations of [Cache] ship in the package:
//
//   - [NewMemory] — pure in-process map+mutex. No persistence; the
//     cache evaporates with the process. Cheapest path (Get is a map
//     lookup); appropriate for drive-mode reproducibility runs, unit
//     tests, and any flow where the orchestrator wants cache
//     semantics without cross-session leakage. The default for code
//     that does not opt in to durability.
//
//   - [NewSQLite] — single-file SQLite via modernc.org/sqlite. Adds
//     cross-session reuse (the row survives a process restart) at the
//     cost of one disk-bound op per call (WAL-mode, NORMAL fsync).
//     The proposal's §5.4 budget is ~80 µs/op for the read path; the
//     bench in sqlite_bench_test.go confirms the actual numbers in
//     CI. Pick this backend when the orchestrator wants the cache to
//     survive process restarts (production runs, multi-session apps),
//     and accept the marginal latency.
//
// Both backends satisfy the same [Cache] interface. The conformance
// suite in conformance_test.go runs every behavioural assertion
// against both, so a regression on one backend that doesn't affect
// the other is caught as a divergent test outcome rather than silent
// drift.
package turncache
