# Tracing: capsule CI receipts and attestations

**Status:** v1 in progress. Canonical receipt build/verification, tamper tests,
signer/verifier DI, fake signer, project receipt-signature policy, shared trace
schema constants, controller trace sidecars, promotion-gate binding, and
`capsule-ci-run-index/v1` status projection, and provider-safe summary
projection ship. Rich trace producers, live provider publication, and trusted
remote receipt adoption remain.
**Kind:**   tracing
**Epic:**   [capsule-ci.md](capsule-ci.md)
**Depends on:** [`capsule-control-plane.md`](capsule-control-plane.md),
[`story-native-ci.md`](story-native-ci.md)

## Why

A CI status is only trustworthy if a reviewer can answer: which source and
environment ran, which story/imports defined the pipeline, what authority was
granted, what sandbox/network policy was actually applied, which deterministic
checks and LLM/human decisions occurred, what changed, where the evidence lives,
and whether the result is the one being promoted.

Kitsoki already records agent and gate decisions and has shipped
`artifactjob.Store`, `RunIndex`, and trace reindexing without rewriting trace
bytes ([`artifact-driven-stories.md`](artifact-driven-stories.md#shipped-substrate)).
Capsule manifests already carry source/head/network/tree/probe provenance
([`../guide/development/capsules.md`](../guide/development/capsules.md#manifest)).
What is missing is a normalized, content-addressed projection that joins those
facts into one machine-verifiable CI handoff.

## What changes

Add `capsule-ci-receipt/v1`, derived from the immutable execution envelope,
capsule/environment manifests, trace events, artifact index, typed story
verdict, and sync/promotion events. The receipt is JSON and is the canonical
machine artifact; runstatus and optional Slidey/Markdown summaries are human
views over it.

The receipt generator is a consumer first. It reuses existing agent/gate/host/
artifact events and adds only capsule lifecycle, applied-policy, executor, and
sync event families that the runtime slices do not currently record.

```jsonc
{
  "schema": "capsule-ci-receipt/v1",
  "receipt_id": "sha256:...",
  "job_id": "job-...",
  "run_id": "run-...",
  "attempt": 1,
  "project": {"id": "acme", "profile_digest": "sha256:..."},
  "source": {
    "repo": "acme/widgets", "base_ref": "main", "base_sha": "...",
    "head_ref": "agent/fix-1", "head_sha": "...", "tree_digest": "sha256:..."
  },
  "capsule": {"definition": "change", "definition_digest": "sha256:...", "instance_id": "..."},
  "environment": {"id": "ci", "lock_digest": "sha256:...", "image_digest": "sha256:..."},
  "story": {"app": ".kitsoki/stories/acme-ci/app.yaml", "digest": "sha256:...", "imports_lock": "sha256:..."},
  "executor": {"id": "remote-ci", "placement": "worker-3", "applied_policy_ref": "event:..."},
  "policy": {"digest": "sha256:...", "network": "replay", "external_write": "deny"},
  "checks": [],
  "decisions": [],
  "cost": {"usd": 0.41, "input_tokens": 12000, "output_tokens": 900},
  "changes": {"before": "...", "after": "...", "sync_plan": "sha256:...", "remote_effects": []},
  "result": {"outcome": "passed", "promotion_eligible": true, "verdict_artifact": "artifact:..."},
  "artifacts": [],
  "trace": {"uri": "...", "digest": "sha256:..."},
  "integrity": {"content_digest": "sha256:...", "signer": null, "signature": null}
}
```

## Impact

- **Producers:** capsule control plane lifecycle, environment/executor applied
  policy, story CI terminal result, ref reconciliation, existing agent/gate/host
  and artifact producers.
- **Consumers:** artifact-job `RunIndex`, runstatus, `capsule ci status`, sync
  promotion checks, GitHub/provider status adapters, offline audit/report tools.
- **Format:** one new receipt schema plus capsule lifecycle/policy/executor/sync
  events; existing trace/cassette fields remain compatible.
- **Backward compat:** old traces remain replayable but cannot produce a trusted
  v1 receipt when required source/environment/story facts are absent; generator
  returns `incomplete` with missing facts, never a fabricated pass.
- **Docs on ship:** `docs/tracing/capsule-ci-receipts.md` and trace-format event
  tables.

## Event / format model

| Event | When emitted | Key fields |
|---|---|---|
| `capsule.workspace.declared|ready|changed|committed|closed` | control-plane instance transitions | project, instance, generation, definition/source/head/tree/policy digests, lease owner |
| `capsule.environment.resolved` | environment lock accepted | definition id/digest, lock digest, image/toolchain/lockfile inputs |
| `capsule.executor.selected|prepared|started|finished|failed|cancelled` | executor lifecycle | envelope digest, provider/placement, capabilities, execution id, attempt |
| `capsule.policy.applied` | preparation/launch policy applied | requested vs. applied network/isolation/cache/secret/resource posture, degradation |
| `capsule.ci.started|verdict` | story CI starts/terminates | pipeline, trigger digest, story digest, verdict artifact/outcome |
| `capsule.sync.observed|planned|applied|stale|conflicted|aborted` | reconciliation lifecycle | operation, observed refs, plan digest, old/new refs, remote effects |
| existing `agent.*` / `machine.gate_decided` | interpretive work and gates | call/decision id, profile/model, schema verdict, chosen intent, confidence, cost |
| existing `artifact.emitted` | evidence written | handle, kind, mime, label, path/digest/size |

Every new event carries `job_id`, `run_id`, `attempt`, `project_id`, and the
execution-envelope digest where available. Secret values, raw provider tokens,
and unredacted environment dumps are forbidden.

## Determinism

For a completed attempt, the receipt is rebuilt as follows:

```text
execution envelope + managed manifests + ordered trace + artifact index
        │ validate ids/digests/event sequence
        ▼
normalized receipt (signature fields empty)
        │ canonical JSON + sha256
        ▼
receipt_id/content_digest
        │ optional provider/local signer
        ▼
integrity.signer + signature
```

Canonicalization fixes field order, time representation, numeric forms, null vs.
omitted optional fields, artifact/check ordering, and decision references.
Reindexing the same trace/manifests yields byte-identical unsigned receipt JSON.
Signing is a final non-rebuildable envelope over the content digest and signer
metadata; it never changes the receipt's semantic result.

Promotion eligibility is derived, not trusted from prose or a story boolean. It
requires:

1. a terminal typed verdict whose pipeline/source/story/environment/envelope
   digests match the receipt;
2. every manifest-required check present with acceptable outcome and evidence;
3. no missing terminal agent event, policy degradation above the allowed level,
   exhausted/unreported cost, or unapproved external effect;
4. the sync/promotion plan (when present) targeting the exact candidate head;
5. signer/trust requirements satisfied by project policy.

## Producers & consumers

The artifact-job registry remains the durable identity. The receipt generator
registers the receipt as an artifact and writes no new job table. `RunIndex`
continues to be rebuildable from trace files; it gains receipt metadata and
verification status as a derived projection.

Runstatus renders:

- source/story/environment/executor identity;
- deterministic, LLM, human, and infra checks as distinct kinds;
- requested vs. applied confinement/network policy and any degradation;
- model/profile/schema/confidence/cost for interpretive checks;
- changed refs and external effects;
- missing evidence/integrity failures;
- a trace link and artifact gallery.

GitHub or another CI provider receives a compact status from the verified
receipt: pipeline, outcome, summary, run URL, receipt id, and promotion
eligibility. Provider comments are a view; the receipt/trace remains the source
of evidence.

## Backward compatibility

- new fields/events are additive and unknown-event-tolerant for old readers;
- receipt generation from pre-Capsule-CI traces returns
  `{verification: incomplete, missing:[...]}` and cannot authorize promotion;
- cassettes need new events only when testing Capsule CI surfaces; existing
  story cassettes replay unchanged;
- path fields are stored as scoped artifact/workspace references when possible;
  local absolute paths may appear only in private debug metadata and are not
  part of the content-addressed portable receipt;
- version migration is explicit: v1 readers do not silently accept a future
  schema as v1.

## Fixtures / golden traces

- `local-deterministic-pass`: no agents, sealed environment, valid promotion
  receipt; rebuild is byte-stable.
- `llm-review-pass`: cassette-backed reviewer with model/profile/schema/cost and
  decision ref clearly labeled.
- `human-park`: `needs_input`, no promotion eligibility.
- `sandbox-degraded`: story verdict says pass but applied policy violates the
  minimum; receipt verification fails closed.
- `remote-signed-pass`: fake signer/worker, verified content digest and signer
  policy.
- `remote-tamper`: changed artifact/trace/source digest invalidates signature
  and promotion.
- `retry`: first infra failure plus second accepted attempt; history preserved,
  accepted attempt explicit.
- `legacy-incomplete`: old trace replays but lists missing receipt facts.

## Tasks

```text
## 1. Emit and schema
- [ ] 1.1 Define capsule lifecycle, environment/executor/policy, CI verdict, and sync trace event schemas with shared run/envelope ids
  - Shipped: `internal/capsule/trace` defines `capsule-ci-trace/v1` document validation plus CI/workspace/environment/sync event-kind schemas; CI receipt traces and sync lifecycle facts consume these shared constants.
  - Remaining: complete producer adoption for all lifecycle/environment/executor/policy facts and golden trace coverage.
- [x] 1.2 Define capsule-ci-receipt/v1 JSON schema, canonicalizer, content hashing, and verification result
- [ ] 1.3 Wire runtime/story producers and enforce secret/path redaction
  - Shipped: shared trace validation rejects provider-unsafe `fields` and
    `error` content, including secret/token/password/credential-style keys,
    secret-looking inline values, and absolute host paths.
  - Remaining: wire every runtime/story producer through this schema and add
    producer-specific redaction fixtures.

## 2. Rebuild and consume
- [x] 2.1 Add deterministic receipt builder over manifests, trace, artifact index, and typed verdict
- [x] 2.2 Derive promotion eligibility and explicit incomplete/invalid reasons
- [ ] 2.3 Add RunIndex receipt projection, capsule ci status output, and runstatus/provider summary consumers
  - Shipped: `capsule-ci-run-index/v1`, CLI/MCP status projection, and `capsule-ci-provider-summary/v1` via CLI/MCP.
  - Remaining: live runstatus/provider publication adapters.
- [x] 2.4 Add signer/verifier DI seam and fake; require according to project policy

## 3. Prove and document
- [ ] 3.1 Add all golden traces above; assert byte-stable unsigned rebuild and old-reader compatibility
  - Shipped: `internal/capsule/trace` has a byte-stable
    `local-deterministic-pass`-style golden trace with lifecycle,
    environment, CI-start, and verdict events, plus an old-reader projection.
  - Remaining: add the LLM review, human park, sandbox degraded,
    remote-signed, remote-tamper, retry, and legacy-incomplete goldens.
- [x] 3.2 Tamper tests cover trace, artifact, source, story, environment, policy, signer, and accepted-attempt substitution
  - Shipped: receipt verification rejects trace/artifact/source/story/environment/policy/signer/signature tampering, and promotion gates reject a run record that substitutes a different accepted attempt behind a valid receipt id.
- [x] 3.3 Add one no-LLM local and fake-remote end-to-end receipt used by a promotion-plan fixture
  - Shipped: host and `remote-fake` Capsule CI service runs persist receipts, write run projections, and authorize a promotion plan through the real `PromotionGate`.
- [ ] 3.4 Update trace/receipt/runstatus docs; trim/delete this proposal
```

## Open questions

1. **Signature format** — DSSE/in-toto-compatible envelope vs. Kitsoki-specific
   detached signature. *Lean: adopt a standard envelope if it can carry the
   canonical receipt without changing the inner schema; spike before locking
   v1.*
2. **Trace digest scope** — whole raw JSONL vs. normalized attempt slice. *Lean:
   digest both: raw stored trace for custody, normalized attempt event set for
   portable verification.*
3. **LLM evidence disclosure** — full prompt/output vs. hashes and private trace
   refs. *Lean: receipt holds schemas, model/profile/cost, decision/outcome, and
   refs/hashes; raw prompts stay in access-controlled trace artifacts.*

## Non-goals

- A new job registry, blob store, or narrative trace format.
- Claiming cryptographic signatures make an unconfined worker trustworthy; the
  receipt separately states applied policy and trust domain.
- Publishing private prompts, source, logs, or secrets in provider statuses.
- Collapsing deterministic, LLM, human, and infrastructure checks into one
  indistinguishable “passed” row.
