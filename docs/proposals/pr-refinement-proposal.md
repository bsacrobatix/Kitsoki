# Kitsoki changes for external orchestration

**Status:** Draft v4. Nothing implemented yet. v4 expands the
testing scenarios end-to-end — every layer of the replay-determinism
suite is broken out into the concrete corner cases an implementer
has to cover (encoding, concurrency, crash-mid-write at every byte
offset, fsync failure, disk full, inode replacement, time-zone /
clock-skew independence, large-payload round-trip, resume across
schema versions, resume mid-oracle-call), a new "Testing the oracle
contract" subsection in §2 enumerates plugin-side failure modes
(malformed/invalid submission, sub-event ordering, call_id
collisions, subprocess crash and HTTP failure modes, env/header
interpolation, conformance across all four transports), and §3.3
covers cassette resume / matchIdx continuity, oversize episodes via
`!include`, and unmatched-episode-at-session-end as a blocking
gate. v3's contract decisions stand: `O_APPEND` over atomic rewrite,
`call_id` stays 1:1, optional sub-events on `AskResponse`, phase A
writes oracle events in parallel with the journal so `FromHistory`
is a true pass-through at phase A exit. Reconciles with the
`host-cassettes` work merged onto main between drafts (see §3).

Two kitsoki-internal changes are needed before kitsoki can host
externally-driven story runs like the cyber-repo `pr-refinement`
loop or the `claude-autofix-agent` phase-0 control inversion:

1. **Sessions fully replayable from a JSONL trace.** Today the event
   log lives in SQLite; replay (`store.BuildJourney`) only runs
   in-process against that store. To let an external driver own
   session lifecycle ("pull trace → run one turn → push trace"), the
   on-disk JSONL must be the authoritative form *and* the
   import/export round-trip must be lossless.
2. **Oracle plugin mechanism.** The `Harness` interface today is
   Anthropic-SDK-shaped (`mcp.CallToolParams` return value, claude-CLI
   subprocess assumption). To let an external system register itself
   as the LLM (autofix's bounded fixer; a CI-failure responder; a
   user's own MCP server), kitsoki needs a typed plugin contract that
   admits in-process, subprocess JSON-RPC, and MCP-over-HTTP oracles
   without each one re-implementing the harness lifecycle.

Both are prerequisites for any external orchestrator. Nothing else
in this proposal — no story design, no driver design — is in scope.
Motivating examples (cyber-repo PR refinement, autofix phase 0) are
referenced only to keep the contracts honest.

## 1. Trace-as-state

### Principle

**One trace format. One sink. Every path.** Whether a session is
driven by the TUI, by `session continue` from a Bitbucket poller, or
by `kitsoki turn` from an external driver, the on-disk session
artefact is the same fully-replayable JSONL. There is no
"interactive uses sqlite, headless uses jsonl" split — that would
mean two serialisation contracts to keep in sync, and a lossy
sqlite-to-jsonl export every time something wants to look at a
session it didn't start.

The JSONL trace is the session. Everything else (sqlite, recording
cassettes, slog) is a derived view or a debug aid.

### What we have today

- **SQLite event log** (`internal/store/`). Source of truth for
  interactive sessions. `store.BuildJourney` already proves the
  event stream is self-sufficient — sqlite is the storage, not the
  model.
- **Recording JSONL** (`internal/harness/recording.go`). Harness-
  scoped cassette for the `replay` harness; not a session log.
- **slog trace JSONL** (`internal/trace/trace.go`). Debug logs,
  lossy by design.

### What we want

The orchestrator writes every event to a `JSONLSink` — one line per
event, the existing `store.Event` shape, appended with `O_APPEND`
+ `fsync` (no prior line is ever rewritten, so atomic-rename is
unnecessary and would make `Append` O(N) per turn). Every entry
point uses it:

| Entry point        | Trace location                             |
|--------------------|--------------------------------------------|
| `kitsoki turn`     | `--trace path/to/trace.jsonl` (explicit)   |
| `kitsoki session continue` | `~/.kitsoki/sessions/<key>.jsonl` (default; `--trace` override) |
| `kitsoki tui`      | same default path, namespaced by app+key   |
| Replay / tests     | the same JSONL files used as fixtures      |

`session continue` and the TUI gain no new persistence story —
they're already keyed by `(app, transport:thread)`; that key just
maps to a path under `~/.kitsoki/sessions/` instead of a sqlite row.

```go
// internal/runner/runner.go (new orchestrator-facing seam)
type EventSink interface {
    Append(ev store.Event) error
    History() store.History  // in-memory tail for recent-turns, etc.
}
type JSONLSink struct { Path string; hist store.History; f *os.File }
func OpenJSONL(path string) (*JSONLSink, error)   // load + fold-ready, file opened O_APPEND|O_CREATE|O_WRONLY
func (s *JSONLSink) Append(ev store.Event) error  // marshal, write line, fsync
```

Load semantics: `OpenJSONL` reads the file into memory, hands the
history to `BuildJourney` for the `(state, world, turn)` fold,
then reopens for append and keeps the slice live for the rest of
the session. Append: marshal one line, `Write` it under `O_APPEND`
(POSIX-atomic for writes under `PIPE_BUF`; lines are well under it
in practice — bound `Write` size and surface an error otherwise),
`fsync`, extend the in-memory slice. A driver / TUI / continue
call that crashes mid-write may leave a torn last line; the
"truncated last line" case in test layer 5 below covers detection
and reporting.

The orchestrator takes an `EventSink`, not a `*store.Store`. Every
call site that currently INSERTs to sqlite becomes `sink.Append`.

### What happens to SQLite

The sqlite event tables go away. What remains, if anything:

- **External-key index** (`internal/store/external_keys.go`).
  Resolves `(app, transport:thread) → session_id` for `session
  continue`. Replace with a directory layout:
  `~/.kitsoki/sessions/<app>/<sha256(transport:thread)>.jsonl`.
  No index needed; the path *is* the lookup.
- **Off-path side-channel rows.** Off-path events already live in
  the same event stream (`OffPathEntered` etc.); they're just
  appended to the same JSONL. The `max(existing)+1` turn-numbering
  rule from `BuildJourney` is preserved verbatim — it's a property
  of the event stream, not of sqlite.
- **Anything else.** Nothing else in `internal/store/` is
  load-bearing for state. The package shrinks to `Event`, `History`,
  `BuildJourney`, and the new `JSONLSink`. The `database/sql`
  dependency drops out of the orchestrator entirely.

### Surface

```
kitsoki turn --app stories/foo/app.yaml \
             --trace path/to/trace.jsonl \
             --intent <name> [--slot k=v …]
```

- Trace doesn't exist → create it (header line + `TurnStarted` for
  turn 1).
- Trace exists → load, fold, run one turn, append.
- Stdout: the new events appended this turn, as JSONL — drivers
  that want streaming don't have to diff the file.
- Exit code: accepted / rejected / terminal.

`kitsoki session continue` and the TUI are unchanged from the
user's point of view; only the on-disk artefact changes from a
sqlite row to a jsonl file. `kitsoki session export` is no longer
a thing — the file already *is* the trace.

### Contract details

