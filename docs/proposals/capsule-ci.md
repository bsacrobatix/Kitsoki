# Epic: productized capsule CI

**Status:** v1 in progress. A local vertical slice now ships: scoped native
workspaces/MCP, self+pinned sources, environment locks, story-native CI,
canonical receipts, local ref plans, and host/fake-remote providers (see
`docs/guide/development/capsule-ci.md`). Native lifecycle compatibility is now
the agent-facing path; host migration, production remote publication, receipt
indexing, and full adoption remain below.
**Kind:**   epic
**Slices:** 5 (all have partial substrate; none is fully retired)

## Why

Kitsoki already has most of the hard parts of a new kind of CI, but they are
separate products and repository-specific scripts rather than one capability.
`kitsoki capsule` can materialize and verify deterministic synthetic git states;
`scripts/dev-workspace.sh` can create, commit, integrate, and tear down isolated
clone-backed development workspaces; the staging helpers can reconcile local and
remote refs without editing protected `main`; stories can run deterministic
commands, LLM reviews, and human gates; and the trace can explain every recorded
decision.

What is missing is the general contract that joins those parts for any onboarded
project. A project with `.kitsoki/` should be able to declare its source,
environment, workspace policy, and CI stories once, then run the same governed
pipeline locally, in a container, or on a remote worker. An external coding agent
should be grantable only a project-scoped Capsule MCP server and still be able to
create a workspace, inspect and modify files, run permitted commands, commit its
work, ask Kitsoki to run the project's CI story, and return a trace-backed
receipt—without ambient access to the protected checkout, host credentials, or
unrelated repositories.

This epic productizes that end state without reopening the retired
`hermetic-capsules.md` proposal. Its shipped v1 lives in
[`../guide/development/capsules.md`](../guide/development/capsules.md); this epic
owns the explicitly deferred remote, environment, workspace-provider, MCP, and
CI-story follow-ups.

## What changes

`kitsoki capsule` becomes the project-aware control plane for a lifecycle with
five distinct objects:

1. a **capsule definition** pins source, environment, and policy;
2. a mutable **workspace instance** is leased inside the declared project scope;
3. an **execution envelope** binds that instance to an executor and a story;
4. the **story run** is the CI pipeline, including deterministic checks, LLM
   tasks/reviews, and human gates;
5. a content-addressed **receipt** joins source, environment, policy, trace,
   artifacts, decisions, and final verdict.

Local and remote CI consume the same execution envelope. Placement changes how
the workspace is materialized and the story is run; it does not change the
pipeline or its evidence contract. The existing Kitsoki workspace and protected
main scripts become the first adapter and migration oracle, not a second
long-term implementation.

## Impact

- **Spans:** runtime, story, and tracing.
- **Net surface:** project capsule/environment definitions under `.kitsoki/`, a
  checked-in `.kitsoki/ci.yaml` routing manifest, a project-scoped Capsule MCP
  server, local/container/remote executor providers, compare-and-swap ref
  reconciliation, story CI result contracts, and a rebuildable
  `capsule-ci-receipt/v1` artifact.
- **Docs on ship:** `docs/guide/development/capsules.md`,
  `docs/guide/development/capsule-ci.md`, `docs/stories/ci.md`,
  `docs/tracing/capsule-ci-receipts.md`, and the onboarding/project-profile
  reference.

## Existing ownership map

The following seams are dependencies, not blank space for this epic to
reimplement:

