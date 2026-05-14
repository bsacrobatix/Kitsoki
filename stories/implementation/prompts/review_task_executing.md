# Review the task — produce a task-summary artifact

You are reviewing the small task **{{ args.ticket_id }}** —
*{{ args.ticket_title }}* against `{{ args.workdir }}`.

The ticket body fetched from the tracker:

> {{ args.ticket_body }}

{% if args.refine_feedback %}**Refinement feedback from the previous attempt
(cycle {{ args.cycle }}):**

> {{ args.refine_feedback }}
{% endif %}

## Constraints

- `summary_title` is the one-line description of what the task is.
- `summary_markdown` is a structured rendering: scope, acceptance criteria,
  open questions. Keep it under 400 words; this room is the
  understanding pass, not the design pass.
- `acceptance_criteria` is a flat list (`["...", "..."]`) — each item is a
  testable assertion ("Endpoint X returns 200 on input Y").
- `scope` should call out what is **out** of scope (refactors, doc
  updates) when the ticket is silent. Implementation tickets are by
  contract small; if the body implies a multi-day feature, set
  `risk: high` and explain in `summary_markdown`.

## Output

Submit a `task_summary_artifact` (`schemas/task_summary_artifact.json`).
The `summary_markdown` is what the operator reads at the checkpoint —
write it for them: what to build, what done looks like, anything that
could derail.
