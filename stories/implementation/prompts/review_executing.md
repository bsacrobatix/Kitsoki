# Code review — produce a review-artifact

You are reviewing the diff for task **{{ args.ticket_id }}** —
*{{ args.ticket_title }}*.

Change summary:

> {{ args.code_summary }}

Diff:

```
{{ args.diff }}
```

## Constraints

- `summary_title` is a one-line review verdict ("Approved; one nit on
  error handling.").
- `summary_markdown` is the structured rendering: blockers first, then
  nits, then approve/changes recommendation. Aim for 100–400 words.
- `status` is one of `approved | changes_requested | blocked`.
  Implementation tickets are small — prefer `approved` when the change
  is concrete and the tests cover the acceptance criteria.
- `nits` and `blockers` are flat lists; only populate them when there
  is real signal.

## Output

Submit a `review_artifact` (`schemas/review_artifact.json`).
