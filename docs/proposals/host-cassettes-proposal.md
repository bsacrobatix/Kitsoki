# Host cassettes — VCR-style replay for long, multi-call flow fixtures

Author: Brad Smith. Triggered by an attempt to write one mocked end-to-end
flow fixture for `stories/bugfix` (cyber-repo) that walks all 14 phases of
the autonomous bug-fix pipeline against captured ABR-429271 run data
(`.bug-fix/ABR-429271-033/`), exercising the comment-reply round-trip at
every checkpoint without paying for an LLM call. Companion to
`tools/loopy/docs/BUGFIX-MOCKED-E2E-PLAN.md` in cyber-repo.

## 1. What the user wants to write

One flow fixture that:

1. Starts at `bootstrap`, walks `phase_minus_1 → phase_0 → phase_0_5 →
   phase_1 → phase_1_5 → phase_1_7 → phase_3 → … → phase_13 →
   terminated` — 14 phase rooms plus four checkpoint pauses
   (`phase_1_7_awaiting_reply`, `phase_6_awaiting_reply`,
   `phase_9_awaiting_reply`, `phase_13_awaiting_reply`).
2. Dispatches `host.oracle.ask_with_mcp` once per phase, where the stub
   must return **a different envelope per phase** (a repro report for
   phase 1, a fix proposal for phase 3, an impl summary for phase 6.5,
   etc.).
3. Dispatches `host.transport.post` ~15 times — once `kind=create` at
   phase 1 that returns `comment_id=8344778`, then `kind=update` at
   every subsequent phase reusing that id.
4. Replays the user's `continue` / `feedback` replies at each checkpoint.

The real data for steps 2–3 already exists on disk in run-033's artifact
directory (`01-repro-report.json`, `03-fix-proposal.json`, …,
`09.65-pr.json`) plus the trace.jsonl event log; we want to **read it
back** without re-running the pipeline.

## 2. Why the current `host_handlers:` surface can't do this

The flow-fixture stub model (`internal/testrunner/flows.go:64-163`) is
**one stub per handler name for the entire fixture**:

```go
HostHandlers map[string]HostStub `yaml:"host_handlers,omitempty"`
```

`HostStub` (lines 133-163) gives you `Data`, `Error`, `InfraError`,
`Delay`, `RequestClarification`, and `ByOp` (one envelope per `op:` arg
value). It does **not** give you per-call sequencing. The handler is
registered once into `host.Registry` (`flows.go:431`) and returns the
same canned envelope every time the orchestrator dispatches it.

Concrete gaps, anchored in the testrunner code:

### G1 — No per-call sequencing

`HostStub.Data` is a single map. Two successive dispatches of the same
handler in one fixture return the same envelope. For a 14-phase walk
that calls `host.oracle.ask_with_mcp` 14+ times with 14 different
expected returns, the surface forces one of:

- Split the fixture into 14 single-phase fixtures (loses end-to-end
  coverage of the checkpoint transitions).
- Put one mega-union of every phase's fields into a single stub
  envelope and rely on the app's bind selectors to pluck the right
  one out per phase (couples the test to bind syntax; fails the
  moment two phases bind the same field name to different shapes —
  `submitted.action` is `"edit"` in phase 12.6 but
  `"submitted.fix_description"` is a paragraph in phase 3).

There is no third option in the current code.

### G2 — Args matching limited to a single `op:` key

`HostStub.ByOp` (lines 162, 169-173) dispatches on the `op:` arg only,
because it was designed for the `host.local_files.ticket` /
`host.cypilot_artifacts` family of multi-op handlers. It cannot route on
`schema_name`, `transport`, `kind`, or any other arg the app threads
through `with:`. For `host.transport.post` — which the bugfix app
invokes with `transport: jira|bitbucket|tui`, `kind: create|update`, and
a `comment_id` payload — `ByOp` is unusable; the handler has no `op:`
arg.

### G3 — Unstubbed host calls fail quietly, not loudly

If a new arc in `app.yaml` introduces a host call that the fixture's
`host_handlers:` does not declare, two distinct paths exist today and
**neither fails the way a cassette miss should**:

