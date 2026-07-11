# Runtime: portable capsule environments and executors

**Status:** v1 in progress. Environment definitions/locks, host probe,
pinned-image/devcontainer resolution, lockfile/bootstrap input hashing,
cache/secret-reference grant validation with lock redaction, host and
fake-remote executor contracts, pipeline-selected CLI/MCP placement, and
environment operations ship locally. The HTTPS remote-worker transport and
checked-in `remotes:` project selection serialize sealed envelopes and typed
results with header-only credential injection. The Arena-style container
adapter now consumes `completion-state/v1` through both a no-Docker fake backend
and a Docker backend that runs `capsule worker run` against a mounted workspace;
the CI executor catalog accepts an injected production `container` provider
while keeping the no-Docker `container-fake` test lane. Repo-history capsules
project environment/executor metadata. Final Arena docs trimming remains.
A bounded VM dogfood of the Studio/Claude worker path reached real GLM-5.2
worker launch but stalled before any provider stream or terminal verdict; it is
not counted as remote-worker completion. The deployed HTTPS Capsule worker proof
and final Arena docs trimming remain.
**Kind:**   runtime
**Epic:**   [capsule-ci.md](capsule-ci.md)
**Depends on:** [`capsule-control-plane.md`](capsule-control-plane.md)

## Why

