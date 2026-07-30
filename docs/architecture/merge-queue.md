# Capsule merge queue: retry policy, human overrides, and conflict resolution

The capsule merge queue (`internal/capsule/queue`) serializes receipt-bound
candidates onto a protected ref through speculation → deterministic gate →
compare-and-swap finalization. This document covers the operational semantics
productized from the POG shell-script extension of the queue: bounded retries,
the emergency lane, human override verbs, WIP preservation, and automatic
conflict resolution. The design goal is a single invariant:

> **The queue can never reach a state a human cannot exit, and it never loses
> data.** Every non-terminal candidate is either making progress, waiting on a
> durable timer clearable by `kick`, or parked — in `needs_input`,
> `needs_conflict_input`, or `needs_human` — with an audited evidence trail
> and `resume` / `override` / `reject` available.

## Ordering and the never-stuck train

Candidates carry an immutable admission `sequence` and a durable `position`.
Claiming and finalization order is: the **emergency lane** first (FIFO within
itself, by `emergency_sequence`), then `position`. A candidate that fails its
gate or speculation is requeued **to the back of the line** in `retry_wait`
with a durable `retry_at` under exponential backoff (`RetryDelay · 2^(n−1)`,
capped at `MaxRetryDelay`; defaults 5m/30m). After `MaxAttempts` (default 5)
it parks as `needs_human` instead of spinning: the queue's own declaration
that automation is out of options, not an operator's own park (see "Typed
failure reasons and `needs_human`" below). A failing candidate therefore
delays only itself; the finalization head skips parked and timer-waiting
candidates, and the protected base CAS remains the correctness guard.

Failures of the queue's own machinery — a resolver, gate, or finalizer
harness that could not launch — are classified separately
(`queue.Harness(err)`) and park the candidate immediately as `needs_human`
without burning bounded retry attempts.

## Typed failure reasons and `needs_human`

Every `retry_wait`/parked candidate carries two things together: `retry_reason`
(a free-text stage tag, unchanged — humans and `queue sweep`'s substring
matching still read it) and `reason_code`, a small closed-set enum
(`queue.ReasonCode`) an operator or a medic can switch on instead of parsing
prose. The codes: `gate-failed`, `merge-conflict`, `lease-lost`,
`seal-mismatch` (reserved), `resolver-failed`, `resolver-exhausted`,
`repairer-exhausted`, `finalization-failed`, `budget-exhausted` (generic
fallback), `environment-degraded`, `harness-failure`, `operator-parked`, and
`legacy-freeform` for a pre-existing record whose `retry_reason` is its
current explanation but which has no code.

Every `state.json` written before this enum existed loads cleanly, but the
`legacy-freeform` stamp is deliberately **narrower** than "any record with a
non-empty `retry_reason`": `normalize` applies it only to a candidate
currently in `retry_wait` or a parked phase. `retry_reason` is durable
history that outlives the failure it describes — `claimPreparation` clears
`reason_code` and `failure` on the candidate it claims but intentionally
keeps `retry_reason` as "what happened last time", and it survives landing
too — so an unscoped stamp would classify in-flight (`preparing`/`gating`/
`finalizing`) and `landed` records as legacy when they are nothing of the
kind. That mislabel would also be permanent, because `normalize` runs inside
`write`: the next durable save for any unrelated reason would persist it.
Such records therefore keep an empty `reason_code`, matching `Summarize`'s
own `retry_wait`/parked guard for both roll-ups. In practice this classifies
exactly the population the enum was introduced for (the parked candidates
carrying hand-typed prose) and nothing else.

`resolver-failed` and `resolver-exhausted` are deliberately two codes, not
one: `resolver-failed` is the still-retrying counterpart (a failed
speculation attempt with budget remaining), `resolver-exhausted` only fires
at the actual exhaustion transition — mirroring `gate-failed` (retrying) vs
`repairer-exhausted` (exhausted, repairer configured). `environment-degraded`
is instead genuinely dual-purpose end to end: an environmental failure is
the same failure mode whether the wall-clock bound has been reached yet or
not, so the code does not change, only the phase (`retry_wait` →
`needs_human`) does.

