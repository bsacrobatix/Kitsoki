# Runstatus snapshot host

`host.runstatus.snapshot` is the read-only story-application projection of the
daemon's durable artifact-job registry. It is intentionally smaller than the
runstatus web RPC surface: a story receives stable lifecycle facts, not trace
content, filesystem locations, URLs, operator identities, or process state.

The runtime injects the daemon's artifact-job repository and fixes the
application ID when it constructs the handler. The application cannot widen
that scope through host arguments. Outside daemon mode the registered builtin
returns a typed unavailable result.

## Contract

The operation requires two explicit budgets:

- `max_jobs`: maximum matching non-archived rows, from 1 through 200.
- `max_bytes`: maximum encoded snapshot size, from 1 through 262144 bytes.

The provider queries one extra row to detect a row overflow. It returns an error
when either budget is exceeded and never returns a partial projection.

The `kitsoki/runstatus-snapshot/v1` object contains:

- `jobs`: stable job and session references, lifecycle status, update time, and
  whether a workspace is attached.
- `sessions`: durable session summaries derived from those jobs, including the
  latest job reference and attention/workspace counts.
- `workspaces`: opaque workspace references linked to their job and session,
  carrying only the durable job lifecycle status.
- `attention`: finite refs for `awaiting_input`, `interrupted`, and `failed`
  durable jobs. The kinds are `operator_input_required`, `interrupted`, and
  `failed`.
- `counts`: total rows and counts by the closed artifact-job status vocabulary.

Rows and references are sorted independently of repository iteration order.
No wall clock is sampled. Repeating the operation against the same captured
store state therefore produces the same JSON bytes.

## Privacy boundary

The projection omits story paths, origin details, run URLs, trace paths,
summaries, free-form phases, owners, interruption text, terminal artifact
handles, and artifact contents. It rejects unknown job status values rather
than reflecting an untyped string into an application.

Workspace IDs, session IDs, and job IDs are opaque local references. They are
only returned for the application ID bound to the handler. This provider does
not probe processes, inspect workspaces, execute commands, or contact remote
workers.

## Deliberate gaps

Artifact jobs contain workspace attachment identity but not a canonical
workspace-health or review-decision record. This provider does not infer either
from the free-form `phase` or `summary` fields.

Three separate typed repositories are needed before the remaining facts can
join this snapshot:

1. A workspace snapshot provider keyed by workspace instance ID with a closed
   lifecycle vocabulary and an explicit freshness/version field.
2. A review snapshot provider keyed by subject job/workspace reference with a
   closed state such as `pending`, `approved`, `changes_requested`, or
   `dismissed`, plus an operator-attention flag.
3. An operator-inbox snapshot provider keyed by app and durable session
   reference. It must expose only notification IDs, severity, read/action
   counts, and subject refs; notification titles, bodies, teleport slots, and
   other free-form payloads stay outside the application projection. The
   current `attention` section covers artifact-job lifecycle attention, not
   every live session notification.

Both providers must support app scoping and limit-plus-one reads so composition
preserves the same fail-not-truncate and privacy contracts. Cross-worker daemon
federation is also outside this local-store slice; it needs the same typed
bounded seam rather than the frontend summary projection, which contains URLs
and free-form text.