The shipped capsule core deliberately stops at synthetic local repositories:
remote pinned sources, environment image capture, sealing, and remote execution
are named follow-ups in
[`../guide/development/capsules.md`](../guide/development/capsules.md#hermetic-capsules).
Mutable development workspaces bootstrap through a project-local Make target and
share a runstatus dependency cache
([`../dev-workspaces.md`](../dev-workspaces.md#bootstrap)). Repo-history and Arena
have separate container/materialization paths, and Arena already proves local or
remote Docker placement through a `ContainerBackend` seam
(`tools/arena/README.md:128-136,412-430`).

Without a common environment lock and execution envelope, a “same” CI story can
silently use different tools, network, caches, credentials, and confinement on a
developer laptop and a remote worker. Moving the story is not enough; the
execution facts must move with it.

## What changes

Add project environment definitions, resolved environment locks, and an
`ExecutorProvider` interface shared by Capsule workspaces and story CI.

Checked-in definitions live under `.kitsoki/environments/`. Resolution produces
a content-addressed lock recording image/toolchain/lockfile/bootstrap inputs.
The lock plus capsule instance, story identity, trigger, policy, and artifact
job id form an immutable execution envelope. Host, container, and remote-worker
providers consume that envelope and return the same completion/result contract.

```yaml
# .kitsoki/environments/ci.yaml
schema: capsule-environment/v1
id: ci
source:
  devcontainer: .devcontainer/devcontainer.json  # or image / host_probe
toolchains:
  go: "1.26.1"
  node: "22.20.0"
lockfiles:
  - go.sum
  - tools/runstatus/pnpm-lock.yaml
bootstrap:
  command: bootstrap-workspace
network: none
caches:
  - id: go-build
    scope: project
    mode: read_write
  - id: runstatus-node-modules
    scope: project
    mode: read_write
sandbox:
  minimum: supervised
```

`kitsoki capsule env lock ci` resolves floating image references, hashes
lockfiles/bootstrap inputs, probes declared host tools, and writes a reviewable
lock artifact. It never installs a tool on the host. A host executor must match
the lock or refuse; a container/remote provider may materialize the declared
environment.

## Impact

- **Code seams:** `internal/capsule/environment`,
  `internal/capsule/executor`, an Arena adapter/extraction, CLI/MCP environment
  tools, and remote one-shot/worker protocols.
- **Vocabulary:** environment definition/lock, executor capability, execution
  envelope, placement, cache grant, secret reference, applied policy.
- **Stories affected:** none behaviorally. Story CI selects a named environment
  and receives a prepared workspace; story files do not branch on provider.
- **Backward compat:** an omitted environment maps to an explicit `host-current`
  compatibility definition with probe-only behavior. Current bootstrap scripts
  remain provider hooks until migrated.
- **Docs on ship:** `docs/guide/development/capsule-environments.md`, executor
  provider reference, and Arena consolidation notes.

## Vocabulary changes

| Kind | Name | Shape | Notes |
|---|---|---|---|
| spec | `capsule-environment/v1` | `{id, source, toolchains, lockfiles, bootstrap, network, caches, sandbox}` | Checked in; no secrets or machine endpoints |
| artifact | `capsule-environment-lock/v1` | resolved digests/versions/input hashes | Content-addressed and reviewable |
| runtime type | `ExecutionEnvelope` | `{job, project, definition, instance, source, story, environment_lock, trigger, policy, inputs, outputs}` | Immutable provider input |
| runtime type | `ExecutorCapabilities` | `{placements, os, arch, isolation, network, caches, secret_delivery, cancellation}` | Preparation fails on unmet minimums |
| provider | `host` | probe and run on current machine | No auto-install; useful for local fast path |
| provider | `container` | materialize/run in image by digest | Adapts Arena's container backend |
| provider | `remote` | submit envelope, collect events/artifacts/receipt | Transport-neutral worker contract |
| MCP | `capsule.env.resolve|lock|verify` | definition -> lock/probe result | No secret values returned |
| MCP | `capsule.executor.describe` | executor id -> capabilities | Read-only |

## The model

```text
environment definition + source refs + policy
                    │ deterministic resolve
                    ▼
          EnvironmentLock{digest}
                    │
workspace instance + story + trigger + job_id
                    │
                    ▼
             ExecutionEnvelope
          ┌─────────┼─────────┐
          ▼         ▼         ▼
        host     container   remote worker
          └─────────┼─────────┘
                    ▼
       events + artifacts + typed result
```

Provider contract:

```go
type ExecutorProvider interface {
    Describe(context.Context) (ExecutorCapabilities, error)
    Prepare(context.Context, ExecutionEnvelope) (PreparedExecution, error)
    Run(context.Context, PreparedExecution, EventSink) (ExecutionResult, error)
    Collect(context.Context, PreparedExecution) (CollectedArtifacts, error)
    Cancel(context.Context, ExecutionID) error
}
```

`Prepare` is side-effect-bounded and idempotent by envelope digest. It verifies
capabilities and materializes content, but it does not reinterpret the story.
`Run` executes Kitsoki against the pinned story/import lock. `Collect` returns
content hashes and provider metadata; it never turns missing evidence into a
pass.

### Remote contract

The remote seam supports two transports without changing the envelope:

- **one-shot runner:** `kitsoki capsule worker run --envelope envelope.json`
  for an existing CI service, SSH command, container job, or GitHub Action;
- **worker service:** a queue/stream adapter submits the same envelope and
  streams normalized events before returning artifacts and a receipt.

Transport auth, scheduling, retries, and provider job IDs are adapter concerns.
The executor interface owns capability negotiation, cancellation, event order,
and artifact collection. Arena's scheduler may remain the multi-cell fleet
consumer; a single Capsule CI run does not introduce another fleet scheduler.

## Decision recording

Environment resolution and capability matching are deterministic and emit:

- `capsule.environment.resolved` with definition/lock/input digests;
- `capsule.executor.selected` with requested minimums, advertised capabilities,
  placement, and selection reason;
- `capsule.executor.prepared|started|finished|failed|cancelled` with envelope and
  provider execution ids;
- `capsule.policy.applied` with requested vs. actually applied network,
  isolation, cache, secret-delivery, and resource policy.

If a story or operator chooses among eligible executors for cost/speed/trust,
that is a recorded story decider and its event id is included in
`executor.selected`. A deterministic explicit executor id needs no interpretive
decision.

## Engine seams & invariants

1. environment locks cover every referenced image digest, toolchain version,
   lockfile digest, bootstrap command definition, and base-definition digest;
2. checked-in definitions contain secret **names/refs** only; resolved values
   are delivered ephemerally to the narrow provider call and never written to
   workspace manifests, envelope JSON, events, or artifacts;
3. `network: none` is the default. `replay` and `live` are distinct grants;
4. host mode probes and refuses mismatch; it does not run package managers or
   change global tools to make the probe pass;
5. container image tags resolve to immutable digests in the lock before a run
   can produce a trusted receipt;
6. cache keys include environment lock, project, architecture, and declared
   inputs. Write access is explicit; protected or cross-project cache poisoning
   is rejected;
7. the applied sandbox strength comes from the existing `sandbox:`/agent-runtime
   capability ladder, and a weaker-than-required provider fails closed;
8. remote workers verify project/source/story/environment digests before start
   and return the observed values; the controller rejects mismatches;
9. provider retry never reruns a completed story invisibly—artifact-job/run
   identity records every attempt and the receipt names the accepted attempt;
10. executor stdout is evidence, not the result contract. Completion requires a
    schema-valid story result and required artifacts.

## Backward compatibility / migration

- `host-current` captures current behavior for projects with only commands in
  `.kitsoki/project-profile.yaml`; receipts label it unsealed/degraded until an
  environment lock is adopted.
- `make bootstrap-workspace` and `.kitsoki.local.yaml` copying become explicit
  Kitsoki project hooks in the host executor; they are not generic defaults.
- the runstatus dependency cache becomes a named project cache under the generic
  cache contract, retaining its current key inputs before reuse is expanded;
- Arena's `ContainerBackend`/`FakeBackend`, placement, and completion-state
  adapters are used or extracted behind `ExecutorProvider`; existing Arena jobs
  remain consumers;
- repo-history materializers migrate capsule by capsule after pinned remote and
  hidden-oracle support lands. They are not blocked on mutable development
  workspace adoption;
- Harbor task import/export consumes the stable source/environment/verifier
  roles and execution envelope after v1. It remains an adapter in `tools/`, not
  an `internal/` runtime dependency or an alternate story engine.

## Tasks

```text
## 1. Environment contract
- [x] 1.1 Define environment schema, loader, lock schema, resolver DI seams, and content hashing
- [x] 1.2 Implement host probe, image-digest/devcontainer resolution, lockfile/bootstrap hashing, and no-auto-install refusal
- [x] 1.3 Define cache and secret-reference grants; redact all serialized surfaces
- [x] 1.4 Add env resolve|lock|verify CLI/MCP and compatibility host-current definition

## 2. Executor contract
- [x] 2.1 Define ExecutionEnvelope, capabilities, provider interface, event sink, attempts, cancellation, and fake provider
- [x] 2.2 Implement host provider with declared commands, sandbox/applied-policy reporting, and artifact collection
- [x] 2.3 Adapt/extract Arena container backend and completion-state handling; prove local container parity
- [x] 2.4 Implement remote one-shot protocol and fake streaming worker; add one real remote adapter only after offline conformance is green

## 3. Adopt and document
- [x] 3.1 Express Kitsoki bootstrap and runstatus cache as environment hooks/cache grants
- [x] 3.2 Run one no-LLM story identically on host, fake remote, and optional gated container; compare normalized results
- [x] 3.3 Migrate one repo-history capsule and one foreign onboarded project
  - Shipped: foreign project onboarding now writes a project-local
    `.kitsoki/environments/ci.yaml`, `.kitsoki/ci.yaml`, and minimal
    `.kitsoki/stories/capsule-ci/app.yaml` wrapper that parks honestly until
    checks are composed.
  - Shipped: repo-history capsule records now include a
    `capsule-environment/v1` projection, verifier-only oracle overlay metadata,
    a container executor contract, and the shared `completion-state/v1`
    materializer result. The harness remains the materializer for pinned repos
    and hidden oracles until real Docker provider adoption moves behind the
    common Go executor.
- [ ] 3.4 Update environment/executor/Arena docs; trim/delete this proposal
  - Shipped: Capsule CI docs now describe host, fake-remote,
    fake-container, Docker worker entrypoint, and repo-history environment
    projection.
  - Attempted: a two-instance VM dogfood used the external bakeoff worker path
    with Claude Code / GLM-5.2 and reached real process launch. It did not
    exercise a deployed HTTPS Capsule worker service and did not complete a
    typed remote verdict; keep this proposal open until that proof exists.
  - Remaining: fold the final Arena-specific extraction notes into the Arena
    guide, then delete this proposal once downstream docs no longer need it.
```

## Verification

Default tests are offline and no-LLM: schema/load failures, stable lock hashing,
host version match/mismatch, image-tag resolver fake, secret redaction, cache key
isolation, capability mismatch, cancellation, retry/attempt identity, event
ordering, missing result artifact, and remote digest mismatch. A fake executor
runs the same cassette-backed story envelope as host and returns byte-equivalent
normalized result fields. Docker/remote integration tests are separately gated
and never contact a live LLM.

## Open questions

1. **Lock file location** — beside each environment vs. central lock. *Lean:
   `.kitsoki/environments/<id>.lock.json`; diffs stay local to the definition.*
2. **Devcontainer resolution depth** — consume image/features only vs. fully
   reproduce lifecycle commands. *Lean: v1 resolves the image/features and
   explicit Capsule bootstrap; do not promise every editor-specific hook.*
3. **Remote event transport** — JSONL stream vs. MCP notifications vs. queue
   messages. *Lean: versioned JSONL event envelope as the portable core, with
   transport adapters.*
4. **Harbor adapter timing** — ship alongside the first remote executor vs.
   after native receipt v1. *Lean: after the receipt schema stabilizes, then
   implement mechanical import/export and a Kitsoki agent shim without changing
   the core provider interface.*

## Non-goals

- Building a general VM/container fleet scheduler; Arena or deployment adapters
  own fleets.
- Installing or upgrading host toolchains.
- Treating environment isolation as a replacement for agent toolboxes or OS
  sandboxing.
- Making network-live dependency resolution reproducible without pinned inputs.
- Replacing story gates/traces with Harbor's task runner; Harbor is an
  interchange/executor adapter only.
