# Flow Evidence Host

`host.flow_evidence` records deterministic Kitsoki flow results for one
application-scoped catalog node. Its entire story-facing contract is:

```text
record(catalog_path, node_id) -> evidence_ref, passed, run_count
```

It does not accept commands, programs, argument vectors, scripts, suite paths,
output paths, model profiles, or arbitrary repository roots. It does not launch
an LLM and does not grant graph mutation or source-landing authority.

## Resolution

Daemon construction registers one exact catalog binding and four injected
dependencies for an application:

```go
err := registry.RegisterFlowEvidenceProvider(appID, host.FlowEvidenceProvider{
    CatalogPath: registeredCatalogPath,
    Resolver:    appCatalogResolver,
    Runner:      newTestrunnerFlowEvidenceRunner(buildImportResolver()),
    Store:       durableEvidenceStore,
    Clock:       clock.Real(),
})
```

Session construction adds the loaded application's ID, author, and version.
`record` requires an authenticated actor and rejects any `catalog_path` other
than the exact registered value. The path is compared as an identity claim; it
is never opened from host input. The resolver receives only this server scope
and the validated catalog node ID. It returns the node's catalog revision and
declared flow suite IDs, revisions, application paths, and fixture selectors.
Those server-resolved suite paths are never returned to the caller.

There is no default resolver, store, or catalog binding. An unregistered
application gets the unavailable sentinel.

## Deterministic execution

The production runner calls `testrunner.RunFlows` in process with
`DeterministicOnly`. This posture rejects fixture `host_bindings` and cassette
recording modes, so the evidence path cannot activate real host providers,
shell commands, network recording, or live agents. Replay cassettes and stub
handlers remain available for deterministic flow fixtures.

Execution is bounded to 16 suites, 200 total flow runs, 8 MiB of fixture input,
200 failure messages per suite result, and a 1 MiB durable evidence record.
Limits fail before execution where they can be precomputed. Oversized results
fail rather than returning or storing a truncated success.

Every red flow and runner failure is represented in the stored suite record.
The host returns `passed: false` with its durable evidence reference; it does
not turn an ordinary red suite into an infrastructure error. Resolver, store,
scope, and malformed-runner failures fail closed.

## Replay And Restart

The host hashes the resolved application scope, catalog revision, node ID, and
sorted suite definitions into a stable evidence key and reference. It checks
the evidence store before running. A matching stored pass or failure is replayed
without executing the suites again.

Concurrent calls for the same key are serialized in process.
`PutFlowEvidenceIfAbsent` must also be atomic so multiple daemon processes
converge on the same first durable record. A new handler after daemon restart
uses the same store lookup and returns that receipt. Catalog resolvers must
change the catalog or suite revision whenever executable content changes;
otherwise replay correctly treats the declaration as unchanged.

If the store is unavailable, the host does not run. If persistence fails after
a run, the operation fails and does not claim evidence exists.
