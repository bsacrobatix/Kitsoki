# Flow Evidence Host

`host.flow_evidence` records deterministic Kitsoki flow results for one
application-scoped catalog node. Its entire story-facing contract is:

```text
record(node_id) -> evidence_ref, passed, run_count
```

It does not accept commands, programs, argument vectors, scripts, suite paths,
output paths, model profiles, or arbitrary repository roots. It does not launch
an LLM and does not grant graph mutation or source-landing authority.

## Configuration

The checked-in config declares repository-relative authority and every hard
bound. Story input cannot override any field in this block:

```yaml
story_application_assurance:
  pog-application:
    catalog: pog/catalog.yaml
    compliance:
      max_checks: 64
      max_resolved_bytes: 131072
      max_evidence_bytes: 262144
    flow_evidence:
      suites:
        - id: pog-application
          app: stories/pog-application/app.yaml
          flows: stories/pog-application/flows/*.yaml
          version: v1
      max_suites: 16
      max_runs: 200
      max_suite_bytes: 8388608
      max_evidence_bytes: 1048576
```

Web config parsing uses strict nested allowlists. Runtime construction resolves
the catalog, suite application, and flow glob beneath the discovered project
root, follows symlinks, and requires every resolved input to be a regular file.
It then injects the loaded application's ID, author, version, authenticated
actor, deterministic runner, SQLite/Postgres evidence store, and configured
bounds. The resolver receives only this server scope and the validated catalog
node ID. Server-resolved suite paths are never returned to the caller.

Unknown input is rejected. Authority-bearing keys are rejected recursively,
including path, URL, command, program, script, provider, profile, actor,
session, transport, bound, catalog, suite, evidence, runner, store, and
application variants. An unregistered application keeps the unavailable
sentinel unless a low-level integration explicitly registers the legacy
path-taking provider.

## Deterministic execution

The production runner calls `testrunner.RunFlows` in process with
`DeterministicOnly`. This posture rejects fixture `host_bindings` and cassette
recording modes, so the evidence path cannot activate real host providers,
shell commands, network recording, or live agents. Replay cassettes and stub
handlers remain available for deterministic flow fixtures.

Configuration must state positive limits within the platform ceilings of 16
suites, 200 total flow runs, 8 MiB of suite input, 200 failure messages per
suite result, and a 1 MiB durable evidence record. Limits fail before execution
where they can be precomputed. Oversized results fail rather than returning or
storing a truncated success.

Every red flow and runner failure is represented in the stored suite record.
The host returns `passed: false` with its durable evidence reference; it does
not turn an ordinary red suite into an infrastructure error. Resolver, store,
scope, and malformed-runner failures fail closed.

## Replay And Restart

The host hashes the resolved application scope, catalog revision, node ID, and
sorted suite definitions into a stable evidence key and reference. It checks
the evidence store before running. A matching stored pass or failure is replayed
without executing the suites again.

Concurrent calls for the same key are serialized in process. SQLite and
Postgres stores use an atomic insert-if-absent so multiple daemon processes
converge on the same first durable record. A new handler after daemon restart
uses the same store lookup and returns that receipt. Suite revisions include
the configured version and digests of the application and matched fixtures;
catalog content changes likewise change catalog revision.

If the store is unavailable, the host does not run. If persistence fails after
a run, the operation fails and does not claim evidence exists.
