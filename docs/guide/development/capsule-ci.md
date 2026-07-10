# Capsule CI

Capsule CI is Kitsoki's project-local CI mechanism. A project declares a
pipeline in `.kitsoki/ci.yaml`; the pipeline selects a story, environment, and
policy. The story is the CI graph: deterministic checks, review, agent tasks,
human checkpoints, and refinement are ordinary story rooms—not a second
`steps`/`needs` YAML language.

## Project files

```text
.kitsoki/
├── ci.yaml
├── environments/ci.yaml
└── capsules/              # optional project definitions
```

Existing root `capsules/<id>/capsule.yaml` fixtures remain compatible. A
project definition uses `capsule-definition/v1` and may select `synthetic`,
`self`, or a full-commit `pinned` source. Environment definitions use
`capsule-environment/v1`; `network: none` is the default and host resolution
only probes tools—it never installs software.

## Local lifecycle

```sh
kitsoki capsule workspace create --id change-1 --definition development --owner developer
kitsoki capsule env resolve ci
kitsoki capsule ci plan change --workspace change-1
kitsoki capsule ci run change --workspace change-1
kitsoki capsule ci status
kitsoki capsule ci cancel --job <job-id>
kitsoki capsule cleanup plan --keep-runs 20
kitsoki capsule workspace commit --id change-1 --message "Implement change"
kitsoki capsule workspace integrate --id change-1 --gate "go test ./..." --teardown
```

`capsule ci run` seals source, story, environment, and policy identities into
an execution envelope, invokes the declared story's `run` intent, and accepts
only a matching `capsule-ci-verdict/v1` object in `world.ci_verdict`. A story
that cannot complete safely must emit `needs_input`; the reference
`stories/capsule-ci` does exactly that until a project composes its checks.

Pipeline `executor:` is an immutable placement selection: `host`/`local` runs
through the host provider and `remote-fake` exercises the identical remote
worker protocol without a network. A production remote-worker name must be
declared in the checked-in `remotes:` map; a story cannot select or add one
itself. The shipped `HTTPRemoteWorker` adapter accepts only HTTPS endpoints,
sends the sealed envelope to the worker, receives a serialized typed
verdict/result, and uses an ephemeral authorization header callback. Credential
values never enter the envelope, workspace, trace, or receipt.

Example remote placement:

```yaml
remotes:
  remote-prod:
    endpoint: https://capsule-worker.example
    credential_env: KITSOKI_CAPSULE_WORKER_TOKEN

pipelines:
  change:
    executor: remote-prod
```

`credential_env` stores only the environment variable name in the checked-in
file. If present, the variable must be set by the launching operator process;
the value is read only when the HTTP request is made and is sent as an
authorization header.

The optional `--verdict file.json` run input is for an explicit external story
adapter. It is not an authority bypass: the verdict still has to match the
envelope and promotion eligibility is derived from its outcome and evidence.

## Least-authority agents

Run the standalone server when a coding agent should receive only one scoped
tool surface:

```sh
kitsoki capsule mcp --project /path/to/project --pipeline change --executor local --branch staging/local
```

It exposes opaque workspace handles and project-relative filesystem paths. The
agent cannot name arbitrary host paths, add a remote, obtain credentials, or
widen effects. Available operations include workspace/FS/declared-command/local
VCS actions, environment resolution, local reconciliation, and CI plan/run/
status, plus project-scoped cleanup planning/apply. `--branch` is required for
each reconciliation target; omitting it denies synchronization while retaining
the rest of the tool surface. Remote publication is intentionally not part of
this grant.

## Environments and remote placement

`capsule env resolve|lock|verify` creates and verifies content-addressed
environment locks. The executor contract supports host and remote providers;
the repository ships host, deterministic fake-remote, and checked-in
HTTPS-remote selection. Remote workers own source materialization and story
execution for the sealed envelope they receive; the local controller validates
the returned typed verdict against that envelope before persisting receipts.

## Compatibility

Kitsoki's checked-in `development` definition is the required protected
development workflow. Use the native `capsule workspace` commands above; do
not run the underlying `scripts/dev-workspace.sh` lifecycle directly. The
compatibility provider intentionally preserves the script's clone,
branch-target, bootstrap, rebase, and primary-checkout safeguards while the
generic Capsule API remains available to every project with `.kitsoki/`.

## Receipts and promotion

Capsule receipts are canonical `capsule-ci-receipt/v1` projections over a
sealed envelope, typed verdict, artifacts, and trace custody digest. A receipt
must verify, be promotion eligible, and bind to the promotion plan's exact
candidate before it can authorize promotion; a green-looking story response
alone cannot.

Local `integrate`, `refresh`, and `promote` reconciliation plans update refs
only through stale-safe fast-forward checks. `publish` is deliberately separate:
a publish plan carries the `remote_publish` effect and cannot apply through the
local Git reconciler. A remote publication provider must be explicitly granted
and injected before a publish plan can be applied.

When a stored plan is diverged, materialize the deterministic conflict input
for a resolver/reviewer story before attempting continuation:

```sh
kitsoki capsule sync conflicts --plan <digest>
```

The command writes a `capsule-sync-conflict/v1` artifact under
`.capsules/sync/` with the merge base, candidate/target changed paths, overlap
paths, required story inputs, and continuation token. Managed integration
instance creation and continuation apply remain separate runtime work.
Agents limited to Capsule MCP use `capsule.sync.conflicts` for the same
operation; it accepts only a server-owned plan digest and returns only the
project-relative artifact path.

For credential-free local development and tests, `capsule sync apply` can inject
a local bare-remote publisher:

```sh
kitsoki capsule sync apply --plan <digest> --local-bare-remote /path/to/origin.git
```

That provider checks the live bare-remote ref still matches the plan's expected
target before pushing the candidate commit. Production Git/PR publication is a
separate provider/grant path.

See [Capsule CI receipts](../../tracing/capsule-ci-receipts.md).

## Ongoing Cleanup

Capsule CI writes useful local evidence: run records, receipt sidecars, compact
controller traces, managed workspaces, and optional caches. Treat cleanup as a
normal part of the CI lifecycle, especially on developer machines where Go
build caches and repeated workspace runs can consume tens of gigabytes.

Start with a dry-run plan:

```sh
kitsoki capsule cleanup plan --keep-runs 20
```

By default this only proposes old `.capsules/ci` run bundles beyond the newest
retained run records. Project caches and Go build/test cache cleanup are
explicit:

```sh
kitsoki capsule cleanup plan --include-capsule-cache --include-go-build-cache
kitsoki capsule cleanup apply --include-capsule-cache --include-go-build-cache
```

The apply path removes only planned Capsule-managed project paths. Go build
cache cleanup goes through `go clean -cache -testcache` rather than deleting an
arbitrary directory, because that cache may live outside the project root.

Agents limited to `kitsoki capsule mcp` use `capsule.cleanup.plan` for a
path-redacted hygiene dry run and `capsule.cleanup.apply` when their immutable
startup grant includes the `cleanup` effect. MCP cleanup intentionally stays
inside the project Capsule tree and does not clear host-global Go caches.
