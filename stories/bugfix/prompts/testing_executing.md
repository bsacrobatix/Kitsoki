# Reviewing tests & implementation — produce the testing artifact

You are reviewing the implementation of the fix for **{{ args.ticket_id }}** —
*{{ args.ticket_title }}*.

The proposed fix was:

> {{ args.fix_summary }}

The CI / test run produced:

```
{{ args.ci_log }}
```

## Constraints

- `status` must be `passed` only if the tests actually ran and the bug
  reproduction now produces the expected outcome. `blocked` is for
  unrunnable suites (compile error, missing dependency); `failed` is for
  ran-but-broken.
- `tests_added` must list new / modified test files. Reuse existing tests
  where possible; only add fresh ones if no existing test covers the bug.
- `blockers` are review-grade objections that must be fixed before the
  PR can advance.

## Output

Submit an `implement_review_artifact` (see `schemas/testing_artifact.json`).
The `summary_markdown` should walk the reviewer through tests-added,
tests-run results, and any blockers in plain prose.