| Existing surface | What it already owns | This epic's relationship |
|---|---|---|
| `internal/capsule` and `kitsoki capsule open|verify|close` (`internal/capsule/capsule.go:70-157,261-366`; `cmd/kitsoki/capsule.go:18-26`) | Capsule spec loading, deterministic synthetic materialization, manifests, verification, sentinel-gated cleanup | Generalize behind the control plane; preserve existing commands as compatible aliases |
| `scripts/dev-workspace.sh` and the staging/sync helpers ([`../dev-workspaces.md`](../dev-workspaces.md#quick-path)) | Clone-backed local workspaces, bootstrap, commit, staging integration, protected-main promotion, remote freshness | Treat current behavior as the reference adapter and parity suite; migrate mechanics into providers rather than shelling to project-local scripts forever |
| `iface.workspace` and `host.git_worktree` (`stories/dev-story/app.yaml:311-344`; `internal/host/git_worktree.go:70-91`) | Story-facing workspace abstraction and compatibility operations | Rebind to the Capsule control plane; do not add capsule-specific room logic |
| `iface.ci` and `host.local` (`stories/dev-story/app.yaml:297-309`; `internal/host/local_ci.go:31-80`) | Local build/test host calls | Retain as deterministic steps that CI stories may compose; do not turn the handler itself into the pipeline engine |
| Artifact jobs and the trace index ([`artifact-driven-stories.md`](artifact-driven-stories.md#shipped-substrate)) | Durable `job_id`, run attachment, artifact indexing, rebuildable trace projections | Reuse as the run identity and artifact catalog; no capsule-specific job database |
| Agent toolboxes, launch policy, and `sandbox:` ([`../stories/state-machine.md`](../stories/state-machine.md#agent-toolboxes); [`../guide/agents/launch-policy.md`](../guide/agents/launch-policy.md)) | Tool grants, effect classification, launch placement checks, applied runtime policy | Enforce the Capsule MCP grant and process confinement; capsules do not invent a second sandbox model |
| Execution modes and gate deciders ([`execution-modes-and-gate-deciders.md`](execution-modes-and-gate-deciders.md#2-execution-mode)) | Default/LLM/human gate resolution and decision recording | CI stories use it directly for autonomous, LLM-reviewed, or parked-for-human runs |
| Arena `ContainerBackend`, placement scheduler, and completion-state contract (`tools/arena/README.md:34-58,128-136`) | Container execution, local/remote Docker placement, infra-vs-model classification | Extract or adapt its executor seam; do not build a parallel remote scheduler |
| Retired capsule design's Harbor lanes and repo-history capsule catalog | Pinned-source/hidden-oracle requirements plus Harbor-shaped import/export and agent-shim direction | Preserve source/verifier/interchange compatibility; keep Harbor an edge adapter rather than the Capsule CI engine |
| GitHub-agent proposals | GitHub ingress, auth, comments, PR dispatch | Consume Capsule CI and publish its receipt; GitHub is one trigger/provider, not the CI core |

## Slices

| # | Slice | Kind | Scope (one line) | Depends on | Status | File |
|---|---|---|---|---|---|---|
| 1 | Project-scoped Capsule control plane and MCP | runtime | Canonical definitions, leased workspace handles, scoped file/process/VCS tools, CLI/MCP parity, and a migration adapter over today's workspace script | — | Draft | [`capsule-control-plane.md`](capsule-control-plane.md) |
| 2 | Ref sync and promotion | runtime | Plan/apply reconciliation for workspace, staging, protected branches, and remotes with compare-and-swap safety and explicit external-write authority | 1 | Draft | [`capsule-sync-promotion.md`](capsule-sync-promotion.md) |
| 3 | Portable environments and executors | runtime | Sealed environment locks plus one execution envelope across host, container, and remote-worker providers | 1 | Draft | [`capsule-environments-executors.md`](capsule-environments-executors.md) |
| 4 | Story-native CI | story | `.kitsoki/ci.yaml` routes triggers to stories; story exits and a schema-validated verdict—not a stage DSL—define CI | 1, 3 | Draft | [`story-native-ci.md`](story-native-ci.md) |
| 5 | Trace-backed CI receipts | tracing | Rebuildable receipts and attestations joining source, environment, policy, decisions, artifacts, cost, sync, and verdict | 1, 4 | Draft | [`capsule-ci-receipts.md`](capsule-ci-receipts.md) |

## Sequencing

```text
#1 control plane ──▶ #2 ref sync / promotion ───────────────┐
        │                                                   │
        └──────────▶ #3 environments / executors ──▶ #4 story CI
                                                       │    │
                                                       └────┴──▶ #5 receipts
```

Slice 1 establishes the scoped handle and provider contracts. Slices 2 and 3
can then proceed in parallel. Slice 4 first ships locally against slice 1 and a
host executor, then gains portable/remote parity from slice 3. Slice 5 may begin
once slice 1 emits lifecycle facts, but it is not complete until it can rebuild
a full story-CI receipt and include slice-2 sync/promotion facts.

## Shared decisions

1. **Stories are the CI definition.** `.kitsoki/ci.yaml` names triggers,
   stories, environments, execution posture, and required result contracts. It
   does not grow `steps:`, `needs:`, matrix shell blocks, or a second workflow
   language. Rooms, intents, agents, gates, and exits remain the pipeline.
2. **Placement is orthogonal to behavior.** Local, container, and remote workers
   consume the same immutable execution envelope and must produce the same
   result schema. Provider capability differences fail during preparation; they
   do not silently rewrite the story.
3. **Authority is project-rooted and handle-based.** A Capsule MCP server starts
   with an immutable project scope and returns opaque workspace handles. Tool
   calls never accept arbitrary host paths or widen the startup grant.
4. **Configuration and runtime state stay separate.** Checked-in definitions
   live under `.kitsoki/`; machine-local provider endpoints and secret refs live
   in `.kitsoki.local.yaml`; mutable clones/caches live under `.capsules/`; run
   artifacts live under `.artifacts/` or the configured artifact-job store.
5. **Remote writes are a separate capability.** Fetch/status may be granted
   without push/PR/merge. A green CI verdict never implies permission to publish
   a branch or promote a protected ref.
6. **The trace remains the explanation.** The receipt is a deterministic,
   queryable projection over manifests and trace events. It does not become a
   second narrative log or hide LLM/human decisions behind a boolean check.
7. **No-cost testing remains mandatory.** Kitsoki's own tests use flow fixtures,
   cassettes, fake executors/remotes, and synthetic capsules. A user may opt a
   production CI story into live LLM work with explicit profile, budget, and
   fallback policy; that permission never leaks into automated tests.
8. **Harbor is an interchange edge, not the core.** Capsule definitions keep
   pinned source, instruction, verifier-only assets, and reference-solution
   roles mechanically exportable to a Harbor task. Import/export and a Kitsoki
   agent shim remain a focused adapter after the native envelope/receipt is
   stable; Capsule CI does not delegate its story gates or trace guarantees to
   Harbor.

## Cross-cutting open questions

1. **CLI namespace** — `kitsoki capsule ci run` vs. the shorter `kitsoki
   capsule run`. *Lean: `capsule ci run` while the distinction between
   workspace lifecycle and story execution is new; aliases can follow usage.*
2. **Project manifest home** — dedicated `.kitsoki/ci.yaml` vs. a large `ci:`
   block in `project-profile.yaml`. *Lean: dedicated file referenced from the
   project profile; it changes at a different cadence and should remain a small
   routing/policy manifest.*
3. **Remote receipt signatures** — required for every remote executor vs. only
   for providers whose trust policy demands it. *Lean: integrity hashes always;
   signer identity required by remote/protected-promotion policy, optional for a
   same-user local run.*

## Non-goals

- Replacing GitHub Actions, Buildkite, Kubernetes, or another trigger/worker
  fleet on day one. They are adapters around the envelope and receipt.
- A second story/workflow language for CI stages.
- Treating an LLM verdict as deterministic proof. The receipt labels decision
  type, model/profile, schema, cost, and supporting evidence.
- Hiding remote network, credential, or branch mutation behind generic shell
  commands.
- Reopening shipped synthetic-capsule v1 or duplicating artifact-job, sandbox,
  Arena placement, or GitHub-ingress ownership.
