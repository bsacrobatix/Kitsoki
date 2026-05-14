# Proposing a fix — produce the fix-proposal artifact

You are proposing a fix for **{{ args.ticket_id }}** — *{{ args.ticket_title }}*
against `{{ args.workdir }}`.

You have a reproduction artifact from the previous room:

> {{ args.reproduction_summary }}

{% if args.refine_feedback %}**Refinement feedback from the previous attempt
(cycle {{ args.cycle }}):**

> {{ args.refine_feedback }}
{% endif %}

## Constraints

- `fix_description` must be concrete — not "fix the bug". Describe the
  edit in enough detail that another engineer could implement it without
  rereading the ticket.
- `root_cause` must explain *why* the bug happens, not *what* the bug is.
  Cite the offending file / line where possible.
- `affected_files` must be real, relative paths in the repo (no leading
  slash; must have an extension). At least one.
- `confidence` is your own estimate in [0.0, 1.0]; under 0.3 is rejected
  downstream.
- `reasoning` is the chain from evidence → cause → fix.

## Output

Submit a `propose_fix_artifact` (see `schemas/proposing_artifact.json`).
The `summary_markdown` field is what a human reviewer reads at the
checkpoint — write it for them: bug, cause, fix, files, confidence.