`needs_human` is a status distinct from `needs_input`: it is the queue's own
declaration that automation exhausted every option for this candidate — a
broken harness, an exhausted bounded attempt budget (`repairer-exhausted` when
a `Repairer` was configured and still couldn't turn the gate green,
`resolver-exhausted` for a preparation that never got there, `gate-failed` /
`finalization-failed` when there was nothing more specific to say, or the
generic `budget-exhausted` fallback), or an environment that stayed degraded
past its wall-clock bound (`environment-degraded`). `needs_input` remains
exactly what an operator's own `queue park` verb produces
(`operator-parked`), independent of whatever free-text reason they typed.
Operationally the two are identical: neither is ever auto-picked up by the
worker loop (`parked()`), both are visible in `queue status`'s phase and
reason-code roll-ups, and both are returned to `queued` with a fresh attempt
budget by the same `resume` verb. `needs_human` additionally carries a
`needs_human_evidence_ref`, set by `parkHuman` to the first non-empty of, in
order: the gate log path, the finalization log path, the candidate's
workspace path, then finally the candidate's own ID — so a human or medic
has something concrete to open without re-deriving it from `evidence`.

Resubmitting the same SHA (e.g. with a fresh CI receipt) supersedes the prior
active candidate and inherits its attempt count, so bounded retries cannot be
reset by resubmission races.

### BREAKING CHANGE for downstream consumers of the wire status (bump-gated)

Before this change, every automation park (harness failure, exhausted
attempt budget, environment stuck past its wall-clock bound) emitted
`status`/`phase` = `needs_input` on the wire (`kitsoki queue status --json`,
`.capsules/queue/state.json`). As of this change those same three cases emit
`needs_human` instead; only an operator's own `queue park` verb still
produces `needs_input`. This is a deliberate, intentional split — it is the
entire point of the `needs_human` terminal state — but it is **not**
backward compatible for any consumer that pattern-matches the literal string
`needs_input` (or a hardcoded status union/allow-list) expecting it to cover
every automation park.

This engine repo has no such consumer. POG (the known downstream, pinned via
`kitsoki.lock`) does, as of this writing.

**A registered POG CI gate goes red, not merely quiet.** This one is not a
degraded message, so it is listed first and separately:

- `scripts/test-queue-core-integration.sh:196` asserts
  `.candidates[0].phase == "needs_input" and .candidates[0].attempt == 2 and
  .candidates[0].retry_reason == "max_attempts_exhausted"` against
  `.capsules/queue/state.json` after driving max-attempt exhaustion. The
  writer of that park is THIS engine: the test's `worker_once` helper runs
  `scripts/process-promotion-queue.sh` with `POG_KITSOKI_BIN`, and that
  script is a thin wrapper that execs `<kitsoki> queue worker`, with no
  POG-native fallback path. Since `retryOrPark`'s exhaustion path now parks
  as `needs_human`, the assertion hard-fails with `max-attempt exhaustion did
  not park the candidate`. This gate is registered in
  `scripts/kitsoki-ci.mjs` as `queue-core-integration`, so the failure is a
  red required check, not a local-only surprise. (The adjacent
  `retry_wait`/`gate_failed` assertion at line 188 is unaffected and still
  holds.)

Production consumers that degrade rather than fail:

- `scripts/promotion-status.sh` — its parked-candidate branch does not
  recognize `needs_human` and falls through to a generic message instead of
  the explicit "operator action required: resume/override/reject" hint.
- `scripts/feedback-autonomous-dispatch.sh` — its dispatcher match arm is
  `retry_wait|needs_input`; a `needs_human` candidate will not match and the
  dispatcher loops instead of taking its fast, explicit exit.
- `scripts/pog-prune-capsule-workspaces.sh` — its `LIVE_PHASES` allow-list
  does not include `needs_human`, so a needs_human candidate's continuation
  tree (`.capsules/sync/cont-*`) — exactly the evidence a human needs to act
  — becomes prunable.
- `portal/src/server/stream-proposal-rpc.ts` — `needs_human` is not in its
  status union, so it is coerced to `queued` with a misleading
  "the Kitsoki worker owns the next queue phase" next-action string.
