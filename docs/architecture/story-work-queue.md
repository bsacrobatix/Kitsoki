# Story work queue

`internal/workqueue` is Kitsoki's canonical durable broker for application
work that must survive the lifetime of a story turn or one daemon process. It
is a generic platform service: a Story Application describes the meaning of
the work and the configured worker adapter performs it, while the queue owns
idempotency, claim, retry, and terminal truth. It is not a POG queue and it
does not execute arbitrary commands.

This document defines the intended contract for every application that needs
durable queued work. It deliberately does not replace the Capsule merge queue,
which has a different authority and safety contract.

## Ownership

| Surface | Owns | Does not own |
|---|---|---|
| Story Application | Domain eligibility, payload schema, priority policy, completion interpretation, and operator-facing meaning | Queue polling, worker liveness inference, retry timers, or direct database access |
| `host.work_queue` | The application-scoped, bounded story-facing enqueue and inspection boundary | Worker endpoint selection, arbitrary process execution, or cross-application discovery |
| `internal/workqueue` | Immutable submission identity, durable state, atomic claims, leases and fencing, attempts/backoff, and terminal receipts | POG feedback policy, campaign cadence, Git promotion, or a product's definition of success |
| Worker adapter | Capability/capacity advertisement, execution of one configured queue, lease renewal, and fenced completion | Reinterpreting a payload, deciding countability, or mutating another worker's claim |
| Operator surface | Bounded inspection and explicit, authorized remediation requests | A second private queue ledger |

The queue key is application-scoped. A caller cannot choose another
application, filesystem root, worker endpoint, credential, process command,
or provider/profile through story input. Queue names and their payload schemas
are deployment-owned registrations, analogous to application-job templates.

## State And Receipts

A submission receives a durable `work_ref` under a unique application, queue,
and caller-supplied idempotency key. The normalized input and configured policy
are part of the immutable submission identity. Repeating the same submission
returns that record; reusing its key with different input or policy is rejected.

The durable lifecycle is:

```
queued -> leased -> succeeded | failed | cancelled
  ^           |
  +-----------+ retry or lease expiry (with a configured available-at time)
```

`leased` work is lease-bound, not owner-PID-bound. A successful
claim mints a monotonically increasing fencing token. Renew, start, progress,
complete, and fail operations must carry the current token; a stale worker is
rejected even when it later regains connectivity. Lease expiry makes the work
eligible for recovery according to its configured retry policy. A worker may
never turn an expired or superseded claim into a terminal result.

Every terminal transition writes a typed receipt, including the immutable work
identity, final status and reason code, attempt and fence facts, artifact
handles, and code-bundle evidence. Restart recovery records
what can be known: unexpired leased work retains its lease until expiry, and
expired work becomes eligible for the configured retry path. The service never
reports that a worker process resumed merely because its database row survived.

### Countable code work

Code-producing work is countable only when its terminal receipt contains a
durable recoverable bundle reference: for example, a patch, managed workspace
bundle, immutable commit plus retained bundle, or another configured portable
artifact. A bare worker report, session ID, or local checkout path is not
countable ship evidence. This shared receipt rule is intentionally usable by
the queue, a product drain, and scoreboard/read-model projections without
giving any of them independent authority to decide whether lost work shipped.
The daemon installs a file-backed `workqueue.BundleValidator` from
`work_queue_bundle_root`. A bundle reference is a relative path below that
daemon-owned root. The validator rejects traversal, symlinks, special or
oversized files, and digest mismatches before committing a countable terminal
receipt. Code-producing configuration and completion fail closed without that
durable root; a syntactically valid reference or digest is not proof of
retention.

In SQLite mode, the retained file under that root remains the canonical
evidence. In PostgreSQL mode, the root is an intake directory: after verifying
the file, the daemon imports its bytes into `workqueue.bundles` and rewrites
the terminal receipt to a canonical `workqueue-pg-bundle:` reference. Every
daemon sharing PostgreSQL can resolve that evidence without sharing a local
filesystem.

