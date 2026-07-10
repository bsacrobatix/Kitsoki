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
  digest; and
- remote publication and credentials are absent unless a future explicit grant
  and provider are configured.

Verifier-only overlays and secret values are not placed in the agent-visible
filesystem or MCP response payloads. Lifecycle operations emit deterministic
Capsule facts for receipt/tracing consumers.
