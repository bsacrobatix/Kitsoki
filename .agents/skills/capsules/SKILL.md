---
name: capsules
description: Use Kitsoki hermetic capsules for deterministic repository/workspace fixtures. Trigger when adding or migrating Go tests, story fixtures, MCP/tool tests, workflow validation, or agent QA that needs a reusable git repo state; when you see ad hoc `git init`, repeated `t.TempDir()` repo setup, rebase/conflict/dirty-index setup, or instructions to make capsule usage exclusive.
---

# Capsules

Capsules are the default way to create reusable git/workspace fixtures in
Kitsoki. Prefer them over hand-written temp repo setup when the test needs a
known state rather than testing repo creation itself.

## Workflow

1. Check `capsules/<name>/capsule.yaml` before creating a new fixture.
2. In Go tests, open fixtures with:

```go
repo := capsuletest.Open(t, "clean-repo")
```

3. Add `kitsoki/internal/capsuletest` only in tests; production code should use
   `internal/capsule` or the CLI.
4. Author new reusable fixtures under `capsules/<name>/capsule.yaml` with
   `source.synthetic: true`, `network: none`, and verification probes.
5. Run or recommend focused validation for changed packages and
   `go run ./cmd/kitsoki capsule verify <name>` for new capsules.

## When not to use a capsule

Keep bespoke setup when the behavior under test is:

- creating or bootstrapping a repository;
- asserting the exact git commands issued through a fake runner;
- a one-off filesystem/database/cache temp directory that is not a repo state;
- a gated external corpus or local-only mirror test.

## Useful commands

```sh
go run ./cmd/kitsoki capsule open clean-repo
go run ./cmd/kitsoki capsule verify clean-repo
go run ./cmd/kitsoki capsule close /tmp/opened-capsule
```

## Existing starter capsules

Use `clean-repo`, `rebase-conflict-ready`, `mid-rebase-conflict`,
`dirty-index`, `stale-worktree`, and `diverged-remote` for common git states.
