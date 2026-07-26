# Feedback Host

`host.feedback` is the narrow application-facing bridge from reviewed feedback
to a governed daemon launcher. It does not collect feedback, run shell commands,
resolve repository paths, mutate source, or grant landing authority.

The existing application feedback adapter and runstatus feedback intake remain
the submission authority. This host exposes only:

```text
list_reviewed(scope, limit) -> reports, revision
dispatch(report_ref, dispatch_id, resume_mode, resume_workspace, retry_brief)
  -> job_id, canonical application receipts
```

## Trust boundaries

Session construction derives the authoritative scope from the loaded
application's `app.id`, `app.author`, and `app.version`. Callers cannot override
that identity. Both operations require an authenticated actor in the host
context. Owner identity is used to reject out-of-scope backend rows but is never
returned.

`list_reviewed` accepts a backend selector in `scope`; that selector cannot
widen the server-resolved application scope. The backend must return only
privacy-reviewed projections. The host validates every row, sorts by review time
descending and report reference ascending, and returns only reviewed content
plus privacy-safe source, revision, and receipt metadata. It asks the backend
for `limit + 1` rows and fails if more than `limit` match. It also fails if the
encoded response exceeds 256 KiB. Neither bound silently truncates data.

`dispatch_id` is the required idempotency key for external dispatch. A backend
must return the same durable job for repeated requests with that ID. The only
accepted resume modes are `fresh`, `reused-workspace`, and `resumed-session`.
The host bounds and transports `resume_workspace` and `retry_brief`; the backend
verifies those opaque references against `.capsules/workspaces` and `.artifacts`
under the daemon root, resolves symlinks, and rejects containment escapes before
launch. A successful dispatch returns only its opaque durable `job_id` and
finalized `application-receipt/v1` records. It never returns a story path,
repository path, resolved workspace path, raw feedback row, provider choice, or
target configuration.

## Daemon adopter

The builtin handler remains an unavailable sentinel in ordinary `kitsoki web`
and for applications without an explicit binding. `kitsoki daemon` constructs
the generic adopter from `.kitsoki.yaml` after story discovery:

```yaml
reviewed_feedback:
  review.application:
    target_application: implementation.application
    target_handler: implementation.application.feedback.apply
    target_action: implementation.application.feedback.apply.action
```

Each configured source and target application ID must resolve to exactly one
catalogue entry. The action must bind the exact exported target handler, and the
handler must require a session and declare a write or external effect. No
ledger, repository, story, provider, command, or path field exists in this
configuration.

The adopter reads the conventional reviewed ledger at
`.artifacts/feedback/feedback.jsonl`. It accepts only canonical
`kitsoki.feedback.report.v1` rows with `reviewed: true` whose top-level and
application attachment IDs both match the server-resolved source application.
It bounds ledger bytes, records, line size, response size, and row count;
deduplicates exact report references; fails on conflicting duplicates; sorts
deterministically; and scrubs home paths and recognized secrets before a report
can cross the host boundary.

Dispatch identity is durable in the daemon SQLite database under
`(source application, dispatch_id)`. Reusing an ID with different reviewed
content or resume references fails closed. The adapter creates or reuses one
deterministic artifact job and attached target session, then calls the exact
configured handler through the shared application service with actor
`kitsoki.feedback-daemon` and a server-derived idempotency key. Application
replay prevents a repeated handler side effect if a successful call is retried
before dispatch completion is persisted.

## Restart truth

Daemon startup marks pending feedback dispatches `interrupted` with reason
`daemon_restarted`. It may reattach the durable target session so its existing
trace remains inspectable, but it leaves the artifact job interrupted and does
not claim that process-bound work resumed. An explicit retry with the same
scope, dispatch ID, and request digest advances the attempt count, reuses the
same job/session and application idempotency key, and either obtains application
replay or executes the still-unrecorded call. Completed dispatches return their
stored canonical receipts without invoking the handler again.

The backend may create a job that performs governed review or implementation
work, but this host contract does not expose graph apply/authorize operations,
git merge/push operations, queue promotion, or any other source-landing
authority.