- `portal/src/data/streams.ts` — routes a parked stream back to the portal
  Inbox only on `needs_input`; a `needs_human` stream will not route there.

Finally, three POG tests hand-seed a `needs_input` fixture *as* the
automation-park case. They keep passing (they never invoke the engine to
produce the park), but they silently stop covering what they were written to
cover, so they belong in the same change rather than being discovered later:
`scripts/test-promotion-status.sh:54`,
`scripts/test-pog-prune-capsule-workspaces.sh:166`, and
`scripts/test-runner-session-reaper.sh:824`. (`scripts/test-promotion.sh:350`
also asserts a `needs_input` automation park, but it is not registered in
`scripts/kitsoki-ci.mjs` and its assertions — `.attempts`, a POG-only overlay
the engine does not write, and `retry_reason == "promotion_failed"` — already
describe a pre-engine POG-native path, so it is stale independently of this
change and is deliberately excluded from the list above.)

**This is a blocking follow-up, not an oversight to be worked around here.**
POG's `kitsoki.lock` must not be bumped past the commit that introduces
`needs_human` until the CI-gate assertion, the five production consumers, and
(ideally in the same change) the three fixtures above are updated to treat
`needs_human` as parked/human-actionable alongside `needs_input`. Nothing is
broken today because POG still pins an engine revision that predates this
change; this note exists so that stays true only until someone deliberately
fixes the POG-side consumers, not by accident. Anyone acting on this list
should re-grep POG for the literal `needs_input` before bumping rather than
trusting this enumeration to have stayed current.

## The medic: bounded productive retries on stalled parked candidates

Before this item, nothing watched `needs_conflict_input` (candidates could
sit parked indefinitely even though the configured or embedded git-ops
resolver might well succeed on a second try — a flaky launch, a transient
story-harness hiccup, an operator having just fixed the resolver
configuration) or a repeated gate-failed `retry_wait` streak (a configured
`Repairer` only ever gets its shot after the full exponential backoff, never
sooner). `queue.Store.MedicRunOnce` (`internal/capsule/queue/medic.go`)
closes both gaps using only existing queue mechanics — it never calls
`queue override`, never waives a gate, never skips a test:

- **`needs_conflict_input`** → dispatch the resolver again by returning the
  candidate to `queued`; the ordinary worker's next `prepare()` re-runs
  `Speculate`, which re-drives `resolveConflicts` with whatever resolver is
  configured. The medic itself never launches an agent or runs git.
- **`retry_wait` with a repeated gate-failed streak, only when a `Repairer`
  is actually configured for this worker** → clear the backoff timer early
  (exactly what `kick` does), once per attempt, so the repairer gets its
  shot sooner. With no repairer configured the medic leaves these alone
  entirely: kicking would just burn the ordinary attempt budget faster for
  no benefit.

Both cases carry their own bounded productive-retry budget, tracked durably
on the candidate (`medic_dispatches` / `medic_first_dispatch_at` /
`medic_kicked_attempt`) and separate from the ordinary `attempt` budget: a
wall-clock deadline (checked first, always escalates with the generic
`budget-exhausted` — "ran out of time" is a different claim from "kept
failing") and a dispatch-count ceiling (escalates with the specific
`resolver-exhausted` or `repairer-exhausted`). Exhaustion always lands on
`needs_human` with a `needs_human_evidence_ref`, exactly like `parkHuman`.
`resume`/`override` reset this budget too — a human declaring the underlying
cause fixed gets the medic a fresh budget, not a mid-exhaustion one.

A third, reaper arm covers the case where the medic *has* dispatched a
candidate back to `queued`/`reprepare` but nothing ever re-drives it into
`needs_conflict_input`/`retry_wait` again before the deadline — no worker
running for this target, the worker down, or a standalone `queue medic`
running with no worker process at all. Without this arm such a candidate
would sit forever with its medic budget already spent and nothing left in
the scan able to revisit it (the switch only matches
`needs_conflict_input`/`retry_wait`). This is what makes "every candidate the
medic touches ends up progressing or in `needs_human`" hold unconditionally,
not just in the common case of a live worker claiming its own dispatch
promptly.

