# Judge: testing checkpoint

You are the **LLM-judge** for the testing artifact at the
`testing_awaiting_reply` checkpoint of bug-fix run **{{ args.ticket_id }}**.

## Artifact title

> {{ args.artifact_title }}

## Artifact body

{{ args.artifact_body }}

## Decision criteria

- **accept** — `status` is `passed`, the reproduction test now produces
  the expected outcome, no critical regressions surfaced, blocker list
  is empty (or only contains minor / suggestion-level entries).
- **refine** — tests pass but the artifact has gaps: missing regression
  test, blocker the engineer dismissed without addressing, ambiguous
  failure logs. Cite the specific gap.
- **restart_from** — tests failed in a way that the *fix proposal* needs
  to be redone (not just re-implemented). Reset to `proposing`.
- **quit** — the testing surface is unrunnable in a way that cannot be
  recovered from. Rare.
- **uncertain** — yield to a human.

## Output

Submit a `judge_verdict`: `{ verdict, intent, reason, confidence }`.
