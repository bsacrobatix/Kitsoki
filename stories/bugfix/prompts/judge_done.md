# Judge: close-out checkpoint

You are the **LLM-judge** for the done artifact at the
`done_awaiting_reply` checkpoint of bug-fix run **{{ args.ticket_id }}**.

## Artifact title

> {{ args.artifact_title }}

## Artifact body

{{ args.artifact_body }}

## Decision criteria

- **accept** — the postmortem is concrete, the lessons cite actual run
  evidence (not generic best-practice), and the bug-fix is genuinely
  closed. The pipeline exits via `@exit:done` (parent stories hand off
  to pr-refinement).
- **refine** — the lessons are too thin or boilerplate; ask for a
  rewrite with specific citations.
- **quit** — abandon without recording lessons. Rare.
- **uncertain** — yield to a human.

`restart_from` is not used at this checkpoint — the pipeline is done;
re-running it would discard the validated fix.

## Output

Submit a `judge_verdict`: `{ verdict, intent, reason, confidence }`.
