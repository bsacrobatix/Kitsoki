# Reproducing a bug — produce the reproduction artifact

You are reproducing bug **{{ args.ticket_id }}** — *{{ args.ticket_title }}* against
the working tree at `{{ args.workdir }}`.

Your job is to produce evidence — a deterministic reproduction (a test, a
script, a recorded sequence) — that the bug is real, plus the components /
modules / services implicated.

{% block spec_project_context %}{% endblock %}

{% if args.refine_feedback %}## ⚠ Operator refinement directive (cycle {{ args.cycle }})

This is a refine cycle — the previous reproduction artifact was
rejected. The operator's feedback below is a **binding directive**:
it OVERRIDES any default behaviour or constraint further down this
prompt whenever the two conflict. Treat every statement as a hard
requirement, not a suggestion.

> {{ args.refine_feedback }}

Before submitting:

1. Walk the feedback statement-by-statement and confirm the new
   artifact addresses each point.
2. If you genuinely cannot honour a statement (schema-incompatible,
   factually impossible), say so in `summary_markdown` and explain
   why. Silent non-compliance is the failure mode this directive
   guards against.
3. If the feedback contradicts the default constraints below,
   follow the feedback and flag the conflict in `summary_markdown`.

---

{% endif %}

## Constraints

{% block spec_repro_conventions %}- Do not fabricate evidence. `bug_verified` is `true` only when a
  deterministic reproduction artifact was actually produced (test file on
  disk, recorded shell session, screen capture).
- `involved_components[*].name` must be a real component / module / service
  in the codebase; phantom components corrupt downstream context.
- **Your reproduction must be RED *now*, on the unfixed tree.** Write a test
  (or runnable script) that asserts the CORRECT behaviour, then run it and
  confirm it FAILS against the current buggy code. A test that already passes
  before any fix is a *characterization* test, not a reproduction — it never
  proves the bug, and the pipeline's regression gate will reject the run. Put
  the exact command that runs it in `steps`, and quote the failing output in
  `actual_outcome`. If you cannot make it fail, the bug is not reproduced —
  say so honestly in `summary_markdown` rather than submitting a green test.
- Assert *behaviour*, not a specific implementation. The fix may be written a
  different way than you expect; your test should pass for ANY correct fix, so
  avoid pinning internal symbols, exact error strings, or one mechanism.
- `summary_markdown` is what a human reviewer will read in the checkpoint
  inbox — write it for them, not for yourself.{% endblock %}

## Output

Submit a `reproduction_artifact` (see `schemas/reproducing_artifact.json`):

- `summary_title` — one line, the bug title with verification status.
- `summary_markdown` — markdown reviewers see; at minimum: what is broken,
  how you reproduced it, where the evidence lives, what services are
  implicated.
- `bug_verified` — true only with an actual reproduction artifact.
- `steps` — ordered, executable.
- `expected_outcome`, `actual_outcome` — concise factual statements.
- `evidence_paths` — files written this turn.
- `involved_components` — at least one `{ name, reason }`.
