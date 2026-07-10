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
kitsoki capsule workspace create --id change-1 --definition clean-repo
kitsoki capsule env resolve ci
kitsoki capsule ci plan change --workspace change-1
kitsoki capsule ci run change --workspace change-1
kitsoki capsule ci status
kitsoki capsule ci cancel --job <job-id>
kitsoki capsule workspace close --id change-1
```

`capsule ci run` seals source, story, environment, and policy identities into
an execution envelope, invokes the declared story's `run` intent, and accepts
only a matching `capsule-ci-verdict/v1` object in `world.ci_verdict`. A story
that cannot complete safely must emit `needs_input`; the reference
`stories/capsule-ci` does exactly that until a project composes its checks.

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
status. `--branch` is required for each reconciliation target; omitting it
denies synchronization while retaining the rest of the tool surface. Remote
publication is intentionally not part of this grant.

## Environments and remote placement

`capsule env resolve|lock|verify` creates and verifies content-addressed
environment locks. The executor contract supports host and remote providers;
the repository currently ships host plus a deterministic fake remote worker for
offline parity tests. A credentialed production remote-worker adapter is not
yet enabled by these commands.

## Compatibility

`scripts/dev-workspace.sh` remains the required workflow for Kitsoki's
protected development checkout while its branch-target/bootstrap/merge parity
adapter is completed. The native `capsule workspace` commands are the generic
project API; do not replace the protected-checkout staging workflow with ad hoc
Git operations.

## Receipts and promotion

Capsule receipts are canonical `capsule-ci-receipt/v1` projections over a
sealed envelope, typed verdict, artifacts, and trace custody digest. A receipt
must verify, be promotion eligible, and bind to the promotion plan's exact
candidate before it can authorize promotion; a green-looking story response
alone cannot. See [Capsule CI receipts](../../tracing/capsule-ci-receipts.md).
