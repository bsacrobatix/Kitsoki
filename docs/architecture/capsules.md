# Hermetic capsules

Capsules are deterministic, local materializations of git/development states.
They give tests, story fixtures, and future benchmark runners one shared way to
say: open this repository state, prove it matches, run against it, then tear it
down.

The shipped v1 is intentionally narrow and local-only:

- `internal/capsule` loads `capsules/<name>/capsule.yaml`, validates the schema,
  materializes synthetic git repositories with fixed author/date identity, writes
  `capsule-manifest.json`, verifies tree digests and probes, and sentinel-gates
  cleanup.
- `internal/capsuletest.Open(t, name)` opens capsules under `t.TempDir()` and
  registers cleanup for Go tests.
- `kitsoki capsule open|verify|close` exposes the same behavior from the CLI.
- Starter capsules cover `clean-repo`, `rebase-conflict-ready`,
  `mid-rebase-conflict`, `dirty-index`, `stale-worktree`, and
  `diverged-remote`.

Remote pinned repositories, environment image capture, `seal`, flow-test capsule
bindings, agent workspace providers, product-journey adoption, and Harbor import
/export are follow-on slices. v1 does not perform live network fetches and does
not install tools on the host.

## Capsule spec

A capsule is a directory containing `capsule.yaml`:

```yaml
name: clean-repo
source:
  synthetic: true
  default_branch: main
  steps:
    - action: write
      path: a.txt
      content: |
        a
    - action: commit
      message: init
network: none
verify:
  probes:
    - name: clean-tree
      run: git diff --quiet && git diff --cached --quiet
      expect: zero
scenario:
  kind: git-flow
```

Supported synthetic actions are `write`, `mkdir`, `remove`, `commit`,
`checkout`, `branch`, `git`, and `bare_remote`. Every git command runs with
fixed identity and dates so commit topology is reproducible.

`verify.tree_digest` may pin the expected `sha256:<digest>` of the materialized
working files. When omitted, verification still reports the actual digest in the
manifest and CLI output. Probes are shell commands run inside the workspace with
`expect: zero` or `expect: nonzero`.

## CLI

Open a capsule into a temp directory:

```sh
go run ./cmd/kitsoki capsule open clean-repo
```

Open into a specific empty directory:

```sh
go run ./cmd/kitsoki capsule open clean-repo --dest /tmp/clean-capsule
```

Verify a spec by opening a disposable workspace:

```sh
go run ./cmd/kitsoki capsule verify clean-repo
```

Verify an already-open workspace:

```sh
go run ./cmd/kitsoki capsule verify /tmp/clean-capsule
```

Close only removes a directory with a capsule sentinel:

```sh
go run ./cmd/kitsoki capsule close /tmp/clean-capsule
```

## Go tests

```go
repo := capsuletest.Open(t, "clean-repo")
```

The helper returns a real git checkout and removes it during test cleanup. Use it
instead of bespoke `git init` fixtures when the desired state belongs in the
shared capsule library.

## Manifest

Every open writes `capsule-manifest.json` in the workspace. It records the spec
path, workspace, opened time, source type, HEAD, branch, network mode, tree
digest, and probe results after verification. Consumers should persist that
manifest as provenance when a capsule-backed run produces artifacts.
