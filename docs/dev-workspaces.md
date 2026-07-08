# Managed development workspaces

Kitsoki development work happens in managed clone-backed capsule workspaces, not
Git linked worktrees. The primary checkout is protected as read-mostly; agents
and operators should delegate workspace lifecycle operations to
`scripts/dev-workspace.sh` instead of running `git clone`, `git worktree`,
rebase, merge, or teardown commands by hand.

The script is the contract. It creates an isolated clone, writes Kitsoki
ownership metadata, optionally bootstraps the clone, commits the finished work,
lands it through the protected-main merge helper, and removes the workspace when
requested.

## Quick path

From the primary checkout:

```sh
scripts/dev-workspace.sh create --id docs-example --branch agent/docs-example --base main --bootstrap
scripts/dev-workspace.sh status docs-example
```

Do the implementation inside the reported workspace path, then:

```sh
scripts/dev-workspace.sh commit docs-example --message "Document managed workspaces"
scripts/dev-workspace.sh merge docs-example --gate "go test ./internal/host" --teardown
```

For docs-only changes, the merge gate can be a focused committed-diff check
such as `git show --check --format= HEAD` plus any story flow or package test
affected by the edit. Before committing, `git diff --check` is still useful for
checking the working tree. Always prefer the narrowest gate that proves the
changed behavior.

## Workspace layout

By default, workspaces live under:

```text
<repo>/.capsules/workspaces/<id>
```

Each workspace is a normal Git clone of the local source checkout with the
remote named `source`. The script writes these unmanaged-by-Git metadata files
inside the clone:

| File | Purpose |
|---|---|
| `.kitsoki-capsule` | Sentinel that marks the directory as a Kitsoki capsule workspace. |
| `capsule-manifest.json` | Capsule-compatible provenance: source repo, source commit, workspace id, base, branch, target, and session id. |
| `.kitsoki-clone` | Clone lifecycle metadata consumed by host cleanup/listing code. |
| `.kitsoki-dev-workspace.json` | Script-specific manifest used by `status`, `commit`, `merge`, and `teardown`. |
| `.kitsoki-owner` | Compatibility owner marker when a session id is supplied. |

The script also appends those paths to `.git/info/exclude` in the clone, so the
metadata is local provenance rather than project source.

## Commands

### `create`

```sh
scripts/dev-workspace.sh create --id <id> --branch <branch> --base <base> [--bootstrap] [--json]
```

`id` is a single path segment and becomes the workspace directory name.
`branch` defaults to `agent/<id>`. `base` defaults to `main`. `--bootstrap`
runs `make bootstrap-workspace` inside the clone after checkout.

Use `--session-id <id>` from story/runtime code so repeated entry by the same
session is idempotent while a different session is refused instead of being
handed another session's workspace.

### `bootstrap`

```sh
scripts/dev-workspace.sh bootstrap <workspace-or-id>
```

Bootstrap prepares embed-only story/SPA assets, installs the runstatus
dependencies, and warms the Go build cache. `create --bootstrap` is the normal
path when a workspace will run `go run ./cmd/kitsoki`, Playwright specs, or web
UI tooling. Pure documentation edits usually do not need it.

### `status`

```sh
scripts/dev-workspace.sh status <workspace-or-id> [--json]
```

Use `status` to find the path, current branch, short HEAD, and dirty state. For
agent sessions, this is the supported replacement for manually inspecting linked
worktree registries.

### `commit`

```sh
scripts/dev-workspace.sh commit <workspace-or-id> --message "<message>"
```

`commit` stages the full workspace delta and commits it on the workspace branch.
It refuses a clean workspace. Commit messages should describe the completed
slice, not the mechanics of the workspace.

### `merge`

```sh
scripts/dev-workspace.sh merge <workspace-or-id> --gate "<focused validation>" --teardown
```

`merge` requires the primary checkout to be on the target branch and locally
available. It refuses dirty workspaces and refuses primary-checkout dirt only
when that dirt overlaps files the workspace branch will update. Unrelated dirty
primary files are preserved. The command fetches the current target into the
workspace, rebases the workspace branch onto it, runs the optional gate inside
the workspace, imports the branch into the primary checkout as a temporary
`capsule/<id>-land` branch, and lands it through `scripts/merge-to-main.sh`.

If the rebase conflicts, resolve the conflict inside the managed workspace,
rerun the focused validation, then rerun `merge`. Do not resolve by editing the
primary checkout.

### `close` / `teardown`

```sh
scripts/dev-workspace.sh teardown <workspace-or-id>
scripts/dev-workspace.sh teardown <workspace-or-id> --force
```

Teardown refuses unmanaged directories and dirty workspaces. Use `--force` only
when intentionally discarding uncommitted local state.

## Story/runtime integration

The `workspace` host interface still uses the historical provider name
`host.git_worktree`, but default creation is script-backed:

- `workspace.create` and `workspace.clone_create` call
  `scripts/dev-workspace.sh create`.
- New workspaces default to `.capsules/workspaces/<id>`.
- Legacy linked worktree list and cleanup remain so older local checkouts can be
  inspected and removed.
- Bugfix and implementation stories derive session-scoped workspace ids such as
  `bf-<ticket>-<session>` and bind `world.workdir` from the script's returned
  `path`.

The old `worktree_path` world key still exists in some story contracts as a
field name. Treat it as a compatibility field for "the isolated workspace path",
not a direction to create a Git linked worktree.

## Recovery rules

- **Existing workspace, same session:** `create` is idempotent and returns the
  existing path when branch and owner metadata match.
- **Existing workspace, different session:** `create` refuses reuse. Pick a
  distinct id or close the old session/workspace deliberately.
- **Primary checkout has overlapping dirt:** `merge` refuses to land when a
  dirty primary path overlaps a file the workspace branch would update. Preserve
  or commit that primary change separately before retrying. Dirty primary files
  outside the workspace branch's changed paths are left alone and do not block
  landing.
- **Workspace is dirty:** `merge` refuses to land. Commit or discard the
  workspace changes first.
- **Target advanced:** `merge` rebases the workspace onto the current local
  target before running the gate.
- **Gate fails:** fix the workspace, recommit or amend there, and rerun `merge`.
- **Workspace no longer needed:** run `teardown`; do not leave merged workspaces
  behind.

## Validation

The lifecycle script has a focused shell regression test:

```sh
scripts/test-dev-workspace.sh
```

Host integration is covered by:

```sh
go test ./internal/host
```

When story provisioning changes, use no-LLM flow tests:

```sh
go run ./cmd/kitsoki test flows stories/bugfix/app.yaml --flows <fixture.yaml>
go run ./cmd/kitsoki test flows stories/dev-story/app.yaml --flows <fixture.yaml>
go run ./cmd/kitsoki test flows stories/implementation/app.yaml --flows <fixture.yaml>
```