1. **Pure-stub fixture (no `host_bindings:`).** `flows.go:416-430`
   only registers the stubs the fixture declared plus
   `host.jobs.answer_clarification`. An unstubbed call resolves via
   the registry's prefix-fallback (`internal/host/host.go:103-128`)
   if a parent prefix is stubbed, otherwise `Invoke` returns
   `ErrHostNotFound`. That error is real — but in practice the app's
   `on_error:` arcs catch it and route the room back to idle or a
   generic failure state. The fixture's `expect_state:` then sees the
   idle landing and either passes (if the author wrote
   `expect_state_in: [..., "idle"]`) or fails with a misleading state
   mismatch instead of the real "you forgot to stub X" signal. This
   is exactly the failure class the `kitsoki-debugging` skill exists
   to surface — the TUI swallows the underlying host error.

2. **`host_bindings:` fixture.** `flows.go:417-420` pre-registers
   every builtin via `host.RegisterBuiltins` so the real handler
   under the rebound iface can run. Any *other* unstubbed call
   (`host.run`, `host.git`, `host.transport.post`) now resolves to
   its real builtin and shells out for real against the developer's
   machine, the configured Jira instance, etc. The fixture
   silently performs production-grade side effects.

Both modes mask the regression the proposal flagged in
`dogfood-regression-testing-gap.md`: a workflow change that adds a
host call is invisible to the test until it crashes or fires for
real. The cassette miss-mode (§3.1 step 5) closes both holes with one
mechanism: a `host.*` call that didn't have a matching episode is a
hard fixture failure, regardless of whether a builtin handler
happened to be registered.

### G4 — Stubs are inline; no cassette persistence or diff

`HostStub` lives inside the fixture YAML. There is no way to:

- Share one captured trace across multiple fixtures (the bugfix
  pipeline wants 5 fixtures all keyed off the same ABR-429271 run —
  happy, feedback-at-propose, security-blocker, cycle-budget,
  pr-refinement — with overlapping host-call sets).
- Diff the captured set between two recordings (a workflow author
  who added one new host call wants to see "+1 episode, all others
  unchanged"; today they would have to diff hand-edited YAML across
  every fixture that touched the changed arc).
- Auto-regenerate the stubs from a real run.

### G5 — No request normalization for templated args

The bugfix app templates `with:` args from world: ticket id, stand url,
timestamps, artifact paths. The same logical call has different rendered
args every run. A content-hash match would never replay; the test needs
a configurable matcher (`match_on: [handler, phase, schema_name]`)
analogous to VCR's match_on. There is currently no place to declare such
a matcher — `ByOp` is the closest thing and it's hard-coded to a single
field name.

### G6 — `advance_clock` boilerplate scales linearly with phase count

Background-job phases need an `advance_clock` per turn
(`flows.go:186-192`). The bugfix pipeline backgrounds almost every
phase. A 14-phase happy walk produces 14+ `advance_clock` calls and
14+ `world_override` blocks to set `phase_N_ready: true` because the
on_complete chain doesn't auto-propagate the readiness flag across
phases.

This is a separate, smaller paper cut — fixable by either threading a
fixture-level `advance_clock_per_turn: 1s` default or by extending the
phase_template's `on_complete:` to set the readiness flag the next
phase's guard expects. **Cassettes do not address this.** Each turn
still needs its own `advance_clock:` block; the cassette only canned
the host-call results, not virtual time. Flagged here because anyone
writing the bugfix E2E fixture will hit it regardless of whether
cassettes land.

### G7 — `kitsoki replay` covers a different verb