**Caveat on `--medic-max-dispatches` vs `--max-attempts`:** the gate-retry arm
can escalate a candidate to `repairer-exhausted` while it still has ordinary
attempts left under `--max-attempts` — e.g. `--medic-max-dispatches 3` with
`--max-attempts 10` escalates around attempt 5 (threshold 2 + 3 dispatches)
even though `retryOrPark` would have kept retrying through attempt 10. The
direction is conservative (nothing lands unverified), but it does mean a
worker configured with a much larger `--max-attempts` than
`--medic-max-dispatches` will see the medic reduce automation throughput
rather than increase it for that population; size the two together.

`needs_input` and `needs_human` are never touched. `needs_input` is exactly
what an operator's own `queue park` verb produces; acting on it would mean
silently overriding a human's explicit decision to leave a candidate alone.
`needs_human` is the terminal that says automation is already out of
options — the medic is part of "automation" for this purpose and must not
re-litigate its own prior verdict outside the one exhaustion transition
above. **This means `--medic` alone does not clear a `needs_input` backlog**:
if most of a deployment's stalled population is operator-parked
(`reason_code=operator_parked`) rather than `needs_conflict_input` or a
repeated gate-failure streak, check `queue status`'s phase counts before
assuming `--medic` will drain it — those candidates need a human `resume`,
by design.

A dispatched candidate's queue `position` is also reset to the back of the
line (`nextPosition`, exactly what an ordinary `retryOrPark` failure already
does), not left at whatever position it held when it parked. Finalization
picks its head by position, so an un-parked known-stalled candidate that kept
its old (often earliest) position would re-occupy the finalization head and
block every healthy candidate behind it for up to
`--medic-max-dispatches` full prepare cycles; resetting position means a
medic-dispatched candidate can only ever delay itself, never the train.

Every medic action is one lock-protected read-mutate-write, exactly like
`Worker.claimPreparation`'s own lease-expiry sweep: there is no separate
medic lease to fence, because a candidate a prior (or concurrent, or
pre-restart) pass already moved out of `needs_conflict_input`/`retry_wait`
is simply not matched by the next pass. A worker restart between two medic
passes cannot double-dispatch — the durable write is atomic, so a crash
before it lands leaves the candidate exactly as if the pass never ran. This
also serializes genuinely concurrent callers (two medic-enabled workers, or a
worker plus a standalone `queue medic`, racing on the same `state.lock`) down
to exactly one dispatch, not just sequential restarts.

**Multi-target stores:** a store commonly holds candidates for more than one
protected target at once. `MedicDeps.TargetRef` scopes every medic action
exactly like `Worker.Deps.TargetRef` scopes `claimPreparation`/`finalize`/
`update` — a worker (or standalone medic) bound to one target has no
authority to un-park, kick, or escalate a candidate bound to a different
target, and must set `--target` to match. Left unset, the medic matches
every candidate regardless of target, which is only safe for a
single-target-per-store deployment or a deliberately unscoped standalone
pass.

`queue status` renders the medic's last action per candidate
(`medic_last=<verb>@<time> medic_dispatches=<n>`, appended the same
append-never-insert way `reason_code=`/`needs_human_evidence=` are) and a
`medic_actions[...]` roll-up in the summary line, alongside `reason_codes[...]`.

### Enabling the medic (what POG must set)

Two equivalent surfaces, both wrapping `queue.Store.MedicRunOnce`:

