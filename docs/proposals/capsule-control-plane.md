# Runtime: project-scoped Capsule control plane and MCP

**Status:** v1 in progress. `internal/capsule/control`, native workspace CLI,
and scoped Capsule MCP now cover definitions, lease/generation handles, safe
FS/exec/VCS, synthetic/self/pinned providers, and the `development`/`staging`
compatibility providers behind the native lifecycle. Host adoption and
story-contract migration have started via `host.capsule_workspace`; generic
clone lifecycle, seal/cache/verifier-overlay completion, and full story rebinding
remain.
**Kind:**   runtime
**Epic:**   [capsule-ci.md](capsule-ci.md)

## Why

Kitsoki currently has two capsule-shaped lifecycles with no common product
surface. `internal/capsule` opens and verifies synthetic fixture repositories,
while `scripts/dev-workspace.sh` creates mutable clone-backed development
workspaces and is discovered by `host.git_worktree` through project/CWD path
search (`internal/host/git_worktree.go:546-568`). The CLI exposes only
`list|open|verify|close` (`cmd/kitsoki/capsule.go:18-26`), and Studio MCP's
workspace handle is an authoring-story directory rather than a governed git
workspace.

That prevents the least-authority use case: give a coding agent one MCP server
that can do useful work only inside one onboarded project's managed capsules.
The agent either needs ambient filesystem/shell/git tools or the caller must
pre-create and hand it a workspace. Neither is an end-to-end capability.

## What changes

Add a long-lived `CapsuleManager` with CLI, host-interface, and standalone MCP
front doors. It resolves checked-in project definitions, materializes mutable
instances through a provider, leases each instance to a session/job, and
exposes only handle-scoped operations.

Definitions support the three source families already implicit in Kitsoki's
corpus: deterministic synthetic repositories, a live/self project ref, and a
pinned local or remote repository commit through a content-addressed cache.
Overlays/attachments carry visibility (`workspace|verifier`) so a hidden oracle
can be available to the CI story's verifier without appearing in the coding
agent's workspace. `capsule seal` captures an existing workspace into a
reviewable definition plus overlay/environment refs after applying secret and
size policy; it does not snapshot ambient credentials or caches.

The first provider wraps the shipped clone-backed behavior. Over the migration,
`scripts/dev-workspace.sh` becomes a compatibility wrapper over the Go control
plane plus Kitsoki-project hooks; arbitrary user projects do not need to copy
Kitsoki's scripts to use capsules.

Start the bounded server with an immutable authority grant:

```sh
kitsoki capsule mcp \
  --project /repo \
  --pipeline change \
  --executor local
```

An external agent may receive only this MCP server. It can create a workspace,
read/search/patch files, run policy-allowed commands, inspect/diff/commit local
changes, and start or observe a declared CI story. It never receives an
arbitrary host path, source-checkout write access, remote credentials, or an
undeclared executor.

## Impact

- **Code seams:** generalize `internal/capsule/`; add
  `internal/capsule/control/`, persistent instance metadata, and an MCP tool
  registrar; route `cmd/kitsoki/capsule.go` and the `workspace` host interface
  through the manager; retain a script adapter during migration.
- **Vocabulary:** capsule definition, workspace instance, lease, scope grant,
  workspace handle, provider capability.
- **Stories affected:** no room logic changes. Existing `iface.workspace`
  users rebind from the historical `host.git_worktree` name once parity is
  proven.
- **Backward compat:** existing root `capsules/<name>/capsule.yaml` fixtures and
  `capsule open|verify|close` remain valid. Existing workspace scripts remain
  callable until the parity suite and Kitsoki-specific hooks migrate.
- **Docs on ship:** `docs/guide/development/capsules.md`,
  `docs/architecture/hosts.md`, and `docs/guide/agents/mcp.md`.

## Vocabulary changes

