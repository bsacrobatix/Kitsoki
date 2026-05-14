# Closing the bug-fix run — produce the done artifact

You are wrapping up the run for **{{ args.ticket_id }}** —
*{{ args.ticket_title }}* after **{{ args.cycle }}** refinement cycle(s).

The validation produced:

> {{ args.validate_summary }}

## Constraints

- `lessons` must be drawn from this run's actual evidence — not generic
  best-practice. At minimum one lesson per non-trivial cycle.
- Each lesson cites a category (e.g. `api-patterns`, `failure-patterns`,
  `judge-misclassification`) and a severity.
- `summary_markdown` is the postmortem: what the bug was, what the fix
  did, what cost the cycles (if any), and what changed about how we'd
  approach a similar bug next time.

## Output

Submit a `done_artifact` (see `schemas/done_artifact.json`).