- **`kitsoki queue worker --medic`** runs the medic pass inside the same
  process (`runQueueWorkerLoop`'s `medic` parameter). This is the intended
  production wiring: POG's worker invocation
  (`scripts/kitsoki-queue-worker.sh` → `kitsoki queue worker`) should add
  `--medic` alongside its existing `--repair` (when configured — see below)
  and `--resolver` flags. It runs on the very first outer iteration and
  thereafter at most once every `defaultMedicInterval` (5s) — not on every
  claim/prepare/finalize tick, which can be as fast as every 250ms while the
  train is progressing — so a busy worker does not pay for a second full
  `state.lock` acquisition and `state.json` decode several times a second;
  `--once` always still gets exactly one pass. Under `--concurrency > 1` only
  the base (`-1`-suffixed) loop runs the medic at all; the others do not
  duplicate the scan.
- **`kitsoki queue medic`** is the identical pass as a standalone,
  independently-scheduled process (`--once` for a single pass, otherwise a
  1s-polling loop), for an operator who runs the worker without `--medic`
  or wants the medic on its own cadence.

Flags (both surfaces; the standalone command omits the `medic-` prefix):

| `queue worker` flag                  | `queue medic` flag       | Default | Meaning |
| ------------------------------------- | ------------------------ | ------- | ------- |
| `--medic`                             | *(always on)*             | off     | enable the medic pass |
| `--target` *(reused from the worker's own flag)* | `--target`     | unscoped | scope every medic action to this protected target ref — **required for a multi-target store**, must match the corresponding `queue worker --target` exactly |
| `--medic-max-dispatches`               | `--max-dispatches`        | 3       | productive retries per candidate before escalating |
| `--medic-deadline`                     | `--deadline`              | 2h      | wall-clock bound since a candidate's first medic touch |
| `--medic-gate-failure-threshold`       | `--gate-failure-threshold`| 2       | consecutive gate-failed attempts before a `retry_wait` candidate is medic-actionable |
| *(derived from `--repair`)*            | `--repairer-configured`   | false   | whether this worker actually has a repairer configured; the standalone command cannot see the worker's own `--repair`, so it must be told explicitly |

`queue worker --medic` derives both `RepairerConfigured` (from whether
`--repair` is non-empty) and `TargetRef` (from the worker's own, already
normalized `--target`) automatically — POG does not need to pass either
separately for the embedded form. The standalone `queue medic` process has
no visibility into a separate worker process's flags, so if POG runs the
medic standalone against a worker that has `--repair` and/or `--target`
configured, it must pass `--repairer-configured` and the exact same
`--target` itself — otherwise the medic correctly, silently, either leaves
repeated-gate-failure candidates alone (the safe default for a missing
`--repairer-configured`) or acts across every target in the store (the
default for an unset `--target`, safe only for a single-target-per-store
deployment).

## External worker results: verify privately, then admit

`kitsoki queue submit-external` admits source produced by a disposable worker
without requiring its commit to exist in the controller project repository.
The strict `capsule-external-worker-result/v1` record binds execution, CI job,
train, manifest, branch, candidate and base SHAs, target, receipt, and bundle
digest/size/object key. The caller downloads the named object and supplies the
local bundle path; object-store credentials and URLs never enter queue state.

Admission copies a regular non-symlink bounded file into queue-private staging,
checks its SHA-256, runs `git bundle verify`, requires exactly the declared
advertised head, imports into `.capsules/queue/external-objects.git`, and proves
the declared base is an ancestor of the candidate. Only then does it publish
an immutable `refs/kitsoki/external-results/<anchor>` ref and anchor record.
Queue submission must consume that anchor, so an interrupted or partial import
cannot become a candidate. Replaying the exact result returns the same anchor.
A different result cannot replace it.

The protected project object database and worktree remain untouched during
admission. During ordinary preparation, the queue fetches the anchored commit
only into the managed speculative workspace; the existing receipt gate,
target binding, deterministic gate, and protected compare-and-swap finalizer
remain unchanged.

### Authenticated remote admission

`kitsoki queue serve-admission` is the network edge for a disposable worker
that cannot and must not call the controller-local `submit-external` command.
The exact hosted configuration, filesystem layout, wire request, and worker
retry matrix are in the
[remote queue-admission runbook](../runbooks/queue-admission-service.md).
It accepts `POST /v1/queue/admissions` with:

- schema `capsule-queue-remote-admission-request/v1`;
- one sealed `pog/integration-train-worker-admission-handoff/v1`;
- the exact Capsule CI receipt bytes as `receipt_base64`; and
- target/finalization policy bound into the admission fingerprint.

The handoff names the worker bundle by its exact object-store key, digest, and
size. The service reads it through `internal/objectstore` (Spaces in
production, the in-memory fake in tests), repeats receipt/result/handoff
digest validation, streams the bundle into bounded private temporary storage,
and then invokes the same Git head/base/lineage verifier as local external
admission. Object credentials and URLs never enter the request or queue state.

The service requires a bearer token. Authentication runs before its bounded
concurrency admission: bad or missing credentials are always HTTP `401`
(`unauthorized`), while an authenticated request that exceeds capacity is
HTTP `429` (`rate_limited`) with `Retry-After`. Plaintext listeners are
loopback-only, intended for an SSH tunnel; a non-loopback listener requires
`--tls-cert` and `--tls-key`.

`--root` is a service-owned durable authority outside the protected project.
Receipts, replay intents, immutable responses, queue state, and
`external-objects.git` all live below it. Run queue readers/workers against
the exact `<root>/queue` with `--queue-root`; preparation is the first phase
allowed to fetch the anchored candidate into a disposable project workspace.
Admission itself leaves the project's refs, worktree, and Git object database
unchanged.

Each execution ID is durably bound to one normalized submission digest before
the remote bundle is fetched. Exact replay returns the original admission,
anchor, and candidate IDs. Any changed handoff, receipt, policy, or path under
that execution is rejected as a substitution. Pending intent, queue import,
record publication, and completion are restart-replayable, so a crash may
leave private unreachable objects but can never publish a partial candidate
or silently reinterpret a replay.

## Divergence: disjoint continues, conflicts get resolved

When a candidate has diverged from the protected target, the reconciler
materializes a conflict artifact and an integration instance:

- **Disjoint changes just continue.** A clean merge (no conflicted paths) is
  committed automatically and the train proceeds
  (`queue:disjoint-histories-merged-automatically` in evidence).
- **Conflicts are resolved, not merely quarantined.** The queue first replays
  recorded resolutions (`git rerere`), then drives a kitsoki git-ops
  `conflict_resolver` agent against the integration instance in CodeAct mode.
  The app.yaml is resolved in two tiers, project-local always winning: the
  project's own `stories/git-ops/app.yaml`, falling back — only when that is
  absent — to the git-ops story in the embedded kitsoki story library
  (`internal/basestories`, the same `@kitsoki/<name>` mechanism
  `internal/capsule/storylauncher` uses). This gives a project that ships no
  git-ops story of its own a working automatic resolver anyway. The agent is
  write-fenced (Read/Edit, no git); the queue performs every git operation —
  staging, marker verification, rerere recording, and the merge commit. A
  project-supplied `ResolverCommand` overrides the git-ops launch for
  deterministic policies.
- **Outcomes are strictly classified.** Resolved → the train continues.
  Conflicts remain (or neither a project-local nor an embedded git-ops story
  exists — `queue:git-ops-resolver-unavailable` in evidence, with the
  specific reason spelled out rather than a bare tag) → `needs_conflict_input`,
  with the integration instance and continuation retained on disk. Launch
  harness broken (including a broken embedded-fallback mechanism itself) →
  `needs_human` immediately.

## WIP preservation: the protected checkout never blocks and never loses data

Before the protected finalization CAS, a dirty protected checkout is captured
onto an immutable `queue/preserved-wip/<stamp>` branch: staged, unstaged,
untracked, and deletions together via a temporary index, with a
byte-completeness proof (re-capture must produce the identical tree) before a
single byte is removed. Control surfaces (`.capsules`, `.artifacts`,
`.context`, `.worktrees`, `.kitsoki`) are never captured or cleaned. After the
CAS, the checkout is hard-synced to the new tip only if its worktree still
exactly matches the pre-CAS tip.

## Operator verbs (the human-override surface)

All verbs validate phase, run under the durable state lock, and append an
audited evidence line (actor, timestamp, reason). Worker results never clobber
an operator decision: an in-flight result landing on a parked or rejected
candidate is discarded and the discard is evidenced.

The `state.lock` path is a stable inode guarded by an OS-owned exclusive file
lock. Its presence is not ownership: the kernel releases ownership when the
holder exits or is killed, so a crash or service restart cannot strand the
queue behind an orphaned lock file. A concurrent live holder still produces
the typed `queue.ErrBusy` result within the configured lock wait.

| Verb        | From                                   | Effect |
| ----------- | -------------------------------------- | ------ |
| `kick`      | `retry_wait`                           | clears `retry_at`; attempts untouched |
| `park`      | any non-terminal (incl. `needs_human`) | `needs_input` + `operator-parked`; stops delaying the train. Parking a candidate automation had already parked as `needs_human` fully *replaces* that park: `needs_human_evidence_ref` is cleared with it, so a `needs_input` record never advertises an automation evidence pointer it no longer describes (the prior park stays in `evidence`) |
| `resume`    | parked / `retry_wait`                  | back to `queued`, fresh attempt budget |
| `emergency` | any non-terminal                       | priority lane, FIFO within itself |
| `override`  | any non-terminal                       | **human immediate merge**: emergency priority + durable attributed gate waiver (`operator-override/v1`); integration tree and protected CAS still apply |
| `reject`    | any non-terminal                       | terminal; branches, workspaces, evidence retained |

## Surfaces

- **CLI**: `kitsoki queue kick|park|resume|emergency|override|reject <id>
  [--actor --reason --project]`, plus `submit`, `submit-external`,
  `serve-admission`, `status`, `worker`
  (`--retry-delay`, `--max-retry-delay`, `--max-attempts`,
  `--env-retry-delay`, `--max-env-duration`, `--max-env-repeat`, `--medic`
  and its `--medic-*` flags — see "The medic" above), and the standalone
  `medic` command for the same pass as an independently-scheduled process.
- **JSON-RPC** (runstatus server): `queue.status`, `queue.kick`, `queue.park`,
  `queue.resume`, `queue.emergency`, `queue.override`, `queue.reject` with
  params `{id, actor, reason, project?}` — the portal-UI surface.
- **MCP** (studio server): the same family as `queue.*` tools.
- **Starlark host**: `ctx.host.call("host.queue.<verb>", {...})`, deny-by-
  default allow-listed like every host verb.

## Failure classification

A candidate's failure is one of three classes, and the class decides the
policy. Adapters classify their own errors at the exact operation that failed;
the worker never string-matches an error to guess.

| Class | Produced by | Policy |
| --- | --- | --- |
| **Product** (plain error) | a gate that ran and reported red | exponential backoff, consumes `--max-attempts`, parks as `needs_input` when exhausted |
| **Environmental** (`Environmental`) | transient infrastructure: a target not yet fetched, lock contention, a workspace-create race, a lost remote transport | fixed `--env-retry-delay`, keeps queue position, does **not** consume the attempt budget, parks once degraded past `--max-env-duration` **or** once `--max-env-repeat` consecutive attempts report the byte-identical failure message |
| **Environmental / immediate** (`EnvironmentalImmediate`) | the gate produced **no verdict** and waiting cannot change that: a missing toolchain, a gate killed or starved mid-run | parks at once as `needs_input`, rolls the attempt back, `retry_reason` carries the named cause |
| **Harness** (`Harness`) | the gate is misconfigured (unknown executor/pipeline) | parks at once |

Only a product failure may ever consume the attempt budget, because only a
product failure is a verdict on the candidate's code. No classification can
land red code: a failing gate keeps `Passed=false` in every branch.

`ShellGate` reads its class from the gate process's **exit status**, which is a
shell gate's only way to declare one:

| Exit status | Class | Cause token |
| --- | --- | --- |
| `75` (`GateEnvExitCode`, EX_TEMPFAIL) | immediate environmental | `gate_declared_environment_unusable` |
| `126` / `127` | immediate environmental | `command_not_executable` / `command_not_found` |
| killed by a signal, or `128+N` | immediate environmental | `killed_signal_<N>` |
| any other nonzero | product | — |

So a gate script should preflight the tools it needs and `exit 75` with a named
message when one is absent, rather than letting `command not found` surface
hundreds of seconds deep inside a suite where it is indistinguishable from a
red test. `queue status` then shows the class and cause directly
(`retry_reason=gate_no_verdict_killed_signal_9`,
`gate_evidence` carrying `queue:gate:exit=137 class=environment cause=…`), and
`env_retries` / `first_env_failure_at` are populated instead of left at the zero
value.

### Environmental repetition: a second identical failure parks, it does not wait out the clock

`--max-env-duration`'s wall-clock bound (default 2h) exists so a candidate
stuck in a persistently degraded environment eventually parks. It does not by
itself bound *how many times* the worker rediscovers the same answer before
that clock runs out. POG candidate `queue-58a9285a62d8` retried 172 times over
2 hours: its `ExecutorGate` dispatch to the `vm-pool` executor could not even
lease a worker — `service.Run` returned `capsule ci: pool executor "vm-pool":
lease worker: vmpool: lease: acquire: vmpool: resolve worker image: vmpool:
stat worker image pointer parent /private/etc/kitsoki: no such file or
directory` — and `ExecutorGate.Run` wraps exactly that unmodified message as
`Environmental(runErr)` (`gate_evidence` shows the resulting no-verdict
dispatch as `outcome=unknown`, `ExecutorGate`'s display default for an empty
verdict, not a value the pipeline itself reported). The underlying cause is a
static local misconfiguration — a missing filesystem path — so the message
was identical on every attempt; the queue's durable state only retains the
*most recent* failure text, not a full per-attempt history, but the message's
total absence of per-attempt variables (no job/execution ID, no timestamp) is
consistent with byte-for-byte repetition across all 172. None of those
attempts could have produced a different answer; the wall-clock bound alone
let it burn the full window rediscovering that.

Retrying is only useful when the situation might have changed. An
environmental failure whose message is **byte-identical** to the one
immediately before it is not evidence of transience, it is evidence of a
stuck state. So `retryOrParkEnv` tracks a second, independent bound on the
candidate — `env_failure_signature` (the most recent environmental failure's
exact message) and `env_repeat_streak` (how many consecutive attempts,
including the current one, matched it) — and parks as `needs_input` with
`retry_reason` suffixed `_repeated_outcome` once `--max-env-repeat`
(default **2**: the first failure plus one retry that reproduces it exactly)
consecutive attempts match. This bound is checked before the wall-clock bound,
so it typically trips first and turns what would have been a multi-hour
rediscovery into two fast attempts.

A **different** message each attempt — a different lock holder, a different
transient network symptom — resets `env_repeat_streak` to 1 and keeps the full
`--max-env-duration`-bounded leniency; only exact sameness is the signal, not
the mere presence of an environmental classification like `outcome=unknown`.
Comparison is intentionally exact (no fuzzy/prefix matching): a summary that
varies for a real reason must not be conflated with one that is genuinely
unchanged. `env_failure_signature` / `env_repeat_streak` reset alongside
`env_retries` / `first_env_failure_at` on a clean preparation, a landed
finalization, `resume`, and `override`, so a resolved streak never biases a
later, unrelated environmental failure's bound.

## Testing

`internal/capsule/queue` covers the semantics with unit fakes and real-git
end-to-end tests: backoff and max-attempts parking, kick/park/resume/override,
emergency ordering, harness classification, environmental classification
(`shell_gate_class_test.go` drives the real `ShellGate` against real
subprocesses, so the exit statuses under test are the real ones — a fake that
always resolved its tools would prove nothing), environmental repeat-streak
parking versus genuine transience
(`TestWltFailGateEnvErrorParksAfterRepeatedIdenticalOutcome`,
`TestWltGateEnvErrorParksAfterRepeatedIdenticalOutcomeThroughFullRunOnceFlow`,
and the differing-message negative case, in `worker_lifecycle_test.go`),
parked-head non-blocking, attempt inheritance across resubmission, WIP
byte-completeness (including control-state survival), disjoint auto-merge,
conflict resolution via an injected resolver runner (no LLM in automated
tests), conflict retention without a resolver, and concurrent workers +
operator traffic under `-race`.
The medic (`medic_test.go`) is covered separately: exactly-once dispatch on
a stalled `needs_conflict_input` candidate (real conflicting repo, fake
resolver command), a successful retry after dispatch actually landing,
wall-clock-deadline vs dispatch-count exhaustion producing the distinct
`budget-exhausted` vs `resolver-exhausted`/`repairer-exhausted` codes,
`needs_human`/`needs_input` immunity, `resume`/`override` resetting the
medic budget, and a simulated worker restart (two independent `Store`
values against the same directory) never double-dispatching.
