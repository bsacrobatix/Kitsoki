# Durable delivery

This Story is the management spine for repair followed by independent review.
The controller enqueues `delivery-fixer`, waits for its terminal durable
receipt, and only then enqueues `delivery-reviewer`. Worker entry intents claim
one fenced job, invoke distinct `host.agent.task` personas, and write the
terminal receipt through `host.work_queue_worker`.

Deployment owns both queues, worker identities, concurrency, bundle root, and
the target ladder. Use `target_mode: staging-only` to stop after the reviewed
staging result. A longer policy returns `next_stage_ready`; automatic chaining
is intentionally not claimed yet because the current retained-bundle promoter
does not expose an idempotent operation that re-enqueues the exact already
landed tree and proof tuple for a different target. Adding that operation is
the remaining gap for automatic staging -> main -> deploy progression.

## Exact live wiring boundary

There are two current executor paths and they must not be conflated:

- A generic `work_queue_workers` binding runs the worker rooms in this Story.
  `complete_fixer` calls `SQLStore.Complete`, which validates and retains the
  bundle for a code-producing queue. It does **not** admit that bundle to the
  Capsule merge queue.
- A `work_queue_executors` binding runs `workqueue.CapsuleExecutor`. On a
  passed code job it calls `CapsulePromotionSink.Promote` before fenced
  completion; `capsuleQueuePromoter` then calls
  `queue.Store.AdmitExternalBundle`. This is the only built-in automatic
  workqueue-to-merge-queue path today.

Those paths do not yet compose in the required order. The direct Capsule
executor admits before this controller observes fixer success and enqueues the
reviewer, while the generic Story worker can review first but has no promotion
hook. Therefore these flows prove the durable fixer -> reviewer management
spine, but do not claim end-to-end automatic landing. The missing adapter must
hold the exact retained bundle after fixer completion, require a successful
reviewer receipt from a distinct configured worker identity, then invoke the
existing idempotent promoter.

The Story declares distinct `fixer` and `reviewer` agent personas and the
reviewer has no write-capable tools. Kitsoki's merge-queue repair path also
enforces `repairer_id != reviewer_id`. The generic workqueue configuration does
not yet express an `independent_from` constraint between two worker bindings,
so deployment-level distinct worker identity for these Story jobs is another
explicit gap rather than an inferred guarantee.

Finally, host-wide `FileGateCapacity` currently wraps queue-worker gates. A
direct implementation-agent invocation of `make test` does not acquire that
token, and wrapping a gate naively would deadlock when it is nested under a
queue worker already holding the slot. A built-in re-entrant
`kitsoki delivery gate -- <command>` (with an inherited held-token identity) is
still required before direct local test invocations can truthfully be called
contention-controlled.

All checked-in flows use fake host handlers. They never call an LLM, network,
or live worker.