## Public Story Host

`host.work_queue` is daemon-only and is replaced only for a registered,
configured application. It follows the existing host operation convention:

```yaml
# .kitsoki.yaml
work_queues:
  pog-operations:
    triage-feedback:
      max_input_bytes: 65536
      max_attempts: 4
      required_capabilities: [agent, placement:workstation]
      priority: 10
      produces_code: true

# .kitsoki.local.yaml
work_queue_bundle_root: /var/lib/kitsoki/work-queue-bundles

workers:
  - id: pog-runner
    label: POG runner
    placement: workstation
    enabled: true
    capabilities:
      labels: [agent]

work_queue_workers:
  pog-worker:
    target_application: pog-operations
    story_path: /absolute/path/to/POG/stories/pog-worker/app.yaml
    allowed_queues: [triage-feedback]
    worker_id: pog-runner
    max_concurrent: 2
    lease_seconds: 60

# Direct daemon-owned Capsule CI executor. This is for an allowlisted
# pipeline such as a vm-pool bugfix workflow, not an Application Event.
work_queue_executors:
  pog-bugfix:
    target_application: pog-operations
    queue: triage-feedback
    project_root: /srv/pog
    workspace_id: pog-bugfix
    pipeline: bugfix
    worker_id: pog-vm-pool
    worker_policy: bugfix
    input_schema:
      issue_ref: {type: string, required: true}
    bundle_ref_output: bundle_ref
    bundle_digest_output: bundle_digest
    bundle_kind_output: bundle_kind
    max_concurrent: 1
    lease_seconds: 60
    poll_seconds: 5

# Story effect
- invoke: host.work_queue
  with:
    op: enqueue
    queue: triage-feedback
    idempotency_key: feedback:fb_123:v1
    input: {feedback_ref: fb_123}

- invoke: host.work_queue
  with:
    op: snapshot
    max_items: 50
    max_bytes: 65536

- invoke: host.work_queue
  with:
    op: get
    work_ref: wq_...
```

`enqueue` accepts only a registered `queue`, a bounded idempotency key,
and normalized, bounded JSON input. It returns a stable `work_ref`, normalized
status, replay indication, and a `kitsoki/work-queue-submission/v1` receipt.
`get` returns the same privacy-safe projection for one application-owned
reference. `snapshot` returns a strictly bounded, redacted application-scoped
projection suitable for an operator application. Neither result exposes
worker credentials, endpoint details, raw payloads, hidden application IDs,
or internal scheduler identities.

The worker Story Application receives `host.work_queue_worker` only when it has
an exact `work_queue_workers` binding. That binding fixes its target
application, allowed queues, worker identity, capacity, lease duration, and
server-owned registry capabilities. Worker story input can claim from an
allowed queue and carry the returned work reference and fence through
heartbeat, completion, or failure; it cannot override any of those authority
facts.

Cancellation and lease reaping are **not** story operations. They remain typed
internal service calls. This keeps story code declarative and prevents the host
from becoming a general remote-execution interface.

## Capsule CI Executors

`work_queue_executors` binds one queue directly to an allowlisted managed
Capsule workspace and Capsule-CI pipeline. The daemon validates the bounded
object payload against `input_schema`, seals it as CI `job_inputs`, pins the
configured worker placement, and dispatches only through Capsule CI. It does
not start a Story Application event, run a shell dispatcher, or let the payload
choose a project, source revision, workspace, pipeline, provider, or output.

The daemon persists the queue work reference to the remote CI run and execution
identities before polling. After a restart, a newly leased worker reads that
mapping and asks the durable executor for status; it never assumes an old
process resumed. For code work, the configured output names are daemon-owned
projection keys, not Story claims: after a passed terminal run, the daemon
reads only `runs/<execution-id>/wip/refs.bundle` from that pool's durable
output bucket, hashes the observed bytes, stages them under the configured
work-queue intake root, and overwrites those verdict keys. The fenced queue
completion then runs the normal bundle validator, including PostgreSQL shared
retention, before the receipt is countable. A missing WIP object, unsafe
execution identity or bytes, invalid payload, and unknown durable status all
fail closed; a Story-provided ref or digest is never trusted.

