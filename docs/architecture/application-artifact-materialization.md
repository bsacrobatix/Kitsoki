# Application artifact materialization

Graph types can bind artifact production to exported operations on a registered
Story Application:

```yaml
materialize:
  application_id: evidence-recorder
  context_edges: [implemented_by]
  gates: [owner]
  phases:
    - id: record
      handler: evidence.record
      artifact_outputs: [evidence_ref]
    - id: publish
      action: evidence.publish
      artifact_outputs: [artifact_ref]
```

This typed form is mutually exclusive with the legacy `materialize.story`
driver. It also rejects legacy `params` and script `checks`. Verification that
belongs to a typed materializer must be another exported handler or action
phase.

## Authority boundary

The daemon resolves `application_id` exactly and uniquely against its registered
story catalog. A path-based story session cannot authorize a typed binding.
Before scheduling, Kitsoki verifies that every handler or action is exported by
the resolved application and that handlers are exposed over JSON-RPC. Actions
must be handler-backed; intent-backed actions are not valid materialization
phases.

Each phase receives exactly:

```json
{
  "catalog_ref": "product",
  "node_id": "app-one",
  "context_digest": "sha256:..."
}
```

`catalog_ref` is the allowlisted RPC alias, not its resolved filesystem path.
Graph-authored phases cannot supply commands, scripts, URLs, paths, literal
inputs, actors, session IDs, transports, or idempotency keys. The daemon fixes
the actor to `graph.materialize:<node-id>`, uses the live registered session,
invokes through the shared Application registry over JSON-RPC, and derives a
stable idempotency key from application ID, node ID, phase ID, and context
digest.

Write and external handlers must declare required idempotency with
`scope: application`. The server shares one durable application replay journal
under its materialization artifact root across all short-lived sessions and
reconstructs it after restart. An injected application replay dependency may
replace that journal. Effectful typed materialization fails closed when neither
durable surface is available; read-only phases do not require replay.

## Results

Every declared `artifact_outputs` field is phase-local and must contain an
opaque handle such as `flow-evidence:<digest>`. Each successful phase must also
return a canonical `application-receipt/v1` ID. Raw application output is not
persisted or returned by materialization. A declaration is bounded to 32 phases,
16 artifact outputs per phase, and 512 bytes per handle.

Typed job results, status responses, SSE artifact frames, graph evidence, and
materialization writeback expose only opaque artifact handles and canonical
receipt IDs. Resolved catalog, story, and artifact paths remain server-private.
Failure to persist phase evidence or the terminal materialization record fails
the job; an in-memory application outcome is never reported as completed durable
materialization.

The same generic executor backs configured `host.demo.materialize` and
`host.demo.create_mockup` operations. Their caller-to-producer bindings live in
daemon-owned `story_application_artifacts` configuration, not story input.
Mockup creation additionally requires a verified immutable bundle previously
published by `applicationbuild`; the runtime executor never starts Vite or
generates frontend files.
