# Capsule merge queue: retry policy, human overrides, and conflict resolution

The capsule merge queue (`internal/capsule/queue`) serializes receipt-bound
candidates onto a protected ref through speculation → deterministic gate →
compare-and-swap finalization. This document covers the operational semantics
productized from the POG shell-script extension of the queue: bounded retries,
the emergency lane, human override verbs, WIP preservation, and automatic
conflict resolution. The design goal is a single invariant:

> **The queue can never reach a state a human cannot exit, and it never loses
> data.** Every non-terminal candidate is either making progress, waiting on a
> durable timer clearable by `kick`, or parked in `needs_input` with an
> audited evidence trail and `resume` / `override` / `reject` available.

## Ordering and the never-stuck train

Candidates carry an immutable admission `sequence` and a durable `position`.
Claiming and finalization order is: the **emergency lane** first (FIFO within
itself, by `emergency_sequence`), then `position`. A candidate that fails its
gate or speculation is requeued **to the back of the line** in `retry_wait`
with a durable `retry_at` under exponential backoff (`RetryDelay · 2^(n−1)`,
capped at `MaxRetryDelay`; defaults 5m/30m). After `MaxAttempts` (default 5)
it parks as `needs_input` instead of spinning. A failing candidate therefore
delays only itself; the finalization head skips parked and timer-waiting
candidates, and the protected base CAS remains the correctness guard.

Failures of the queue's own machinery — a resolver or gate harness that could
not launch — are classified separately (`queue.Harness(err)`) and park the
candidate immediately as `needs_input` without burning bounded retry attempts.

Resubmitting the same SHA (e.g. with a fresh CI receipt) supersedes the prior
active candidate and inherits its attempt count, so bounded retries cannot be
reset by resubmission races.

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
  `needs_input` immediately.

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
| `park`      | any non-terminal                       | `needs_input`; stops delaying the train |
| `resume`    | parked / `retry_wait`                  | back to `queued`, fresh attempt budget |
| `emergency` | any non-terminal                       | priority lane, FIFO within itself |
| `override`  | any non-terminal                       | **human immediate merge**: emergency priority + durable attributed gate waiver (`operator-override/v1`); integration tree and protected CAS still apply |
| `reject`    | any non-terminal                       | terminal; branches, workspaces, evidence retained |

## Surfaces

- **CLI**: `kitsoki queue kick|park|resume|emergency|override|reject <id>
  [--actor --reason --project]`, plus `submit`, `submit-external`,
  `serve-admission`, `status`, `worker`
  (`--retry-delay`, `--max-retry-delay`, `--max-attempts`,
  `--env-retry-delay`, `--max-env-duration`, `--max-env-repeat`).
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