| Kind | Name | Shape | Notes |
|---|---|---|---|
| runtime type | `CapsuleDefinition` | `{id, source, environment, policy, overlays, verifier_assets, verify}` | Immutable checked-in recipe; v1 synthetic specs map directly |
| runtime type | `WorkspaceInstance` | `{id, definition_ref, path, source_ref, head, branch, target, state, lease}` | Mutable materialization; path is internal and not caller authority |
| runtime type | `ScopeGrant` | `{project_root, workspace_roots, definitions, executors, effects, remotes, branches}` | Fixed at server startup; calls may narrow, never widen it |
| host interface | `iface.workspace.*` | existing create/get/list/sync/cleanup shapes, then typed extensions | Compatibility route for stories |
| MCP | `capsule.project.describe` | `{}` -> sanitized project/capabilities | No secret values or arbitrary host paths |
| MCP | `capsule.definition.inspect|seal` | definition/workspace -> sanitized definition or captured draft | `seal` requires write authority and applies exclusion policy |
| MCP | `capsule.workspace.create|list|status|close` | handle-oriented lifecycle | Create is idempotent by project + requested id + lease owner |
| MCP | `capsule.fs.list|read|search|patch|write` | `{workspace, relative_path, ...}` | Canonicalized beneath the instance; no absolute/`..` escape |
| MCP | `capsule.exec.run` | `{workspace, command_id, args?, timeout?}` | Runs a declared command or policy-approved argv through the executor |
| MCP | `capsule.vcs.status|diff|commit` | `{workspace, ...}` | Local instance only; remote publication is slice 2 |
| MCP | `capsule.ci.run|status|cancel` | `{workspace, pipeline, ...}` | Registered by slice 4, using artifact-job identity |
| trace | `capsule.workspace.*` | lifecycle facts | Consumed by slice 5 |

The MCP method list is a single server capability, but policy can omit whole
families. A read-only review agent might receive project/FS/diff/CI-status tools;
a writer receives patch/exec/commit too; remote sync remains absent unless
explicitly granted.

## The model

```text
checked-in definition + immutable ScopeGrant
                  │
                  ▼
        CapsuleManager.resolve
                  │
             provider.prepare
                  ▼
 WorkspaceInstance + lease + opaque handle
       │             │             │
       ├─ fs.*       ├─ exec.run   ├─ vcs.*
       │             │             │
       └──────────── policy + executor ───────▶ trace/artifacts
```

State is explicit:

```text
declared -> materializing -> ready -> dirty -> committed -> integrated -> closed
                  \-> failed             \-> conflicted
```

Only provider operations change lifecycle state. File and command tools may
change `ready|committed` to `dirty`, but cannot claim integration or closure.
Every mutation carries the expected instance generation so retries are
idempotent and stale callers fail rather than operating on a replacement
workspace.

## Decision recording

Definition resolution, scope checks, lease acquisition, path confinement, and
provider state transitions are deterministic facts. They emit
`capsule.workspace.declared|materializing|ready|changed|committed|failed|closed`
with project id, instance id, generation, lease owner, definition digest,
source/head refs, provider, and policy digest.

An interpretive decision—such as whether a diff is acceptable or how to resolve
a conflict—does not live in the manager. A story invokes an LLM/human decider,
records the normal gate/agent events, and then submits the chosen deterministic
operation. Slice 5 joins those decision refs into the receipt.

## Engine seams & invariants

The manager sits beneath all three front doors:

```text
CLI `kitsoki capsule ...` ─┐
Capsule MCP tools ─────────┼─▶ CapsuleManager ─▶ provider/executor/VCS seams
`iface.workspace.*` ───────┘
```

Load/start-time invariants:

1. the project root is canonical, contains the declared project profile, and is
   not itself writable through the grant unless explicitly opened as an
   instance;
2. every permitted workspace root is either project-derived `.capsules/` state
   or an explicit absolute root in machine-local config;
3. every definition, environment, executor, pipeline, remote, and branch policy
   referenced by the grant exists and loads;
4. an instance path resolves under its provider root and carries both capsule
   provenance and a matching generation/lease record;
5. lease reuse is allowed only for the same owner and definition; takeover is an
   explicit revoke/recover operation with a trace reason;
6. relative FS paths remain below the workspace after symlink evaluation;
7. `exec.run` resolves a declared command id by default; raw argv is opt-in and
   still passes executor/sandbox/network policy;
8. server requests cannot add tools/effects/executors/remotes beyond the startup
   grant;
9. pinned remote source commits are full immutable ids after resolution; cache
   hits do not touch the network, and fetch is an explicit remote-read effect;
10. verifier-only assets never enter the agent-visible filesystem or FS-tool
    namespace and are mounted only for the named verifier call;
11. seal excludes configured secrets, `.git` credentials, caches, runtime
    artifacts, and oversized files unless an operator-approved policy includes
    them.

## Backward compatibility / migration

Migration is adapter-first:

1. load existing `capsules/<name>/capsule.yaml` as definitions and preserve CLI
   output/manifest behavior;
2. implement a `dev-workspace-script` provider that calls the shipped script and
   maps `.kitsoki-dev-workspace.json` into `WorkspaceInstance`;
3. route `host.git_worktree` create/get/status through the manager while keeping
   legacy linked-worktree discovery/cleanup read-only;
4. move clone, manifest, lease, commit, and teardown mechanics into the native
   provider; keep repository-specific bootstrap/promotion as declared hooks;
