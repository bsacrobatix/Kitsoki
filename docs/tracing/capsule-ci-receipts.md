# Capsule CI receipts

A Capsule CI receipt is the portable evidence handoff for a run. Its canonical
schema is `capsule-ci-receipt/v1` and its `receipt_id` is the SHA-256 digest of
canonical JSON with integrity fields cleared.

The receipt joins the sealed execution envelope, a validated
`capsule-ci-verdict/v1`, artifact handles, and trace custody digest. It records
source, story, environment, executor policy, and verdict digests so promotion
cannot accidentally use evidence from another commit or environment.

`internal/capsule/trace` defines the shared `capsule-ci-trace/v1` document
schema and event-kind constants for workspace lifecycle, environment, CI, and
sync facts. Producers should use those constants instead of string literals so
receipt rebuilders, runstatus projections, and provider comments consume one
event vocabulary.

`internal/capsule/receipt` provides deterministic build and verify operations:

- missing job, envelope, environment, verdict, or trace facts yield
  `incomplete`, never an inferred pass;
- a changed envelope, verdict, artifact order, or trace digest invalidates the
  content hash;
- signing is injected behind a verifier seam and is required only when project
  policy demands it;
- `promotion_eligible` is accepted only when the verdict's matching digests and
  required evidence validate.

Projects require receipt signatures with checked-in CI policy:

```yaml
receipt:
  require_signature: true
  signer: test-signer
```

When enabled, receipt persistence signs with the injected signer and verifies
the signer name before writing a valid receipt. Promotion gates reload the same
project policy and reject unsigned receipts, missing signers, or signer-name
mismatches. Local projects can keep unsigned receipts by omitting the policy;
tests use `receipt.FakeSigner` for deterministic no-credential coverage.
`kitsoki capsule ci run` and `kitsoki capsule mcp` expose
`--fake-receipt-signer <id>` for local/test projects that intentionally require
signed receipts without introducing real key material.

CLI/MCP CI runs write a compact controller trace sidecar and a verified receipt
alongside their local run record; status includes receipt identity and
verification. `capsule ci status` and `capsule.ci.status` now expose a compact
`capsule-ci-run-index/v1` projection with job status, pipeline outcome,
promotion eligibility, receipt verification, digest bindings, and relative
trace/receipt sidecar paths. Runstatus/provider publication can consume that
projection on top of the same receipt schema; it must not introduce a second
evidence format.

`capsule ci summary` and `capsule.ci.summary` consume the run index into
`capsule-ci-provider-summary/v1`: aggregate counts, latest safe run rows, and a
Markdown body suitable for a future provider comment. The summary includes only
provider-safe fields from the index; live publication remains a separate
adapter.

Sync and promotion traces join the same evidence stream through
`capsule.sync.planned`, `capsule.sync.applied`, `capsule.sync.stale`, and
`capsule.sync.conflicted` facts. These events carry the plan digest,
operation/class, target ref, candidate commit, old/new target where relevant,
and the conflict continuation token when deterministic apply must pause for a
story resolver and independent lost-work review.

For a diverged stored plan, `kitsoki capsule sync conflicts --plan <digest>`
or the scoped `capsule.sync.conflicts` MCP tool materializes a
`capsule-sync-conflict/v1` artifact under `.capsules/sync/`. The artifact
records the merge base, candidate/target changed paths, overlap paths, required
resolver/reviewer/validation inputs, and the continuation token that story
traces must later reference.
