# Provider request ledger

`provider-request-ledger/v1` is the append-only accounting boundary for a
stored agent trace. It emits one row for every `agent.call.complete` or
`agent.call.error` paired with its `agent.call.start`; retries are distinct
because they have distinct call IDs. Duplicate terminal evidence is rejected,
never merged or overwritten.

Each row binds run, attempt, stage, requested/resolved model identity,
monotonic start/end timestamps, token/cache buckets, decimal money fields, and
a SHA-256 digest of the paired evidence. Failed rows remain in the ledger even
when usage or billing is unavailable.

`friction/v1` is a separate trace-only sidecar. It reports observed tool,
schema, retry, and no-state-change counts with line-level evidence. Token
attribution and first-success timing are marked unavailable with a reason when
the trace cannot support them; unavailable never means zero.

The initial API is `internal/agentbench.BuildProviderRequestLedger` and
`AnalyzeFriction`. It is intentionally provider-free, so historical evidence
can be reprocessed without live spend.
