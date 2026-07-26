# Typed Story Demo Host

Story Applications use `host.demo` as an app-scoped provider. The provider
accepts graph identity and opaque references, resolves all executable and file
details on the server, and returns only bounded structured data.

## Operations

| Operation | Status | Input | Output or behavior |
| --- | --- | --- | --- |
| `plan` | Implemented | `catalog_path`, `node_id` | `closure_order`, `manifest_ref`, `artifact_handles` |
| `materialize` | Pending typed executor | `catalog_path`, `node_id`, `phase` | Validates the typed request, then returns unavailable |
| `project_mockup` | Implemented projection | `catalog_path`, `node_id`, `audience` | `scenario_ref`, `manifest_ref`; does not create a standalone UI |
| `create_mockup` | Pending typed executor | `manifest_ref` | Resolves the typed manifest, then returns unavailable without writing HTML |
| `record` | Implemented | `manifest_ref` | `record_ref` for a brokered Application Surface capture |
| `doctor` | Implemented | `manifest_ref` | `report`, `ok`, `evidence_ref` |

`phase` is one of `dependencies`, `subject`, or `verify`. `audience` is one of
`internal` or `public`. `catalog_path` is an application-relative catalog
location, not an arbitrary repository or filesystem path.

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
standalone frontend. `materialize` and `create_mockup` retain their typed input
contracts, but production returns explicit unavailable errors until typed
phase-action and Story Application artifact executors exist.

All closures, task lists, reports, reference payloads, artifact counts, and file
sizes have explicit limits. Exceeding a limit fails the operation rather than
returning a truncated result.

The provider uses injected resolver, materializer, mockup creator, capture,
doctor, evidence-store, authorization, and clock dependencies. The implemented
production path is native Go: it resolves graph projections into typed opaque
references, routes `record` through the daemon-owned Application Capture Broker,
and validates existing manifests and their declared artifacts through `doctor`.
It does not create an independent frontend or runtime. The typed provider never
invokes a command, shell, Node process, MJS file, URL, or executable argument.

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
