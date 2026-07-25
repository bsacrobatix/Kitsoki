# integration-train

`integration-train` reconciles a bounded, sealed set of durable worker fix
bundles through integration, aggregate verification, staging, protected main,
and deployment attestation.

It is deliberately authority-neutral. The story never runs Git, queue, or
deployment commands itself. Importers bind the `train` host interface to an
effectful authority that performs or reconciles each phase and returns
`kitsoki/integration-train-authority/v1` evidence. The story validates that
evidence deterministically before advancing. With no authority binding, the
run exits `needs-input`; it never manufactures a ship, queue receipt, or
deployment.

## Entry contract

Import at `bootstrap` and project a `job` object matching
[`schemas/job.schema.json`](schemas/job.schema.json). The manifest is immutable:

- every candidate carries `candidate_id`, report reference and digest,
  execution ID, shipped SHA, base SHA, and bundle digest;
- candidate IDs are unique and sorted ascending (`candidate_id-asc`);
- `manifest_digest` is the authority-issued seal over the canonical manifest;
- `max_items` bounds the train and cannot exceed 100.

An optional `checkpoint` resumes an interrupted train. Every authority call
receives the complete sealed job plus the last accepted checkpoint. A phase may
reconcile an already-completed effect, but its response must return the same
train ID, manifest digest, phase, and a monotonic checkpoint.

## Authority operation

Bind `host_interfaces.train` to a host implementing:

```text
reconcile({phase, job, checkpoint})
  -> {evidence: kitsoki/integration-train-authority/v1}
```

The evidence contract is
[`schemas/authority-evidence.schema.json`](schemas/authority-evidence.schema.json).
Phase-specific requirements:

| Phase | Required authority evidence |
| --- | --- |
| `validate` | sealed manifest accepted; stable candidate order echoed |
| `integrate` | one disposition per candidate (`accepted`, `conflict`, `invalid`, `needs_input`) and exact integrated SHA |
| `gate` | aggregate gate receipt; a red aggregate may proceed only with a completed bisect and isolated candidates |
| `staging` | landed queue receipt for target `staging`, exact gated SHA |
| `main` | distinct landed queue receipt for target `main`, exact staged SHA |
| `deploy` | attestation binding main SHA/tree, release/image digest, environment, health verdict, verifier, and time |

Queue `queued`, `running`, or target-moved responses are not landings. Deployment
identity or health mismatch is not a release.

## Exits

- `released`: every candidate was accepted, both queue targets landed, and the
  deployment attestation matches.
- `partial`: the same proof chain completed after explicitly excluding one or
  more conflict/invalid candidates.
- `needs-input`: authority unavailable, malformed or mismatched provenance,
  unresolved candidate, target movement, unisolated gate failure, or deployment
  mismatch. `last_error`, `resume_phase`, and `checkpoint` remain observable.

## World out

Importers normally lift `status`, `checkpoint`, `item_dispositions`,
`integrated_sha`, `gated_sha`, `staging_receipt`, `main_receipt`,
`deployment_attestation`, `last_error`, and `resume_phase`.

## Deterministic verification

```sh
go run ./cmd/kitsoki test flows stories/integration-train/app.yaml
```

The fixtures use no LLM calls. The authority host is stubbed, while the
Starlark evidence validator runs for real.

The missing generic effectful host is tracked in
`issues/bugs/2026-07-25T172340Z-integration-train-lacks-an-effectful-authority-host.md`.
