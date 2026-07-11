# Reviewing tests & implementation — produce the testing artifact

You are reviewing the implementation of the fix for **{{ args.ticket_id }}** —
*{{ args.ticket_title }}*.

The proposed fix was:

> {{ args.fix_summary }}

The CI / test run produced:

```
{{ args.ci_log }}
```

{% if args.refine_feedback %}## ⚠ Operator refinement directive (cycle {{ args.cycle }})

This is a refine cycle — the previous test review was rejected. The
operator's feedback below is a **binding directive**: it OVERRIDES
any default behaviour or constraint further down this prompt whenever
the two conflict. Treat every statement as a hard requirement, not a
suggestion.

> {{ args.refine_feedback }}

Before submitting:

1. Walk the feedback statement-by-statement and confirm the new
   review addresses each point. If the feedback says "do not X",
   the review must NOT do X — including in `summary_markdown`,
   `status`, `tests_added`, and `blockers`.
2. If you genuinely cannot honour a statement (schema-incompatible,
   factually impossible), say so in `summary_markdown` and explain
   why. Silent non-compliance is the failure mode this directive
   guards against.
3. If the feedback contradicts the default constraints below,
   follow the feedback and flag the conflict in `summary_markdown`.

---

{% endif %}

## Constraints

- **Read the CI result and log honestly.** `status` is `passed` only when the
  runner returned success, the bug reproduction now passes, and no *unexpected*
  regression occurred. A framework may report an explicitly expected / known
  failure while still returning success (for example AVA's `[expected fail]` or
  `N known failure`); that is a documented baseline caveat, not a new failure.
  Record it in `summary_markdown`, but do not turn it into a blocker or fail a
  correct fix. A non-zero result, panic, build failure, unexpected test failure,
  `UNIQUE constraint`/runtime error, or an unlabelled `N tests failed` is
  `failed` even if the bug's own test passes. Use `blocked` only for an
  unrunnable suite (compile error, missing dependency). When `failed`/`blocked`,
  list the specific failing tests + root cause in `blockers` and
  `summary_markdown` so the implementer can repair them on the next cycle.
- `tests_added` must list new / modified test files. Reuse existing tests
  where possible; only add fresh ones if no existing test covers the bug.
- **Check the test asserts the ticket's end-to-end OUTCOME, not a near-side
  signal.** A green log is necessary but not sufficient: confirm the reproduction
  actually asserts the observable deliverable the ticket promises (what the
  caller / downstream / far side of the boundary receives), not merely that a
  mechanism engaged (a header set, a code path hit, a wire format chosen). If the
  test only checks the near-side signal while the ticket's real outcome could
  still be broken, that is a `blocker` — the fix may be incomplete even though the
  test is green. Name the missing far-side assertion in `blockers`.
- `blockers` are review-grade objections that must be fixed before the
  PR can advance.

## Output

Submit an `implement_review_artifact` (see `schemas/testing_artifact.json`).
The `summary_markdown` should walk the reviewer through tests-added,
tests-run results, and any blockers in plain prose.