- **Schema = the existing `store.Event`, with `state_path` promoted
  to a top-level field on every event.** Same `Kind` enum
  (`internal/store/event.go`), same JSON-encoded `Payload`, plus
  `state_path` written inline (no exporter-side back-fill — see
  Runstatus alignment §1). User input is captured as a real
  `UserInputReceived` event at receive time (replacing the
  exporter's synthesised `turn.input` row — see §2). Oracle calls
  are captured as `OracleCalled` / `OracleReturned` events with
  full prompt and response inline (replacing the sidecar
  `KindOracleCall` journal — see §3 and the §2 Oracle plugin
  contract). `Kind` values use the dotted form the SPA already
  consumes (`turn.start`, `oracle.ask.start`, `harness.called`, …)
  so the writer and reader agree on one vocabulary.
- **Identity round-trip.** Reading a trace, running zero turns,
  writing it back produces byte-identical bytes. First of the five
  replay-determinism tests below — it catches drift the moment an
  event payload sprouts non-deterministic serialisation.
- **Forward compat.** `BuildJourney`'s default case ignores unknown
  kinds, so a trace written by a newer kitsoki still replays under
  an older one (up to the point of an unknown kind that mattered
  for state). A header line `{"kind":"SessionHeader","schema_version":1}`
  gives us migration space if we ever need it.

### Testing replay determinism

Trace-as-state is only as good as the guarantee that the trace is
lossless and replay is deterministic. Happy-path coverage is the
floor, not the ceiling — the layers below enumerate the corner
cases an implementer is required to cover. Every layer is a
blocking CI gate.

#### Test inputs

All layers run against the same fixture set, which is curated to
hit the awkward shapes the engine will see in production:

- **Cloak / oregon-trail / bugfix / dev-story** baseline fixtures
  (canonical happy-path traces, each <50 events).
- **Long-tail fixture** — synthetically generated, ≥5k events,
  ≥100 turns, mixed on-path/off-path, used for the property and
  crash-injection layers. Generated once, checked in.
- **Pathological-payload fixture** — hand-crafted events covering
  the encoding edge cases enumerated in layer 1.
- **Resume-mid-call fixture** — terminates immediately after an
  `OracleCalled` line with no matching `OracleReturned`. Used by
  layer 3 and §2's oracle-contract suite.
- **Off-path interleave fixture** — exercises `parent_turn` and
  the `max(existing)+1` numbering rule across a sequence where
  off-path events arrive while a main-path turn is in flight.

#### Layer 1 — Byte-identity round-trip

For every fixture: load JSONL → write back via `JSONLSink` →
`bytes.Equal` on file contents. Catches all serialisation drift
in one assertion. Concrete corner cases that MUST be in the
pathological-payload fixture:

- **Map ordering.** Events whose `Payload` is a nested
  `map[string]any` with ≥10 keys, written by Go's
  `encoding/json` (alphabetic key order). The fixture pins
  alphabetic order; any switch to insertion-order or randomised
  iteration breaks the test.
- **Float encoding.** `-0.0`, `1e-300`, `1e+300`, integers that
  fit and don't fit in `float64`, integer-valued floats
  (`1.0` vs `1`). The trace contract pins one representation
  per value; mismatched encoding fails this layer.
- **NaN / Inf.** Rejected at write time with a clear error.
  `encoding/json` refuses them but a custom marshaller might
  not — assert the refusal.
- **Unicode.** RTL strings, combining diacritics, four-byte
  emoji, surrogate pairs in source data (must be rejected, not
  silently transcoded). String fields use canonical NFC; an
  NFD input MUST be rejected at write time, not normalised
  silently.
- **Embedded NUL.** Rejected at write time (a NUL in a JSONL
  line would break line-based reopen).
- **Embedded newlines.** `\n` and `\r\n` inside string fields
  MUST be escaped as `
` / `` — never written as a
  literal byte. Round-trip preserves the escape form, not the
  raw byte.
- **Trailing newline.** Every line ends with `\n`. The file as
  a whole ends with `\n` after the last event (no missing
  trailing newline, no doubled trailing newline).
- **Empty payload vs missing payload.** `"payload":null`,
  `"payload":{}`, and an absent `payload` field are three
  distinct on-disk states; the writer pins one (`{}` for
  events that have no payload data) and the round-trip
  preserves it.
- **Time format.** All `ts` fields are RFC3339Nano in UTC
  with explicit `Z` suffix, never a numeric offset. Tests
  run under `TZ=Pacific/Auckland`, `TZ=UTC`, and
  `TZ=America/New_York` and produce byte-identical output.
- **Large payloads.** One event with a 4 MiB string payload
  (representing a large oracle prompt). Must round-trip
  byte-identical; must not fragment across multiple write
  calls in a way that breaks the `O_APPEND` atomicity claim
  — if the marshalled line exceeds `PIPE_BUF`, the writer
  MUST surface an error rather than risk a torn write.

Runtime budget: single-digit ms per fixture.

#### Layer 2 — Fold idempotence

`BuildJourney(history)` twice in the same process returns
deep-equal `(state, world, turn)`. A third call after a JSONL
round-trip returns the same. Corner cases:

- **Pointer identity.** `world.Vars` must not share backing
  arrays between successive folds (mutating one fold's result
  must never mutate the other's). Verified with a deliberate
  post-fold mutation that asserts the prior fold is unchanged.
- **Map iteration order.** Effects that emit multiple
  `EffectApplied` events from one room MUST come out in a
  declared order, not Go's randomised map order. The fold
  asserts ordering matches the on-disk JSONL.
- **Slice aliasing.** Events store payloads; world stores
  derived values. Mutating world post-fold must not alter
  the event-stream history slice.
- **Off-path + main-path interleave.** Fold the off-path
  interleave fixture; assert `parent_turn` linkage is
  preserved and `max(existing)+1` numbering is stable across
  reloads.

#### Layer 3 — Live ≡ replay equivalence

The headline guarantee. Run each fixture three ways:

- **Trace A:** full intent sequence live against
  `InProcessOracle` stub → capture trace.
- **Trace B:** replay A from JSONL → run the same next intent
  live → capture trace.
- **Trace C:** whole sequence live from scratch in one go →
  capture trace.

Assert `A+B == C` event-for-event, modulo wall-clock timestamps
which are zeroed in test mode. Corner cases that MUST be
covered as separate sub-cases of this layer:

- **Resume at every event boundary, not just turn boundary.**
  For a fixture of N events, run N variants where the resume
  happens after event i for i in 0..N-1. Every variant must
  produce the same final `(state, world, turn)` as the
  no-resume baseline. Catches state that was implicitly
  carried in transient fields between events of the same turn.
- **Resume mid-oracle-call.** Replay terminates on an
  `OracleCalled` with no matching `OracleReturned` (the
  resume-mid-call fixture). The reopen MUST either (a) treat
  the in-flight oracle as "not yet asked" and re-issue on
  next replay, or (b) fail loud with "trace terminates
  mid-oracle-call at line N." Policy is pinned in the test;
  silent recovery to a different state is the bug this case
  catches.
- **Resume across two different binaries.** Trace written by
  binary built at HEAD, replayed by binary built at the
  previous tagged release (and vice versa). Must succeed for
  any kinds both versions understand; must fail loud on a
  kind only the newer version knows.
- **Wall-clock independence.** Run the same fixture at three
  different wall-clock times (`faketime` or equivalent);
  asserts that ts-zeroed traces are byte-identical.

#### Layer 4 — Crash-mid-write recovery

Simulate driver death and a hostile filesystem. Required
sub-cases:

- **Random truncation.** Property test: pick a random byte
  offset within the last line; truncate the file there;
  reopen. Reopen MUST (a) detect the torn last line, (b)
  discard it, (c) fold to a state equal to the last
  fully-committed turn, (d) log a recovery message naming
  the truncated byte range. Run with N=1000 random offsets
  across the long-tail fixture.
- **Truncation at byte 0 of the last line.** Edge case where
  truncation lands exactly at the newline of the
  previous line — file is well-formed, no recovery needed.
- **Truncation that crosses a turn boundary.** Multiple
  events from one turn lost; recovery must roll back to the
  end of the previous fully-committed turn, not partially
  apply the surviving events from the lost turn.
- **fsync failure injection.** Inject an `fsync` error on
  every Nth call. Writer MUST surface the error to the
  caller (the orchestrator MUST treat it as a fatal turn
  failure, not best-effort).
- **Disk full (`ENOSPC`).** Inject on `write`. Writer
  surfaces the error; no partial line on disk after the
  error path completes; reopen finds the prior fully-
  committed state.
- **File replaced under us.** Between `OpenJSONL` and the
  next `Append`, an external process atomic-renames a new
  file over the trace path. Writer detects inode change
  (stat at open + before each append) and fails loud rather
  than appending to the wrong file.
- **Read-only filesystem.** Opening a trace on a read-only
  mount surfaces a clear error at `OpenJSONL`, not on first
  `Append`.
- **Concurrent writers.** Two processes both open the same
  trace path. Second writer MUST fail at open (flock or
  equivalent OS-level lock). The lock is released on
  process exit, including abnormal exit (verified by
  killing -9 the first writer and confirming the second
  can acquire after).
- **Symlink in path.** Trace path under
  `~/.kitsoki/sessions/` is a symlink. Resolved at open
  time; the symlink target is stable for the session
  duration. A symlink-swap mid-session is detected on
  the next stat check.

#### Layer 5 — Forward-compat / corruption

Hand-crafted inputs that exercise every reject path:

- **Unknown `EventKind`.** Silently ignored by `BuildJourney`
  but preserved byte-identical on round-trip (forward-compat
  shim).
- **Newer `schema_version`.** Explicit error at open;
  the message names the version on disk and the highest
  supported.
- **Missing header.** Error at open with "trace missing
  SessionHeader on line 1."
- **Duplicate header.** Error.
- **Header on a line other than line 1.** Error.
- **Truncated last line, prior file intact.** Distinct from
  layer 4's mid-write case in that the prior file has no
  in-flight write — the truncation is from corruption, not
  crash. Reported as "trace corrupted at line N"; reopen
  refuses to append.
- **Truncated mid-file line.** A torn line in the middle of
  the file (not the last line). Error; refuses to load.
- **Duplicate `(turn, seq)`.** Error.
- **Out-of-order `(turn, seq)`.** Error — events are
  monotonic.
- **Gap in `seq`.** Error — sequence numbers are dense
  within a turn.
- **NUL byte in a line.** Error.
- **Oversize line.** A single line above the implementation
  limit (e.g., 16 MiB). Error at read; the writer would
  have refused it at write time per layer 1.
- **Missing trailing newline at EOF.** Error — last line
  is treated as torn.
- **BOM at start of file.** Error — JSONL has no BOM.
- **CRLF line endings.** Error — POSIX `\n` only.

#### Layer 6 — Property suite

Generate random valid intent sequences against the cloak
fixture (`testing/quick` or `pgregory.net/rapid`). For each
sequence: run live → JSONL → reload → continue; assert final
`(state, world)` matches the no-reload baseline. Variants:

- Resume after every event (not just per-sequence).
- Inject a random fsync failure at one event per run; assert
  recovery from the prior state.
- Run two parallel sequences with different RNG seeds and
  assert their traces are independent (no shared mutable
  state in the engine).

This catches the gap between "round-trips byte-identical"
and "is actually replayable" — they're not the same property.

#### Layer 7 — Exporter pass-through (phase A exit gate)

For every cassette, `Snapshot.Events` returned by
`FromHistory` is the JSONL parsed line-for-line into
`TraceEvent` values. Test: parse JSONL → call `FromHistory`
→ marshal `Snapshot.Events` back to JSONL → assert
`bytes.Equal` with the input. Catches any future temptation
to re-introduce exporter-side synthesis.

Required negative case: include a fixture with oracle events
inline in the JSONL but also a (stale) `WithOracleJournal`
option set; assert the option is ignored and the JSONL is
the only source. Once `WithOracleJournal` deletes in phase
A step 8, the option-removed-build also asserts that
construction without the option produces the same snapshot.

#### Runtime budget

Total runtime for the whole replay-determinism suite: under
10s on CI; under 3s for the fast subset that excludes the
property suite and crash-injection N=1000 runs. Per
[`feedback_fast_tests`](../../../.claude/projects/-home-cloud-user-code-kitsoki/memory/feedback_fast_tests.md)
the test loop has to be tight enough that authors actually
run it before pushing. The long-tail and property suites run
on CI; the fast subset runs on every `go test ./...`.

### Runstatus alignment: the trace is the UI's only input

Runstatus (`internal/runstatus/`, `tools/runstatus/`) is the
visualiser for traces. Its contract is *"display the incoming trace
data with no manipulation"* (`tools/runstatus/CLAUDE.md`: "never use
a UI hack or adjustment to cover for a problem in the trace — the
trace itself must always be correct"). Today `FromHistory` in
`internal/runstatus/fromhistory.go` violates that contract in four
ways, all of which exist because the current SQLite event log is
*not* shaped like the UI's input. Trace-as-state must close every
gap so the JSONL line on disk is what the SPA renders, byte-for-byte
through the typed `TraceEvent` shape.

The required-to-fix list — every item here is a hack that goes away
when the JSONL trace becomes authoritative:

1. **State-path threading.** `mapHistory` walks events left-to-right
   and back-fills `state_path` onto every record from the most
   recent `StateEntered`. Events in SQLite don't carry their state.
   *Fix:* every JSONL event MUST carry `state_path` inline at write
   time. `BuildJourney` knows the active state when each event is
   appended; that value is written into the event, not reconstructed
   downstream. No back-filling, no nearest-preceding lookups.

2. **Synthesised `turn.input` rows.** `mapHistory` invents a
   `turn.input` event from `TurnStarted.payload.input` and back-dates
   it to the previous turn's group so the timeline reads
   chronologically. The SPA then has bespoke "turn.input is always
   visible, comes from the user subsystem" branches
   (`TraceTimeline.vue:119,466`, `StateDiagram.vue:265-322`). *Fix:*
   the engine writes a real `UserInputReceived` (or similarly-named)
   event at the moment the input is received, with the turn number
   it belongs to and the correct timestamp. No SPA-side branch, no
   exporter-side synthesis.

3. **Oracle events synthesised from a sidecar SQLite journal.**
   `synthesiseOracleEvents` reads `KindOracleCall` rows from a
   *second* SQLite database (`internal/host/oracle_journal.go`),
   manufactures `oracle.<verb>.start` / `oracle.<verb>.complete`
   `TraceEvent` pairs, back-calculates the start timestamp as
   `entry.Ts - 1µs` so a stable sort keeps the start before the
   complete, and uses `nearestTurn` / `nearestStatePath` to glue
   them to a turn group because the journal row's `Turn` is 0 for
   calls that fired during `RunInitialOnEnter`. This is the
   single biggest piece of "the trace is incomplete; the UI lies
   plausibly on top of it." *Fix:* §2's `OracleCalled` /
   `OracleReturned` events ARE the start/complete pair, written
   into the same JSONL with their real timestamps, real turn,
   real state_path, full prompt, and full response. The sidecar
   oracle journal goes away with sqlite; `synthesiseOracleEvents`,
   `nearestTurn`, `nearestStatePath`, `mergeEventsByTime`, and the
   `WithOracleJournal` option all delete. `MergeOracleBodyIntoAttrs`
   (`internal/runstatus/oracle_attrs.go`) deletes — the body fields
   live on the event directly.

4. **Prompt truncation done by the exporter, not the writer.**
   `promptPreviewLen = 200` (`fromhistory.go:19`) truncates the
   prompt in the synthesised start event. The UI then renders
   `prompt_preview` as a distinct attr from the full `prompt` on
   the complete event. *Fix:* one event carries the full prompt;
   any "preview" rendering is a pure presentation concern
   (CSS line-clamp or a v-if on length in the SPA), with no
   trace-side or exporter-side string slicing.

5. **`EventKind` → slog-msg renaming layer.** `msgFor` in
   `fromhistory.go` maps `TurnStarted` → `"turn.start"`,
   `LLMCalled` → `"oracle.ask.start"`, `HostInvoked` →
   `"harness.called"`, and so on, because the SPA keys subsystem
   chips off the dotted-prefix slog convention while the store
   uses Go-style enum names. This is benign as long as it's pure
   relabelling, but it's still a contract drift between writer
   and reader. *Fix:* the JSONL trace writes the dotted form
   (`"msg": "turn.start"`) directly. `store.EventKind` either
   uses the dotted form as its string value (`EventKind = "turn.start"`)
   or `msgFor` collapses into a single `string(ev.Kind)` call.
   No bespoke switch.

6. **Off-path parent-turn bookkeeping.** `parent_turn` is already a
   first-class field on `TraceEvent` and is written by the engine
   onto off-path events. This is the model to follow for the items
   above — it's the one case where the engine already writes what
   the UI consumes, with no exporter-side reconstruction.

When phase A lands, `FromHistory` collapses to: read JSONL → fold
once for the `SessionHeader` fields (current_state, turn, terminal,
started_at) → emit `{Session, App, Mermaid, Events}` where `Events`
is the JSONL parsed line-for-line into `[]TraceEvent`. No
`mapHistory`, no `synthesiseOracleEvents`, no `mergeEventsByTime`,
no `nearest*` lookups, no `msgFor`/`levelFor` rewrites — the
package shrinks to the snapshot type, the `MermaidSnapshot`
computation (still derived from `AppDef`, not the trace), and a
thin reader. The phase A exit gate adds a sixth replay-determinism
test:

7. **Exporter is a pure pass-through.** For every cassette, the
   `Events` array in the runstatus `Snapshot` returned by
   `FromHistory` is *literally* the JSONL lines parsed into
   `TraceEvent` values — `Events[i]` round-trips byte-identical to
   line `i` of the on-disk JSONL. Test: parse JSONL → call
   `FromHistory` → marshal `Snapshot.Events` back to JSONL → assert
   `bytes.Equal` with the input. Catches any future temptation to
   re-introduce exporter-side synthesis.

### Resolutions

1. **Off-path turn numbering.** The `max(existing)+1` rule is kept
   verbatim. Without sqlite the PK collision can't happen, but the
   numbering is observable in the trace and downstream tools
   (runstatus, visual analyser) rely on it.
2. **Recent-turns lookup.** `JSONLSink.History()` makes the same
   data available in-memory that the sqlite windowed query
   provided; the harness call site switches to "ask the sink"
   instead of "ask the store."
3. **Default trace path.** `~/.kitsoki/sessions/<app>/<sha8>-<slug>.jsonl`,
   where `sha8` is the first 8 hex chars of `sha256(transport:thread)`
   and `slug` is `transport:thread` with `/` and other unsafe
   characters replaced by `-`. Hash prefix gives collision-safety
   and a fixed-length leading column; slug suffix keeps `ls` output
   human-readable.
4. **Big traces.** With `O_APPEND` + `fsync`, `Append` is O(1)
   per event and `OpenJSONL` is O(N) once per session. No
   compaction strategy needed for PoC scale. The autofix loop
   and refinement loop produce hundreds-to-low-thousands of
   events per session, well within "load once, append forever"
   territory.
5. **Migration.** No migration. PoC scale, only a handful of devs
   have sqlite sessions on disk. Document the breaking change in
   the release notes for the cutover commit; users start fresh.
   `kitsoki migrate-sessions` is not built.

## 2. Oracle plugin mechanism

### What we have today

`internal/harness/harness.go`:

```go
type Harness interface {
    RunTurn(ctx context.Context, in TurnInput) (mcp.CallToolParams, error)
    Close() error
}
```

Three impls:

- `claude_cli.go` — exec the local `claude` binary; assumes
  `mcp.CallToolParams` is what comes back (because the validator MCP
  server is what Claude is talking to).
- `live.go` — anthropic-sdk-go direct.
- `replay.go` — replays a recorded YAML cassette.
- `recording.go` — wraps a live harness to capture the cassette.

Two coupling issues:

- **Return type is MCP-shaped.** `mcp.CallToolParams` bakes in the
  assumption that the LLM speaks MCP and the orchestrator's
  validator is the recipient. An external oracle (autofix's
  `bounded-fixer-agent`, an arbitrary user MCP server) doesn't
  necessarily round-trip through that validator and shouldn't have
  to fake one.
- **Lifecycle is in-process.** `Close()` assumes a subprocess or
  in-process resource. There's no shape for "the oracle is a
  long-running HTTP service I send a request to."

### What we need

A plugin contract that:

- Lets an external process register itself as the oracle for one or
  more `oracle.*` host calls in a story, without compiling into
  kitsoki.
- Preserves the safety stack the calling system already owns
  (env-filter, bash allowlist, budget tracker, audit log) — kitsoki
  must not require the plugin to expose those primitives, only to
  honour a generic ask/return contract.
- Stays backwards-compatible with the existing Harness so
  cloak/oregon-trail/bugfix keep running with `claude_cli` unchanged.

### Shape

Split the abstraction in two:

```go
// internal/harness/harness.go (unchanged for Anthropic-shaped harnesses)
type Harness interface { RunTurn(...); Close() error }

// internal/oracle/oracle.go (new — the plugin contract)
type Oracle interface {
    Ask(ctx context.Context, req AskRequest) (AskResponse, error)
    Close() error
}

type AskRequest struct {
    SessionID   app.SessionID
    TurnNumber  app.TurnNumber
    StatePath   app.StatePath
    PromptText  string                 // fully rendered
    SchemaJSON  json.RawMessage        // optional JSON-Schema for the response
    WithArgs    map[string]any         // the story's `with:` block
    World       world.World            // read-only snapshot
    Deadline    time.Time              // soft; oracle SHOULD honour
}

type AskResponse struct {
    Submission  json.RawMessage        // validated against SchemaJSON if present
    Meta        map[string]any         // tokens, cost, model — opaque to kitsoki
    SubEvents   []store.Event          // optional: plugin-emitted sub-events
                                       // appended verbatim to the JSONL between
                                       // OracleCalled and OracleReturned. Plugins
                                       // that have meaningful internal tool calls
                                       // (autofix's bounded-fixer bash/read/edit
                                       // bursts) MAY surface them this way; v1
                                       // plugins MAY leave it nil.
}
```

`AskRequest`/`AskResponse` is the wire format. It's narrow on
purpose — no MCP types, no tool-call concept, just "render a prompt,
get a schema-shaped JSON back." A `Harness`-backed oracle is one
adapter (`oracleFromHarness(h Harness) Oracle`); an MCP-over-HTTP
oracle is another; an in-process Go oracle is the third.

### Transports

Three plugin transports, one contract:

| Transport       | When                                                      | Plugin owns                                    |
|-----------------|-----------------------------------------------------------|------------------------------------------------|
| in-process Go   | Compiled-in custom oracle (tests, stub, deterministic)    | `Oracle` impl                                  |
| subprocess JSON-RPC over stdio | CLI binary the user trusts; lowest ceremony  | One method `oracle.ask`; framing per JSON-RPC 2.0 |
| MCP-over-HTTP   | Long-running external service (autofix's bounded fixer)   | A single `ask` MCP tool; kitsoki is the client |

The story declares the oracle in `hosts:` next to other host
declarations:

```yaml
hosts:
  oracle.claude:                   # default; what cloak/oregon-trail use
    plugin: builtin.claude_cli
  oracle.autofix_fixer:
    plugin: mcp_http
    endpoint: http://localhost:7301/mcp
    tool: ask
```

A room's `oracle:` block (today implicit; resolved to the global
harness) becomes explicit:

```yaml
on_enter:
  - oracle: oracle.autofix_fixer
    with: { task: "{{ args.task }}", repo: "{{ world.repo }}" }
    schema: schemas/fixer-output.json
    bind: world.fixer_result
```

Backwards compat: rooms with no `oracle:` declaration resolve to
`oracle.claude` (the existing default). Existing stories don't
change.

### Lifecycle

Plugin lifecycle stays on kitsoki:

- **in-process:** registered at boot; `Close()` on shutdown.
- **subprocess:** spawned on first `Ask`; reused for the session;
  `Close()` kills the subprocess. Recovery on crash is "respawn on
  next ask"; the trace records the crash as `OracleError`.
- **MCP-over-HTTP:** no kitsoki-owned lifecycle; the plugin is a
  service. Kitsoki opens a client per session, closes it on session
  end. Health is the plugin's problem.

Audit and replay: every `Ask` writes one `OracleCalled` event
(full prompt, with-args, schema-ref, deadline, turn, state_path,
call_id) and one `OracleReturned` event (full submission, meta,
duration_ms, the matching call_id) to the session JSONL. These
events ARE the start/complete pair the runstatus SPA already pairs
by `call_id` (`TraceTimeline.vue:371`) — no exporter-side
synthesis from a sidecar journal, no `entry.Ts - 1µs` timestamp
fudging, no `nearestTurn`/`nearestStatePath` lookups. Replay
(BuildJourney) treats both as no-ops — they exist for the audit /
runstatus surfaces, not for state reconstruction. The `Submission`
field is what binds into world; that bind is an `EffectApplied`
event the fold already understands.

This subsumes `internal/host/oracle_journal.go` (the sidecar
SQLite journal) and `internal/runstatus/oracle_attrs.go` (the
`MergeOracleBodyIntoAttrs` shim that re-shapes journal bodies into
SPA attrs). Both delete in phase B; the prompt/response body fields
live directly on the events.

This is the seam that lets phase-0 work: autofix runs its
bounded-fixer agent inside its own process, exposes it as
`oracle.autofix_fixer`, and kitsoki sees one `OracleCalled` /
`OracleReturned` pair per turn — exactly the granularity the trace
needs and the runstatus drawer renders well.

### Resolutions

1. **Schema validation locus — kitsoki validates.** `Oracle.Ask`
   returns a raw `Submission`; kitsoki validates against
   `AskRequest.SchemaJSON` and surfaces `ClarifyResponse`-equivalent
   errors on failure. Plugins are dumb pipes; the validation that
   the MCP validator-server does today moves in-process, where the
   story author can reason about it. Plugins MAY pre-validate as a
   fast-fail UX but kitsoki is the source of truth.
2. **Tool-call granularity — boundary by default, sub-events
   optional.** One `OracleCalled` / `OracleReturned` pair per
   `Ask` is the minimum the kitsoki trace records. A plugin
   that has meaningful internal tool calls (autofix's
   `run_fixer` bash/read/edit bursts) MAY populate
   `AskResponse.SubEvents` with its own `store.Event` values
   (using its own `Kind`s under a plugin-namespaced prefix,
   e.g. `oracle.autofix_fixer.bash.called`). Kitsoki appends
   those events verbatim to the JSONL between the
   `OracleCalled` and `OracleReturned` lines, preserving the
   audit fidelity the trace claims. Plugins that don't care
   leave `SubEvents` nil and the boundary-only behaviour is
   recovered. This makes the "perfect deterministic trace"
   claim load-bearing all the way through the plugin boundary
   without forcing every plugin to surface internals.
3. **Deadline — soft cap, plugin MAY ignore.** `AskRequest.Deadline`
   is a hint. Subprocess and HTTP plugins are best-effort;
   in-process plugins SHOULD honour `ctx.Done()`. Kitsoki enforces a
   hard cap via `ctx` cancel and records `OracleError` if the
   plugin overruns.
4. **Auth and secrets — `env:` + `headers:` with `${VAR}` interpolation.**
   The plugin block in `hosts:` accepts `env:` (subprocess
   transport) and `headers:` (MCP-over-HTTP transport) maps whose
   values support `${ENV_VAR}` interpolation. Substitution is
   evaluated at plugin-init time, never logged, never written to
   the trace.

### Testing the oracle contract

The §1 replay-determinism suite proves the trace round-trips; the
oracle contract needs its own failure-mode suite because the plugin
boundary is where untrusted, slow, or buggy external code meets
the deterministic core. Every case below is a blocking CI gate.

#### Schema validation (kitsoki-side)

- **Malformed JSON submission.** Plugin returns bytes that don't
  parse. Kitsoki surfaces a `ClarifyResponse`-equivalent error
  with the parse error inline; writes `OracleReturned` with the
  raw bytes preserved (for forensics) and a `validation_error`
  field set.
- **Schema-invalid submission.** Valid JSON, fails schema check.
  Same path as malformed: error surfaced, raw response preserved.
- **Submission with extra fields.** Schema is closed by default
  (`additionalProperties: false`) — extras fail validation. A
  story that wants open shape declares it in the schema; the
  story author owns that policy, not the plugin.
- **Missing required field with `null` value.** Distinct from
  field-absent. Both fail validation but produce distinct error
  messages (the schema validator's natural behaviour).
- **Schema not provided.** `SchemaJSON` is nil → kitsoki skips
  validation; raw `Submission` binds to world. Tested explicitly
  so the no-schema case is intentional, not an accidental bypass.
- **Schema with `$ref` to a sibling file.** Resolution is
  filesystem-rooted at the story directory; out-of-tree
  references fail at story-load time, not at Ask time.

#### Lifecycle and timeouts

- **Plugin crash before any output.** Subprocess transport: child
  exits with non-zero, no stdout. Trace records `OracleError`
  with exit code and stderr tail; `OracleReturned` is NOT
  written (the pair is `OracleCalled` + `OracleError`, not
  `OracleCalled` + `OracleReturned` with an error field).
- **Plugin crash after partial output.** Subprocess writes a
  partial JSON-RPC frame, then exits. Kitsoki records
  `OracleError` with the partial bytes preserved.
- **Plugin crash after sub-events, before final response.**
  Sub-events already appended to the JSONL are kept (they
  happened); `OracleError` closes the call. Replay treats
  this exactly like a fresh crash recovery (per §1 layer 3's
  resume-mid-oracle-call sub-case).
- **HTTP plugin connection refused.** Immediate error;
  `OracleError` with the dial error.
- **HTTP plugin TLS handshake failure.** Error preserves the
  TLS error chain.
- **HTTP plugin 5xx with retry hint.** Per-plugin policy:
  retry budget is in the plugin block, not the contract. Test
  pins the kitsoki-side behaviour: no automatic retry across
  `Ask` calls; the plugin is responsible for its own retries
  within one `Ask`.
- **HTTP plugin slow response.** Past `AskRequest.Deadline`:
  `ctx` cancel propagates; plugin's HTTP client honours
  `ctx.Done()`; `OracleError` records the deadline overrun
  with the soft-cap value and the actual elapsed time.
- **In-process plugin ignores `ctx.Done()`.** Hard timer in
  kitsoki kills the goroutine path (best-effort — Go can't
  forcibly kill a goroutine, but kitsoki stops waiting and
  records `OracleError`). The fixture documents this as a
  policy: in-process plugins MUST honour ctx, and the test
  proves what happens when they don't.
- **Plugin returns after timeout.** Late response arrives
  after kitsoki has already written `OracleError`. The late
  bytes are discarded; the trace does not retroactively
  rewrite. Tested with a deterministic late-return stub.

#### Sub-events

- **Sub-events with kind outside namespace.** Plugin returns
  a sub-event whose `Kind` doesn't start with
  `oracle.<verb>.` — kitsoki refuses the whole `AskResponse`
  and writes `OracleError` with a clear "sub-event kind
  violates namespace contract" message. No partial append.
- **Sub-event with `call_id` mismatching the parent.**
  Refused.
- **Sub-events ordered after the response.** The contract is
  that sub-events appear between `OracleCalled` and
  `OracleReturned`. The plugin returns them in the
  `AskResponse`, so ordering is structural — but a plugin
  that emits sub-events with timestamps *after* the
  response's emit time creates a non-monotonic stream.
  Kitsoki re-stamps sub-event timestamps at append time
  (sub-event `ts` is the plugin's claim; kitsoki's own
  monotonic `ts` is what's written). Test pins this.
- **Sub-event with payload too large.** Each sub-event is
  subject to the same per-line size limit as any other
  event (layer 1). Oversize fails the whole `AskResponse`.
- **Nil sub-event slice vs empty slice.** Both produce zero
  sub-event lines on disk; tested separately so future
  refactors don't accidentally write an empty marker line.

#### `call_id` derivation and collisions

- **Deterministic derivation produces stable IDs.** Same
  input → same `call_id` across runs.
- **Live + cassette collision.** Live derivation
  (`turn:state_path:seq`) and cassette derivation
  (`episodeID:matchIdx`) feed into the same
  `sha256("oracle-call:"+appID+":"+key)[:16]` hash. A test
  hand-picks an `episodeID:matchIdx` whose hash collides
  with a live `turn:state_path:seq` and asserts kitsoki
  detects the collision at write time. (Probability is
  cosmically low at 64 bits but the test pins behaviour
  rather than relying on luck.)
- **`replay: any` matchIdx continuity across resume.** A
  cassette episode with `replay: any` matched 3 times in
  the first session, then the session is reloaded and the
  same episode matches again — `matchIdx` is 3, not 0.
  Otherwise the new call would collide with the first
  call's `call_id`. Covered jointly with §3.3.

#### Auth and secrets

- **`${VAR}` missing.** Plugin init fails fast at story
  load with "env var X referenced in hosts.<name>.env not
  set." Never zero-values silently.
- **`${VAR}` value contains `${`.** Substitution is single-
  pass — no recursive interpolation. The literal `${`
  passes through to the plugin verbatim. Tested.
- **Secret value in trace.** Run a fixture where a secret
  is substituted into a header, then grep the resulting
  trace JSONL for the secret value. Must not appear. The
  plugin block's `env:` and `headers:` keys MAY appear in
  the trace (key names are not secret); values MUST NOT.
- **Secret value in slog.** Same grep against the slog
  trace. Must not appear.

#### Conformance — same fixture, four transports

The forcing function for the contract: one in-process
reference oracle (`testdata/reference_oracle.json` — a
fixed map from prompt to submission), wrapped four ways:

1. **In-process Go.** Implement `Oracle` directly.
2. **Subprocess JSON-RPC.** Wrap the reference oracle in a
   tiny Go binary speaking JSON-RPC over stdio.
3. **MCP-over-HTTP.** Wrap it in an httptest server
   exposing a single `ask` MCP tool.
4. **Cassette.** Pre-record the reference oracle's
   responses as a cassette; replay through the cassette
   transport.

Run the same story fixture through each transport; assert
all four produce byte-identical JSONL modulo:
- `Meta` field (plugin-opaque, may differ by transport).
- Wall-clock `ts` (zeroed in test mode).
- The `transport` field on `OracleCalled` (different by
  construction).

Everything else — `call_id` derivation, prompt, response,
schema, sub-events, error paths — is identical. This is
the test that proves the contract is actually transport-
agnostic.

## 3. Reconciliation with host-cassette work

Since the draft v1 of this proposal was written, the
`host-cassettes` branch has merged onto main
(`fcc84e0` → `46a635a`, May 2026). That work moves in the *opposite*
direction from §1.3's "the sidecar oracle journal goes away with
sqlite" claim: cassette episodes now actively *write* to the
sqlite oracle journal on replay (`internal/testrunner/cassette.go`
`writeOracleJournalEntry`), `WithOracleJournal` has been threaded
into `fromsession` and `fromflow` runstatus paths, and
`MergeOracleBodyIntoAttrs` is still load-bearing in
`synthesiseOracleEvents`. The proposal still holds, but the
sequencing is more delicate than draft v1 admitted. Three
adjustments:

### 3.1 Cassettes are first-class; the Oracle plugin must respect them

`internal/testrunner/cassette.go` defines `EpisodeOracle`
(verb / agent / model / prompt / response / call_id) as part of
a YAML episode, and `Cassette` provides ordered-match dispatch
with optional `replay: any` reuse and `UnmatchedEpisodes()`
accounting. This is the testrunner's record/replay backbone for
host calls including oracles; it is not going away. The Oracle
plugin contract (§2) must accommodate it:

- **Deterministic `call_id`, 1:1 with each exchange.**
  `derivedCallID` becomes the canonical derivation for *all*
  oracle calls (cassette-backed and live) and stays a unique key
  per exchange. The hash inputs are
  `sha256("oracle-call:"+appID+":"+key)[:16]` where:
  - cassette-backed Ask: `key = episodeID + ":" + matchIdx`
    (`matchIdx` is 0 for normal episodes, and the 0-based match
    counter for `replay: any` episodes — see next bullet).
  - live Ask: `key = turn + ":" + state_path + ":" + seq`.

  This keeps the trace's existing invariant that `call_id` is a
  unique identifier for one oracle exchange (one `OracleCalled`
  paired with one `OracleReturned`). The SPA pairing logic at
  `TraceTimeline.vue:371-393` needs no change.
- **`replay: any` episode identity.** A `replay: any` episode
  can match N times. Each match produces a new `OracleCalled` /
  `OracleReturned` pair with a *distinct* `call_id` (different
  `matchIdx`) but the *same* `episode_id` recorded as a separate
  top-level event field. Trace consumers that want to group
  reuses ("which calls came from the same episode?") read
  `episode_id`; consumers that want to pair start/complete read
  `call_id`. The two are different questions and the trace
  answers both without overloading either field.
- **Cassette-as-oracle is one transport.** A cassette-backed
  oracle ("replay this episode for this Ask") is a fourth
  plugin transport alongside in-process / subprocess JSON-RPC /
  MCP-over-HTTP. It's the transport every story test already
  uses. Its conformance against the §2 Oracle contract is the
  forcing function — if the cassette path can't drive
  `OracleCalled` / `OracleReturned` directly, the plugin contract
  is wrong.
- **Cassette file shape — single YAML, optional `!include`.**
  `internal/testrunner/cassette.go:204` already implements an
  `!include <path>` preprocessor; long prompts/responses MAY be
  pulled into sidecar files for readability, but the default and
  recommended shape is one self-contained YAML per cassette. No
  new file-sharding strategy is added by this proposal. The
  single-file default is what makes the per-story test setup
  one-glance reviewable.
- **Ordered multi-call stubs stay opaque.** Cassettes today
  record one oracle call per episode (`§6.3` in the cassette
  spec forbids `replay: any` + `oracle:` together precisely
  because reuse would duplicate journal rows). With oracle events
  written to JSONL instead of the sqlite journal, that `§6.3`
  constraint goes away — `replay: any` + `oracle:` becomes legal
  and means "this episode's oracle exchange is replayable N times,
  each producing a fresh trace event pair." Update the cassette
  validator at the same time the journal goes.

### 3.2 Oracle events land in JSONL in phase A; journal deletes in phase B

Draft v2 deferred oracle events to phase B and left
`FromHistory` half-synthesising during phase A. That created a
two-source window for runstatus oracle data and forced the
exporter to keep two code paths. Tighter sequencing:

- **Phase A writes `OracleCalled` / `OracleReturned` to the
  JSONL in parallel with the existing journal write.** The
  `writeOracleJournalEntry` call site (one place, inside
  `internal/host/`) gains a second sink: append the two events
  to the session `EventSink` at the same time. The on-disk
  journal SQLite file continues to be written so nothing else
  has to change yet. By the end of phase A, every oracle
  exchange exists in two places (journal + JSONL); runstatus
  reads only from the JSONL.
- **Phase A's `FromHistory` becomes a *pure* pass-through.** No
  oracle-specific code path. The synthesis path
  (`synthesiseOracleEvents`, `nearestTurn`, `nearestStatePath`,
  `mergeEventsByTime`, `MergeOracleBodyIntoAttrs`,
  `WithOracleJournal`) is deleted in phase A because the JSONL
  now carries the oracle events natively.
- **Phase B introduces the `Oracle.Ask` plugin contract and
  deletes the journal.** The plugin replaces the in-process
  oracle call path; cassette replay switches its event source
  from journal write to `EventSink.Append` directly (the
  parallel-write seam from phase A makes this a one-line move).
  `internal/host/oracle_journal.go` deletes in the same commit;
  the binary loses sqlite entirely.

The phase A parallel write is the cheap insurance: if anything
about the new event encoding is wrong, the journal is still
authoritative on disk and a rollback is `git revert` away. The
moment phase B's confidence gate (the conformance suite) goes
green, the journal deletes.

### 3.3 Replay determinism must cover cassette corner cases

§1's "live ≡ replay equivalence" test as drafted runs a fixture
that produces a single trace; cassette reuse via `replay: any`
and `!include` sidecar files introduce cases the determinism
suite must cover explicitly. Required test cases:

- **`replay: any` produces distinct call_ids.** For every
  `replay: any` episode in `testdata/**`, assert the N
  produced `OracleCalled` events share an `episode_id`, have
  *distinct* `call_id`s (one per match, via `matchIdx` in the
  derivation), differ in `(turn, seq)`, and have byte-identical
  `prompt` / `response` bodies.
- **`replay: any` matchIdx continuity across resume.** Run a
  fixture that matches a `replay: any` episode 3 times, write
  the trace, reload, run two more matches. The 4th and 5th
  matches MUST have `matchIdx` 3 and 4 (not 0 and 1). Test
  asserts the `call_id` for the post-resume matches differs
  from the pre-resume ones. This is the case that would
  silently corrupt the trace if `matchIdx` reset on reload.
- **Unmatched episodes are a blocking failure.** For
  `UnmatchedEpisodes()` non-empty after a full fixture run,
  fail the test with the unmatched episode IDs. Blocking
  gate in the determinism suite, not a warning.
- **Episode response fails schema.** Hand-crafted cassette
  whose recorded response fails the story's schema check.
  Cassette transport surfaces the validation error with the
  cassette path and episode ID; the trace records
  `OracleError` and points at the cassette as the source —
  not at "the LLM," which would be misleading.
- **Oversize episode via `!include`.** Episode body is a
  4 MiB string pulled in via `!include`. Cassette load
  resolves the include; the resulting `OracleCalled` /
  `OracleReturned` events serialise as single JSONL lines
  per layer 1's large-payload case. If the post-marshal
  line exceeds `PIPE_BUF`, the writer fails loud per the
  layer 1 contract.
- **`!include` target missing.** Cassette load fails at
  story-load time with the include path in the error; never
  silently substitutes empty content.
- **`!include` target outside story directory.** Refused
  for the same reason as cross-tree `$ref` in schema —
  filesystem-rooted at the story directory.
- **Cassette episode order across off-path interleave.**
  An off-path agent firing during a `replay: any` episode's
  active window must not consume a match the on-path
  caller was about to receive. Match dispatch is keyed by
  (caller path, episode), tested with the off-path
  interleave fixture extended to include a cassette-backed
  oracle.
- **Cassette + crash recovery.** Truncate the trace
  mid-cassette-call (after `OracleCalled`, before
  `OracleReturned`) and reload. The cassette's `matchIdx`
  state must reconcile with what the trace says happened
  — if the trace shows the call was made, the cassette
  considers it matched even though the response wasn't
  recorded. The next run advances `matchIdx` past it;
  the original call surfaces as `OracleError` per §2's
  resume-mid-oracle-call policy.

## 4. What this unblocks

Once both land:

- The cyber-repo `pr-refinement` loop ports to a kitsoki story whose
  `executing` room makes one `oracle: oracle.bugfix_refiner` call per
  turn. The driver round-trips the trace JSONL between Bitbucket and
  kitsoki; nothing else moves.
- `claude-autofix-agent`'s phase-0 control inversion uses
  `oracle.autofix_planner` / `oracle.autofix_fixer` / `oracle.autofix_pr_review`
  as three plugin endpoints. The bounded-fixer safety stack stays
  inside autofix's process; kitsoki owns intents, transitions, and
  the trace.
- Runstatus (the merged HTTP+SSE surface) renders any oracle the
  same way, because every plugin transport produces the same
  `OracleCalled` / `OracleReturned` events.

Neither change introduces a new user-visible kitsoki capability.
Both are substrate.

## 5. Phasing

### Phase A — JSONL sink, sqlite removal

1. `EventSink` interface + `JSONLSink` (atomic rewrite, in-memory
   history).
2. Orchestrator + every call site switches to `EventSink`. SQLite
   `Store` usages collapse onto `JSONLSink`.
3. `kitsoki turn --trace path` direct entry point.
4. `session continue` / TUI switch to default JSONL path under
   `~/.kitsoki/sessions/`.
5. No migration. Document the breaking change in the cutover
   commit message; sqlite event tables go away from the binary in
   the same change.
6. Inline event fields the runstatus exporter currently
   reconstructs: `state_path` on every event, `UserInputReceived`
   at input time (retires the exporter's `turn.input` synthesis),
   dotted-form `Kind` values matching the SPA's subsystem prefixes
   (retires `msgFor`/`levelFor`). The `Kind` rename is a breaking
   schema change for every checked-in trace and flow fixture —
   regenerate `testdata/**` and `stories/*/flows/*.yaml` cassette
   fixtures in the cutover commit and treat that regen as
   in-scope phase A work.
7. **Parallel oracle event write.** `internal/host/`'s
   `writeOracleJournalEntry` call site gains a second sink:
   append `OracleCalled` + (optional sub-events) +
   `OracleReturned` to the session `EventSink` alongside the
   existing journal write. Sub-events arrive via
   `AskResponse.SubEvents` once the plugin contract lands in
   phase B; in phase A the slice is always empty.
8. **Collapse `internal/runstatus/fromhistory.go` to a pure
   pass-through.** Parse JSONL → emit `Snapshot.Events`
   line-for-line. Delete `mapHistory`, `mergeEventsByTime`,
   `msgFor`, `levelFor`, `synthesiseOracleEvents`, `nearestTurn`,
   `nearestStatePath`, `MergeOracleBodyIntoAttrs`, and the
   `WithOracleJournal` option *in phase A*, because step 7 now
   writes oracle events into the JSONL natively. The journal
   sidecar (`internal/host/oracle_journal.go`) stays — narrowed
   to its own SQLite file under `internal/host/` and used only
   as a belt-and-braces audit until phase B's conformance gate
   green-lights its deletion. The orchestrator's `database/sql`
   dependency drops out in phase A because nothing outside
   `internal/host/` touches sqlite.
9. Full replay-determinism test suite (§1 "Testing replay
   determinism"): all seven layers, including the corner-case
   coverage spelled out per layer (encoding edge cases,
   resume-at-every-event-boundary, fsync/disk-full/inode-swap
   injection, concurrent-writer locking, schema-version
   forward-compat, oversize-line refusal) plus the §3.3
   cassette cases (matchIdx continuity across resume,
   unmatched-episode blocking gate, `!include` resolution,
   off-path interleave with cassette-backed oracle, cassette +
   crash recovery). All seven layers green at every commit is
   the phase A exit gate.

Exit: the orchestrator has no `database/sql` dependency (only
`internal/host/oracle_journal.go` does, and only until phase B);
every entry point produces the same JSONL artefact for *every*
event including oracle pairs; `FromHistory` is a pure
pass-through with no oracle-specific code path; the journal
remains on disk as a redundant audit; replay-determinism suite
green at every commit on CI.

### Phase B — oracle plugin contract

1. `internal/oracle/oracle.go` — `Oracle` / `AskRequest` /
   `AskResponse`.
2. `oracleFromHarness(h Harness) Oracle` adapter so the existing
   `claude_cli` plugs in untouched.
3. `hosts:` plugin block parser; resolution at room dispatch.
4. Subprocess JSON-RPC plugin transport.
5. MCP-over-HTTP plugin transport.
6. `OracleCalled` / `OracleReturned` event kinds are *already*
   landed in phase A; phase B only adds the plugin contract that
   produces them via `Oracle.Ask` instead of the in-process
   handler. `call_id` derivation per §3.1: 1:1 with each
   exchange, `key = episodeID + ":" + matchIdx` for cassette and
   `key = turn:state_path:seq` for live.
7. `AskResponse.SubEvents` support: kitsoki appends them
   verbatim to the JSONL between the `OracleCalled` and
   `OracleReturned` lines. Sub-event `Kind`s use a
   plugin-namespaced prefix (`oracle.<verb>.<plugin-internal>`)
   so the SPA's subsystem chip logic keeps working.
8. Cassette path switches `writeOracleJournalEntry` to
   `sink.Append` only — the parallel-write seam from phase A
   step 7 makes this a one-line move. Cassette `§6.3` validator
   constraint (`replay: any` + `oracle:` forbidden) relaxes:
   multiple matches produce multiple event pairs with *distinct*
   `call_id`s (different `matchIdx`) sharing one `episode_id`.
9. Delete in one commit: `internal/host/oracle_journal.go`,
   `internal/journal/sqlite.go` (if no remaining consumers),
   and any remaining cassette journal-write code paths. The
   orchestrator's `internal/host/` package loses its
   `database/sql` dependency at this point; the binary as a
   whole loses sqlite.
10. Full oracle-contract test suite (§2 "Testing the oracle
    contract"): schema-validation failure modes (malformed,
    schema-invalid, extra fields, missing schema), lifecycle
    failure modes (crash before/after partial output, HTTP
    refused/TLS/5xx/slow, deadline overrun, in-process plugin
    ignoring `ctx.Done()`, late response after timeout),
    sub-event ordering and namespace enforcement, `call_id`
    collision detection across live + cassette derivations,
    auth/secret non-leakage (grep trace + slog), plus the
    four-transport conformance suite (in-process, subprocess
    JSON-RPC, MCP-over-HTTP, cassette) producing byte-identical
    `OracleCalled` / `OracleReturned` events modulo `Meta` and
    `transport`. All green is the phase B exit gate.

Exit: a stories-side test declares an MCP-over-HTTP oracle backed by
a Go test server and the room runs end-to-end with no Anthropic SDK
on the call path.

## 6. Out of scope

- The PR-refinement story itself, or any other consumer story.
- The autofix-oracle MCP server (lives in `claude-autofix-agent`).
- Driver design (lives in the consumer repo).
- Multi-tenant oracle hosting, oracle health/observability beyond
  the per-turn audit events.
- Renaming or restructuring the existing `Harness` interface; v1
  keeps it as the in-process default plugin.

## 7. Decision needed

Approve §1 (trace-as-state) and §2 (Oracle plugin) as scoped here.
Both are kitsoki-internal. Realistic estimate behind a clean green
light: **phase A ~1.5–2 weeks**, **phase B ~1–1.5 weeks**. Phase A
is wider than v1/v2 admitted — the call-site sweep
(orchestrator, off-path, intent dispatch, recording), the 7-layer
determinism suite, sqlite removal from `internal/store/`, the
runstatus fixture-cmd updates (`tools/runstatus/fixtures/cmd/
{fromsession,fromflow}`), the parallel oracle event write, and
the `Kind`-rename fixture regen across `testdata/**` +
`stories/*/flows/*.yaml` all land in phase A. Phase A unblocks
phase B (the plugin's `Ask` events have to land in the JSONL
that phase A makes round-trippable).
