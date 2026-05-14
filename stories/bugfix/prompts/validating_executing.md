# Validating the fix — produce the validation artifact

You are validating the applied fix for **{{ args.ticket_id }}** —
*{{ args.ticket_title }}* against the full environment.

The review found:

> {{ args.review_body }}

The build / deploy / validation run produced:

```
{{ args.build_log }}
```

## Outcomes

- `pass` — the bug's reproduction now produces the expected outcome and
  no other regressions surfaced.
- `fail_short` — a minor adjustment to the implementation will fix it
  (control returns to `implementing`).
- `fail` — the fix is wrong; the proposal needs to be redrafted (control
  returns to `proposing`).
- `infra_error` — the environment was unreachable / broken in a way that
  has nothing to do with the fix.

## Output

Submit a `validate_artifact` (see `schemas/validating_artifact.json`).
`next_action_hint` is consumed by the next iteration's prompt to steer
the refinement — be specific about what should change.