## Adjacent Runtime Services

The queue composes with, but does not absorb, existing services:

| Service | Boundary with the work queue |
|---|---|
| `campaign` | Campaigns provide cadence, UTC-day tick budgets, and exact story-intent dispatch. A campaign may enqueue work, but campaign schedule state is not a work queue and a queue must not implement its own recurring poller. |
| `applicationjob` | Application jobs dispatch one configured background event and expose its artifact-job reference. Use them for bounded application events; use the work queue when independent workers need atomic claim, leased recovery, matching, and retry. An application job can be the executor behind a queue, not a competing durable ledger. |
| `artifactjob` | Artifact jobs are durable product-facing job identities and artifact/run projections. A queue receipt may reference an artifact job; it does not duplicate artifact indexing or visibility policy. |
| `workerregistry` | The registry declares stable worker identity, enabled state, placement, and advertised capabilities. The queue uses that declaration plus live capacity supplied by adapters to match work; it does not own worker configuration, secrets, or health transport. |
| Capsule merge queue | `internal/capsule/queue` serializes receipt-bound Git promotion into protected branches. It remains the only protected-Git promotion authority. The story work queue must never claim Git landing, mutate promotion candidates, or share its persistence/state machine. |

## Storage Semantics

SQLite is the single-host mode. Atomic claim and fence increment occur in one
database transaction, and all workers sharing that database are assumed to be
co-located under one daemon/operator authority. SQLite is not evidence of a
cross-host lease protocol; a process-local PID or `ps` observation cannot be
used to reap a remote worker.

PostgreSQL is the cross-host mode. Claiming eligible work uses a transaction
with row-level locking (`FOR UPDATE SKIP LOCKED` or an equivalent safe claim),
an atomic lease/fence update, and an application/queue/capability predicate.
Lease renewal and terminal writes compare the token in the same transaction.
The production backend must use one authoritative time source for eligibility
and lease expiry; callers must not be able to extend a lease by supplying a
clock value. PostgreSQL conformance tests must cover concurrent claim, stale completion,
lease recovery, idempotent submission, retry/backoff, and restart behavior.

Both backends preserve the same public receipts and state vocabulary. They may
not silently weaken cross-host semantics by falling back to a local lock.

## POG Migration

The migration is staged so Kitsoki supplies generic mechanics while POG keeps
its own product policy until it can be removed deliberately.

1. **Runner:** replace `colony-runner.mjs` schedule files, PID locks, and
   script execution with `campaigns:` plus exact story intents. Campaigns only
   create/trigger POG application work; they do not become a feedback broker.
2. **Drain:** move POG's durable dispatch, binding, retry, worker-capacity, and
   completion state to `internal/workqueue`. Keep feedback eligibility,
   prioritization, triage policy, and portal/read-model semantics in the POG
   Story Application.
3. **Supervisor:** consume queue and worker-registry projections through
   bounded host snapshots. Keep POG remediation policy as typed application
   work; do not revive a runner-private status file or a POG daemon watchdog.
4. **Reaper:** stop opening Kitsoki databases and inferring daemon ownership
   from local processes. Let queue lease/retry rules recover work and use
   application maintenance plus Capsule hygiene for sessions and workspaces.
   POG's global VM-fleet observation remains a POG-side requirement until
   Kitsoki provides that separate observation source.
5. **Cutover:** shadow old POG projections against queue receipts, verify that
   drain, portal, and scoreboard agree on terminal/countable work, then delete
   transitional ledgers and services. Do not import POG feedback policy,
   DigitalOcean assumptions, or script-command escape hatches into Kitsoki.

This sequence permits POG to migrate cleanly without making the queue's data
model or lifecycle specific to POG.
