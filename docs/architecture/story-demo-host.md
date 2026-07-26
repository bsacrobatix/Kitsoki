# Typed Story Demo Host

Story Applications use `host.demo` as an app-scoped provider. The provider
accepts graph identity and opaque references, resolves all executable and file
details on the server, and returns only bounded structured data.

## Operations

| Operation | Input | Output |
| --- | --- | --- |
| `plan` | `catalog_path`, `node_id` | `closure_order`, `manifest_ref`, `artifact_handles` |
| `materialize` | `catalog_path`, `node_id`, `phase` | `evidence_ref`, `artifact_handles` |
| `project_mockup` | `catalog_path`, `node_id`, `audience` | `scenario_ref`, `manifest_ref` |
| `create_mockup` | `manifest_ref` | `mockup_ref`, `artifact_handles` |
| `record` | `manifest_ref` | `record_ref` |
| `doctor` | `manifest_ref` | `report`, `ok`, `evidence_ref` |

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

Receipts are immutable and keyed by semantic input. Completed materialization,
mockup creation, capture, and doctor operations are recovered after process
restart instead of repeating side effects. Concurrent calls in one runtime are
serialized, while content-addressed publication prevents partial or overwritten
records across runtimes.

## Server Resolution

`plan` loads the graph catalog within the bound application root, resolves the
node's materialize binding, orders dependencies from declared `source_edge`
parameters, and resolves the manifest from a declared `source_field`.
`materialize` evaluates the server-resolved manifests and artifact declarations
for dependency, subject, and verification phases. It does not interpret
catalog text as executable code.

All closures, task lists, reports, reference payloads, artifact counts, and file
sizes have explicit limits. Exceeding a limit fails the operation rather than
returning a truncated result.

The provider uses injected resolver, materializer, mockup creator, capture,
doctor, evidence-store, authorization, and clock dependencies. The production
implementation is native Go: it projects a static application mockup, writes a
typed manifest, registers its server-resolved artifact bundle (including
existing rrweb JSON when declared), and validates the manifest and artifacts.
The typed provider never invokes a command, shell, Node process, MJS file, URL,
or executable argument.

## Legacy Compatibility

`internal/host.DemoHandler` retains the old `create`, `record`, and `doctor`
path-based behavior for compatibility and is explicitly deprecated. Normal
Story Application runtime construction replaces that registration with the
typed provider, so a Story cannot reach the legacy path or renderer arguments.
