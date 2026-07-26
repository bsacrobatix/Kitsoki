# Typed Story Demo Host

Story Applications use `host.demo` as an app-scoped provider. The provider
accepts graph identity and opaque references, resolves all executable and file
details on the server, and returns only bounded structured data.

## Operations

| Operation | Status | Input | Output or behavior |
| --- | --- | --- | --- |
| `plan` | Implemented | `node_id` | `closure_order`, `manifest_ref`, `artifact_handles` |
| `materialize` | Implemented in configured daemon | `node_id`, `phase` | `evidence_ref`, opaque `artifact_handles` |
| `project_mockup` | Implemented projection | `node_id`, `audience` | `scenario_ref`, `manifest_ref`; does not create a standalone UI |
| `create_mockup` | Implemented in configured daemon | `manifest_ref` | `mockup_ref`, verified `bundle_ref`, opaque `artifact_handles` |
| `record` | Implemented | `manifest_ref` | `record_ref` for a brokered Application Surface capture |
| `doctor` | Implemented | `manifest_ref` | `report`, `ok`, `evidence_ref` |

`phase` is one of `dependencies`, `subject`, or `verify`. `audience` is one of
`internal` or `public`. The caller's catalog is selected by daemon
configuration; no operation accepts a catalog or repository path.

The input contract deliberately has no command, script, executable arguments,
repository path, URL, renderer, environment, secret, or credential fields.
Unknown fields fail before graph resolution.

## Authority And References

Every typed call requires an authenticated actor and the exact app identity and
root bound during runtime construction. Opaque references use an app-scoped
content address:

```text
kitsoki://story-demo/<app-scope>/<kind>/sha256/<digest>
```

The backing records live under `.artifacts/story-demo/references/`. They contain
private server paths when needed, but those records are never returned to the
Story. A reference from another app scope is rejected.

Receipts are immutable and keyed by semantic input. Completed capture and doctor
operations are recovered after process restart instead of repeating side
effects. Concurrent calls in one runtime are serialized, while content-addressed
publication prevents partial or overwritten records across runtimes.

## Server Resolution

`plan` loads the graph catalog within the bound application root, resolves the
node's materialize binding, orders dependencies from declared `source_edge`
parameters, and resolves the manifest from a declared `source_field`.
`project_mockup` resolves a bounded scenario and typed mockup manifest from the
graph. It publishes those values as opaque references; it does not emit a
standalone frontend. `materialize` sends only the configured catalog alias,
node ID, and a digest of the server-resolved phase context to an exact
registered Story Application. `create_mockup` sends only scenario identity,
digest, and bounded action IDs. Neither sends private paths or raw scenario
payloads.

All closures, task lists, reports, reference payloads, artifact counts, and file
sizes have explicit limits. Exceeding a limit fails the operation rather than
returning a truncated result.

The provider uses injected resolver, application artifact executor, capture,
doctor, evidence-store, authorization, and clock dependencies. The daemon
executor creates or reattaches a deterministic durable artifact job/session,
invokes exact exported JSON-RPC handlers through `application.Service`, and
relies on application-scoped replay plus canonical receipts. A restart first
records interruption truth, then reattaches the persisted producer session.
Writeback failure fails the operation.

`create_mockup` consumes an immutable bundle already published by the generic
`applicationbuild` pipeline from story-owned finite components. Runtime
execution only verifies and addresses that bundle; it never builds, copies, or
generates HTML. The typed provider and executor never invoke a command, shell,
subprocess, Node process, MJS file, URL, or executable argument.

## Daemon Configuration

`story_application_artifacts` is keyed by the calling application. Each entry
fixes the private catalog path and alias, one bundle-backed mockup producer, and
all three materialization phase plans:

```yaml
story_application_artifacts:
  review:
    catalog: catalog/product.yaml
    catalog_ref: product
    create_mockup:
      application_id: artifact-producer
      bundle: true
      primary_output: mockup_ref
      phases:
        - id: produce
          handler: artifact-producer.produce
          artifact_outputs: [mockup_ref]
    materialize:
      dependencies: &artifact-phase
        application_id: artifact-producer
        primary_output: artifact_ref
        phases:
          - id: materialize
            action: artifact-producer.materialize
            artifact_outputs: [artifact_ref]
      subject: *artifact-phase
      verify: *artifact-phase
```

Configuration is validated before the daemon starts. Runtime resolution also
requires one exact registered producer, exported handler-backed operations,
JSON-RPC exposure, and application-scoped idempotency for effectful handlers.
Ordinary web/TUI runtimes and unconfigured callers fail closed.

Capture targets an attached Application Surface by application ID and actor and
excludes the Story engine session that requested the capture. Exactly one live
matching non-origin surface must exist. Stale surface leases are pruned; zero or
multiple live matches fail closed because the current typed request has no exact
target-session selector.

## Legacy Compatibility

`internal/host.DemoHandler` retains the old `create`, `record`, and `doctor`
path-based behavior for compatibility and is explicitly deprecated. Normal
Story Application runtime construction replaces that registration with the
typed provider, so a Story cannot reach the legacy path or renderer arguments.
