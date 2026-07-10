# Capsule CI receipts

A Capsule CI receipt is the portable evidence handoff for a run. Its canonical
schema is `capsule-ci-receipt/v1` and its `receipt_id` is the SHA-256 digest of
canonical JSON with integrity fields cleared.

The receipt joins the sealed execution envelope, a validated
`capsule-ci-verdict/v1`, artifact handles, and trace custody digest. It records
source, story, environment, executor policy, and verdict digests so promotion
cannot accidentally use evidence from another commit or environment.

`internal/capsule/receipt` provides deterministic build and verify operations:

- missing job, envelope, environment, verdict, or trace facts yield
  `incomplete`, never an inferred pass;
- a changed envelope, verdict, artifact order, or trace digest invalidates the
  content hash;
- signing is injected behind a verifier seam and is required only when project
  policy demands it;
- `promotion_eligible` is accepted only when the verdict's matching digests and
  required evidence validate.

CLI/MCP CI runs write a compact controller trace sidecar and a verified receipt
alongside their local run record; status includes receipt identity and
verification. `capsule ci status` and `capsule.ci.status` now expose a compact
`capsule-ci-run-index/v1` projection with job status, pipeline outcome,
promotion eligibility, receipt verification, digest bindings, and relative
trace/receipt sidecar paths. Runstatus/provider publication can consume that
projection on top of the same receipt schema; it must not introduce a second
evidence format.
