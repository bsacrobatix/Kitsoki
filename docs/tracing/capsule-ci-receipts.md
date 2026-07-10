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

The CLI/MCP CI status surfaces retain the run envelope and verdict locally. Run
Index/runstatus receipt projection and provider-status publication are being
added on top of the same receipt schema; they must not introduce a second
evidence format.