5. rebind onboarded instances to `host.capsule_workspace`; retain the historical
   host id as an alias for one deprecation window.

The compatibility parser accepts current `.kitsoki-capsule`, `.kitsoki-clone`,
`.kitsoki-dev-workspace.json`, and `.kitsoki-owner` metadata
([`../dev-workspaces.md`](../dev-workspaces.md#workspace-layout)). Native
instances write one versioned manifest with a compatibility projection until
all consumers migrate.

## Tasks

```text
## 1. Contract and native service
- [x] 1.1 Define DefinitionStore, InstanceStore, WorkspaceProvider, ScopeGrant, lease/generation, and lifecycle state types behind DI seams
- [x] 1.2 Discover project definitions under `.kitsoki/capsules/` plus compatible root `capsules/`; validate project/local config boundaries
- [ ] 1.3 Implement synthetic, live/self, and pinned local/remote source providers with content-addressed cache and workspace/verifier overlay visibility
  - Shipped: synthetic, self, and pinned source providers.
  - Remaining: content-addressed source cache and verifier-overlay completion.
- [x] 1.4 Implement native instance metadata and sentinel-gated create/status/close plus policy-filtered capsule seal
- [x] 1.5 Emit capsule.workspace lifecycle facts with stable instance/generation ids

## 2. Front doors
- [ ] 2.1 Route existing `kitsoki capsule list|open|verify|close` through CapsuleManager without output regressions
- [x] 2.2 Add `workspace create|status|commit|close` CLI verbs and JSON schemas
- [x] 2.3 Add `kitsoki capsule mcp --project --pipeline --executor` plus an internal ephemeral-grant input and handle-scoped project/workspace/fs/exec/vcs/cleanup tools
- [x] 2.4 Enforce symlink-safe FS confinement, declared-command/raw-argv policy, request-level narrowing, and secret redaction
- [x] 2.5 Bind artifact-job/job_id when `capsule.ci.run` is registered by slice 4

## 3. Migrate and document
- [x] 3.1 Add dev-workspace-script adapter and a parity suite for create/bootstrap/status/commit/merge/teardown metadata
- [ ] 3.2 Route iface.workspace through the manager; rebind one generated foreign-project instance and Kitsoki dogfood
  - Shipped: `host.capsule_workspace` manager-backed host surface and docs.
  - Remaining: migrate story `workspace` contracts off `name`/`base`/`sync`.
- [ ] 3.3 Move generic clone lifecycle into the native provider; reduce scripts to Kitsoki hooks/compat wrappers
- [ ] 3.4 Update capsule, host, MCP, and onboarding docs; trim/delete this proposal
```

## Verification

No real LLM or remote service is needed:

- open `clean-repo` through CLI, host interface, and MCP and assert equivalent
  normalized instance state;
- launch two leases against one requested id and prove same-owner idempotency,
  different-owner refusal, revoke/recover generation change, and stale-handle
  failure;
- adversarial FS fixtures cover absolute paths, `..`, symlink escape, replaced
  workspace root, and sentinel/manifest mismatch;
- a fake executor proves allowed command, denied command, timeout, cancellation,
  and applied-policy recording;
- migrate `scripts/test-dev-workspace.sh` assertions into a provider parity suite
  before changing the script implementation;
- run existing `internal/capsule`, `internal/capsuletest`, `internal/host`, CLI,
  launch-policy, and no-LLM story flow suites unchanged.

## Open questions

1. **MCP tool granularity** — one `capsule.workspace.call` dispatcher vs. explicit
   tools. *Lean: explicit tools; clients and policy reviewers should see the
   effect boundary without inspecting an `op` string.*
2. **Persistent instance store** — SQLite beside artifact jobs vs. manifest-only
   discovery. *Lean: SQLite index plus versioned manifests as recoverable source
   evidence; rebuild the index from managed roots, like artifact runs.*
3. **Raw command execution** — forbid entirely vs. policy-gated. *Lean:
   declared project commands by default; raw argv only in grants intended for
   implementation agents, still inside executor confinement.*
4. **Definition distribution** — copy project definitions directly vs. publish
   through kits/locks. *Lean: local `.kitsoki/capsules/` first; use the shipped
   kit resolution/lock mechanism for cross-project distribution rather than a
   new registry.*

## Non-goals

- Remote ref reconciliation and publication; slice 2 owns it.
- Environment materialization and remote placement; slice 3 owns them.
- Defining CI graph semantics; slice 4 owns story composition.
- Replacing toolboxes, launch policy, or runtime sandboxing.
- Managing arbitrary directories that were not opened or adopted by a provider.
