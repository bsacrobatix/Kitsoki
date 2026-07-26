# Application jobs

`host.application_job` lets one registered Story Application start a bounded
background event owned by another registered application. It is a daemon-only
adapter over the existing Application Event runtime and per-session job
scheduler; it does not introduce a second scheduler or a general command
runner.

## Deployment contract

The daemon owns all target authority:

```yaml
story_application_jobs:
  caller-application:
    publish-report:
      application_id: report-producer
      event: publish
      artifact_outputs: [report_ref]
      primary_output: report_ref
      bounds:
        max_input_bytes: 65536
        max_runtime_seconds: 900
```

The first map key is the exact calling `app.id`. The second is the public
template name. `application_id` resolves exactly and uniquely against the
registered story catalog. `event` must name that application's exact
`mode: background` event. `artifact_outputs` is the finite allowlist projected
from the completed event outcome, and `primary_output` must be one member of
that list.

Input and runtime limits are required. Configuration is bounded to 64 callers,
64 templates per caller, 16 artifact output fields per template, 256 KiB of
input, and 24 hours of runtime.

## Public host

The host has three operations:

```yaml
- invoke: host.application_job.submit
  with:
    template: publish-report
    input: {node_id: report-one}

- invoke: host.application_job.status
  with: {job_ref: aj_...}

- invoke: host.application_job.cancel
  with: {job_ref: aj_...}
```

Submit accepts only `{template,input}`. Status and cancel accept only
`{job_ref}`. Unknown root fields fail closed. Input is checked recursively and
cannot supply application, event, session, child job, filesystem path, URL,
command, provider, actor, or transport authority. This includes normalized
forms such as `session-id`, `session_id`, and nested occurrences.

The public result contains only:

- stable `job_ref`;
- normalized status;
- configured opaque `artifact_handles` and optional `primary`;
- a fixed privacy-safe reason code when applicable; and
- a canonical `kitsoki/application-job-receipt/v1` receipt.

Application routes, session IDs, scheduler child IDs, story paths, raw
application output, and arbitrary error strings are private.

## Execution and durability

Submission normalizes the JSON input and derives a stable application-scoped
artifact-job reference from caller, template, and input digest. A replay of the
same request returns the existing reference and does not dispatch a second
event.

The adapter opens the configured application through the registered catalog,
verifies the exact background event, and dispatches through
`application.Service.DispatchEvent`. Its private record binds the public
artifact-job reference to the existing event scheduler's route, session, and
child job. Status reads that scheduler and persists only allowlisted opaque
handles after the child finishes. Cancel delegates to the same scheduler.
Runtime expiry cancels the child and records `runtime_bound_exceeded`.

SQLite and Postgres persist the same private mapping. On daemon restart,
artifact jobs left running are swept to `interrupted` with
`daemon_restarted`. Replay returns that durable terminal truth and never
pretends the process-bound child resumed or silently dispatches replacement
work.

Tests inject the backend, clock, timer, artifact-job store, and mapping store.
Registry integration tests use deterministic Starlark Application handlers and
the real scheduler without invoking an LLM.