The existing `kitsoki replay <session-id>` surface (`docs/testing.md`
§7) replays `host.oracle.task` spans by their `(initial_state_hash,
final_diff)` envelopes. The bugfix pipeline uses
`host.oracle.ask_with_mcp`, which is not in scope for the replay tool
(spans don't carry the same shape). So we cannot piggyback on the
existing replay machinery; cassettes are a new surface.

## 3. Proposal — host cassettes

One YAML file per scenario, loaded by the testrunner instead of (or
alongside) inline `host_handlers:`. Shape:

```yaml
kind: host_cassette
app_id: bugfix
app_version: 0.1.0
source_run: .bug-fix/ABR-429271-033       # provenance
generated_at: 2026-05-25T00:00:00Z
match_on: [handler, phase, schema_name]   # default matcher keys
record_mode: none                          # none | new_episodes | all

episodes:
  - id: phase_1_repro_oracle
    match:
      handler:     host.oracle.ask_with_mcp
      phase:       phase_1
      schema_name: 01-repro-report.schema.json
    response:
      data:
        submitted: !include 01-repro-report.json
    delay: 200ms

  - id: phase_1_jira_create
    match:
      handler:    host.transport.post
      transport:  jira
      thread:     ABR-429271
      kind:       create
    response:
      data: { comment_id: "8344778", posted: true }

  - id: phase_3_jira_update
    match:
      handler:    host.transport.post
      kind:       update
      comment_id: "8344778"
    response:
      data: { comment_id: "8344778", posted: true, updated: true }
```

### 3.1 Wire-in

Extend `FlowFixture` (`flows.go:55-112`):

```go
HostCassette string `yaml:"host_cassette,omitempty"`
```

Mutually exclusive with `host_handlers:` at the load step. When set,
`buildOrchestratorRig` (`flows.go:385-508`) installs a single
**cassette dispatcher** under every handler name referenced by the
cassette's episodes, instead of per-handler `HostStub` registrations.

**Interaction with `host_bindings:`.** `host_cassette:` and
`host_bindings:` are *not* mutually exclusive — a fixture can rebind
one iface to its real builtin (e.g. swap `transport` to the live Jira
handler) and cassette everything else. Order of resolution per call:

1. If an episode in the cassette matches, play it.
2. Otherwise, if `host_bindings:` rebound the iface and a real handler
   is registered, dispatch to that handler (this is the only path
   that escapes the miss-failure).
3. Otherwise, fail with `ErrCassetteMiss`.

In short: bindings declare "this iface is live; don't cassette it";
the cassette covers the rest and still fails closed.

The cassette dispatcher (new file
`internal/testrunner/cassette.go`):

1. On each invocation, walk `episodes[]` in declared order.
2. For each unplayed episode, evaluate the `match:` map against the
   call's `(handler, args)`. The match keys are looked up on the
   call's args first, then on a small set of synthetic fields the
   dispatcher computes per-call:

   - **`phase`** — the first segment of the orchestrator's current
     `StatePath` (e.g. `phase_3.dispatching` → `phase_3`). The
     dispatcher reads this off `journey.State` at invocation time,
     which is the same source `expect_state:` already consults — so
     no new derivation logic.
   - **`schema_name`** — the basename of the `schema:` arg if
     present on the call (the bugfix app threads
     `schema: 01-repro-report.schema.json` through `with:` on every
     `ask_with_mcp` dispatch). Authors who want a finer-grained slice
     than `phase` (two oracle calls in the same phase with different
     schemas) use this.
   - Fixture-level `phase_from:` (optional) overrides the
     first-segment default with a Go regex over the full `StatePath`
     for apps whose phase rooms nest differently.
3. First match wins. Episode is marked played; subsequent calls don't
   re-match it unless `replay: any` is set on that episode
   (VCR's `allow_playback_repeats`).
4. Return `host.Result{Data: episode.response.data}` (or
   `Error` / `InfraError`).
5. **On no match: fail the fixture immediately** with
   `ErrCassetteMiss{handler, args, available_episode_ids}`. This is
   the load-bearing safety property — the whole point versus the
   quiet-default behaviour described in G3.

**Coexistence with existing turn-level assertions.** `expect_host_calls`
(flows.go:233) and `expect_jobs` (flows.go:212) keep working unchanged:
they read `HostDispatched` events post-hoc. Order of failure when both
fire on the same turn: the cassette miss raises *before* the turn
completes, so a missing episode surfaces as `ErrCassetteMiss` rather
than as a downstream `expect_host_calls`/`expect_state` mismatch. The
`times:` field on `ExpectHostCall` is redundant with the cassette's
single-shot replay (each unique episode plays once) — use one or the
other, not both, for the same call.

### 3.2 Record mode

```sh
KITSOKI_CASSETTE_RECORD=new_episodes kitsoki test flows app.yaml
```

When set to `new_episodes`, a cassette miss is downgraded from a
failure to a side-effecting append: the dispatcher delegates to the
real registered handler (which the test author has presumably wired up
against a live stand or a scripted fake), captures the returned
`Result`, appends a fresh episode block to the cassette file, and
returns the result to the orchestrator. The fixture proceeds.

`all` re-records every episode (VCR's `record_mode: all`).

`none` (the default) is what CI uses.

Mirrors VCR's three-mode contract exactly. The author re-runs the
fixture once after a workflow change, commits the cassette diff, and CI
goes back to `none`.

**CI safety.** `kitsoki test flows` accepts `--strict-recording` (or
the equivalent env var `KITSOKI_CASSETTE_STRICT=1`); when set, any
non-`none` `KITSOKI_CASSETTE_RECORD` value is a hard error before any
fixture runs. CI sets `--strict-recording` so an accidental
`KITSOKI_CASSETTE_RECORD=new_episodes` in a shell profile cannot
silently re-record against production transports.

### 3.3 `!include` semantics

`!include path.json` resolves the path relative to the cassette file and
inlines the parsed JSON at YAML-load time. Lets us point at the real
`.bug-fix/ABR-429271-033/03-fix-proposal.json` envelope instead of
duplicating multi-KB JSON blobs inline — and means schema changes to
wiggum envelopes auto-propagate when the artifact file is regenerated
without any cassette edit.

### 3.4 Diff + lint

```sh
kitsoki cassette diff old.yaml new.yaml
kitsoki cassette lint cassette.yaml --against-app app.yaml
```

`diff` is a structural diff keyed by episode `id`. `lint` flags
orphaned episodes (handlers no longer dispatched anywhere in the app),
duplicate `id`s, and `!include`-references to missing files. CI fails on
orphans by default.

## 4. Out of scope

- **LLM content replay.** Cassettes capture the structured envelope
  wiggum's `submit_result` produced, not the model's free-text. For A/B
  model evaluation, `kitsoki replay --mode llm_rerun` remains the right
  surface.
- **Inbound message simulation.** `host.transport.post` is
  fire-and-forget. The bugfix pipeline's comment-reply loop is closed
  externally by `loop.py` (cyber-repo `tools/loopy/loop.py`), which
  polls Jira and synthesizes a `continue` / `feedback` intent back
  into the kitsoki session. The flow fixture expresses this by
  interleaving `intent:` turns between dispatch turns — see
  `stories/bugfix/flows/happy.yaml:60-75`. The cassette is
  outbound-only.
- **Real-stand handlers.** The cassette is a test-time stub
  replacement. The production `host.transport.post` / `host.run` /
  `host.oracle.ask_with_mcp` handlers are unchanged.
- **Backwards-incompatible changes to `HostStub`.** `host_handlers:`
  stays exactly as it is. `host_cassette:` is purely additive.

## 5. Why not just extend `HostStub` with a sequence list

The "list of responses per handler" shape (VCR-lite) is tempting because
it's a 50-LOC change. It dies on the first fixture that wants the same
handler to return envelope A at phase 1 and envelope B at phase 3 with
**a different handler interleaved between them** — there's no
cross-handler ordering primitive on `HostStub`. The cassette puts
ordering on a single flat episode list across all handlers, which is
what VCR learned to do after about a year of bug reports on the
per-host-list shape requests-mock started with.

## 6. Effort estimate

- `internal/testrunner/cassette.go` — load, match, dispatch, record.
  ~400 LOC.
- `cmd/kitsoki/cassette.go` — `diff` / `lint` subcommands. ~150 LOC.
- `FlowFixture.HostCassette` wiring in `flows.go`. ~50 LOC.
- Tests under `internal/testrunner/cassette_test.go`. ~300 LOC.
- Docs: extend `docs/testing.md` §1 with a "Host cassettes" subsection;
  add `docs/cassettes.md` for the file format reference.

Total ~900 LOC + tests. No conceptually new machinery — every primitive
exists in VCR and is well-understood. The risk concentrates in the
matcher synthetic-field derivation (`phase` from state path,
`schema_name` from `schema:` arg) — both of which already exist on
`HostDispatched` events in the event store, so the dispatcher reads
them off the same source the assertion path uses.

## 7. Test plan

- Unit: episode matching, sequencing, miss-mode behaviours, `!include`.
- Integration: convert one existing inline-`host_handlers:` fixture
  (`stories/bugfix/flows/phase_12_6_delivery_subpr.yaml` is a clean
  candidate — single oracle stub, single dispatch arc) into the
  cassette form; assert identical pass.
- End-to-end: land the 5-fixture bugfix scenario set described in
  `tools/loopy/docs/BUGFIX-MOCKED-E2E-PLAN.md` and use it as the
  acceptance test for this proposal.
