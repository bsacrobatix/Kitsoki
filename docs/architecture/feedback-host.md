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

## Command-free reconciliation

Daemon applications may also receive three exact, zero-argument operations:

```text
host.reviewed_feedback_campaign.reconcile
host.feedback_intake.reconcile
host.feedback_federation.reconcile
```

They reject every caller argument. In particular, a story cannot select a
source or target application, handler, action, path, URL, command, provider,
credential, repository, actor, session, or transport. Application scope comes
from the loaded application and all remaining authority comes from server
configuration and injected services.

`reviewed_feedback_campaign.reconcile` is enabled by the existing
`reviewed_feedback.<application>` adopter binding. It lists a bounded set of
eligible reviewed records, transactionally claims at most one, and invokes the
configured adopter with a stable identity derived from operation, source
application, and report reference.

`feedback_federation.reconcile` has an independent exact target binding:

```yaml
feedback_federation:
  review.application:
    target_application: portfolio.application
    target_handler: portfolio.application.feedback.apply
    target_action: portfolio.application.feedback.apply.action
    max_records: 50
```

Federation uses the same `application.Service` path and canonical receipt
validation as the adopter. It never edits a sibling filesystem or calls raw
MCP or HTTP. The three target values are opaque semantic identifiers; no
executable, path, endpoint, or secret-bearing fields exist.

`feedback_intake.reconcile` binds an application to a typed capture source:

```yaml
feedback_intake:
  review.application:
    source: application-feedback
    max_records: 50
```

The fixed `application-feedback` source is the existing platform-owned
canonical ledger written by `/api/feedback/local`; the daemon injects its
already-resolved `reviewedfeedback.JSONLLedger`. This adds no file format,
directory, or ingress route. Other sources must be injected through
`RegisterFeedbackCaptureSource` under an opaque ID. An unregistered configured
source reports `configured source is unavailable`; a registered source with no
eligible records returns `status: empty`.

Intake accepts only typed `applicationfeedback.Report` values. It verifies the
canonical report and attachment schemas, application identity, frame revision,
opaque classifications and references, and matching semantic anchors. It
deterministically scrubs reviewed user text and rejects anchors whose encoded
form contains home paths or recognized secrets. It persists through the
existing reviewed ledger. When the source is that same ledger, an exact record
is acknowledged without appending a duplicate line; a conflicting duplicate
fails closed.

## Reconciliation state

The daemon database owns restart and concurrency truth for all three
operations. SQLite remains the default; PostgreSQL uses the same contract in a
dedicated `reviewedfeedback` schema. Durable identity is
`(operation, application, item)` plus a normalized content digest.
A dispatch digest includes the exact target binding; an intake digest includes
the configured source ID and opaque source reference. Configuration drift
therefore fails closed instead of replaying a receipt from a different target
or source.
A partial unique index permits only one pending item for each operation and
application. Reusing an item identity with different content fails, completed
results replay without calling the source or target again, and an external
failure leaves an interrupted claim with only a bounded privacy-safe reason.

Daemon startup marks pending claims `interrupted` with
`daemon_restarted`. Only an explicit later reconcile may claim the item again;
the attempt count advances and the stable dispatch/application idempotency
identity is reused. Campaign and federation successes store the target's
finalized `application-receipt/v1`. Intake stores a finalized
`kitsoki/feedback-intake-receipt/v1` and returns it to the typed source as its
acknowledgement. Results are bounded by the configured record limit, one
selected item, one application receipt, and fixed-size receipt metadata.
