# Capsule MCP authority boundary

`kitsoki capsule mcp` is a standalone MCP server for a single project. Its
startup `ScopeGrant` is immutable: a request can narrow authority but cannot add
workspace roots, definitions, executors, effects, branches, remotes, or tools.

The server returns opaque `{id,generation}` handles. Every mutation requires
the current generation; stale handles fail. Filesystem operations accept only
relative paths and resolve symlinks beneath the managed workspace. Declared
commands run argv-only; raw argv requires both definition and grant approval.

The exposed effect families are intentionally distinct:

- FS, declared execution, and local commit affect one managed workspace;
- environment lock persistence requires `env_write`;
- local reconciliation requires `local_reconcile` and uses a stable plan/apply
  digest, and may target only a branch granted at server startup; and
- remote publication remains absent; remote CI execution is available only when
  the checked-in CI config names a remote executor and the launching process
  supplies its credential environment.

Verifier-only overlays and secret values are not placed in the agent-visible
filesystem or MCP response payloads. Lifecycle operations emit deterministic
Capsule facts for receipt/tracing consumers.

Start reconciliation authority narrowly, for example:

```sh
kitsoki capsule mcp --project /path/to/project --branch staging/local
```

Omitting `--branch` still permits workspace and CI operations but denies all
`capsule.sync.plan` calls. A required promotion gate additionally verifies the
persisted receipt, its run projection, and its exact candidate source digest.
