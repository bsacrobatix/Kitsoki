---
# --- identity ------------------------------------------------
id: "2026-07-25T195758Z-promote-exact-staging-sha-with-receipt"
title: "Queue cannot receipt-bind promotion of an exact staging-landed SHA"
target: "kitsoki"
filed_at: 2026-07-25T19:57:58Z
filed_by: "codex"

# --- target context ------------------------------------------
component: "capsule-queue"
kitsoki_rev: "e663761"

# --- classification ------------------------------------------
severity: "critical"
status: "fixed"
labels: ["integration-train", "promotion", "receipt"]

# --- evidence ------------------------------------------------
related:
  - "2026-07-25T172340Z-integration-train-lacks-an-effectful-authority-host"
---

# Queue cannot receipt-bind promotion of an exact staging-landed SHA

The integration-train evidence contract correctly requires the main queue
candidate SHA to equal the SHA that landed on `staging`. Today `kitsoki capsule
promote` can only run Capsule CI and queue admission for a registered workspace
head. After staging lands, that SHA is an integration/finalization result, not
the source workspace head, so the host cannot obtain a new receipt-bound main
candidate for the required exact SHA without lying about provenance.

## Required contract

Add a receipt-bound `promote-existing` primitive (CLI plus native API) that:

1. Accepts a project root, an explicit source target (`staging`), an exact
   landed SHA, a destination target (`main`), pipeline, and deterministic gate.
2. Verifies the SHA is the current or durably receipted landed result for the
   source target; rejects an arbitrary reachable commit, target movement, or
   missing/incomplete staging receipt.
3. Runs Capsule CI against that exact SHA (no mutable workspace-head
   substitution), stores a new immutable receipt, and submits a `main` queue
   candidate whose `SHA` equals the staging landed SHA and whose required
   receipt chain references the staging landing.
4. Is idempotent across retries and controller restart: the same
   `(source-target, landed-sha, destination-target, pipeline)` returns the
   original receipt/candidate, while a changed target or receipt is
   `needs_input`/`target_moved` with durable evidence.
5. Never offers a test waiver and never does a protected CAS itself; the
   normal target-partitioned queue worker remains the sole finalizer.

## Required tests

- A real-git queue fixture: worker candidate → staging landing →
  `promote-existing` → main queue candidate, with candidate SHA exactly equal
  to staging's landed SHA.
- Reject a SHA that is merely reachable but has no matching staging landing.
- Reject when staging advances after the observed receipt.
- Repeat after process restart and prove one CI receipt / one main queue
  candidate, not duplicated admission.
- Verify main admission retains both the new CI receipt and the staging
  landing receipt as required provenance.

## Resolution

Implemented by `kitsoki capsule promote-existing` and the native
`queue.PromoteExistingAuthority`. The authority persists the observed source
and destination refs before exact-source CI, uses a deterministic CI job
identity, rejects missing or changed source landing receipts and target
movement, and only submits the destination candidate. The target-partitioned
queue worker remains the sole protected CAS owner. Real-Git tests cover the
staging-to-main path, arbitrary reachable commits, missing source receipt,
mid-certification target movement, and controller restart without duplicate
receipt or queue admission.
