# Feedback Host

`host.feedback` is the narrow application-facing bridge from reviewed feedback
to a governed daemon launcher. It does not collect feedback, run shell commands,
resolve repository paths, mutate source, or grant landing authority.

The existing application feedback adapter and runstatus feedback intake remain
the submission authority. This host exposes only:

```text
list_reviewed(scope, limit) -> reports, revision
dispatch(report_ref, dispatch_id, resume_mode, resume_workspace, retry_brief) -> job_id
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
must verify those references against its server-owned managed-workspace and
artifact roots before launch. A successful dispatch returns only its opaque
durable `job_id`.

## Backend registration

The builtin handler is an unavailable sentinel. A daemon adopter explicitly
binds an application ID during construction:

```go
err := registry.RegisterFeedbackBackend(applicationID, governedBackend)
```

Session creation replaces the sentinel only for the matching application ID.
There is intentionally no default repository, story, workspace, or launcher
binding in Kitsoki. The remaining adopter step for POG is to register its
governed launcher backend at daemon construction, including its own report
resolver, managed-root validation, retry-state validation, and durable
dispatch-ID deduplication.

The backend may create a job that performs governed review or implementation
work, but this host contract does not expose graph apply/authorize operations,
git merge/push operations, queue promotion, or any other source-landing
authority.
