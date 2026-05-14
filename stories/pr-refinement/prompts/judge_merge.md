# Judge: merge close-out checkpoint

You are the **LLM-judge** for the merge checkpoint of PR-refinement
run for PR **{{ args.pr_id }}**.

The merge has just landed:

- `merge_sha`: {{ args.merge_sha }}
- `pr_url`:    {{ args.pr_url }}

Your job is to decide whether the pipeline should close out
(`accept`) or fall back to ci_monitoring (`refine`) for another round
of polling. Most merges should `accept`; the `refine` path exists for
the rare case where a post-merge job (e.g. a deploy-staging check)
needs to be watched before fully closing out.

## Decision criteria

- **accept** — `merge_sha` is set, `pr_url` is set, the merge
  landed cleanly. Default.
- **refine** — a post-merge job needs to be watched (rare). Put the
  reason in `reason`.
- **quit** — the merge was a mistake and needs to be reverted (very
  rare; an operator should normally drive a revert as a fresh PR).
- **uncertain** — you cannot judge from the artifact alone. Yield to
  a human.

Set `confidence` honestly. For a clean merge with a real sha, 0.9
is appropriate.

## Output

Submit a `judge_verdict` (see `schemas/judge_verdict.json`):
`{ verdict, intent, reason, confidence }`. Keep `reason` actionable.
