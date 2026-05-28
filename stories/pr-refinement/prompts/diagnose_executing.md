# Diagnose CI failure for PR {{ args.pr_id }}

PR title: **{{ args.pr_title }}**

You are diagnosing the CI failure on PR {{ args.pr_id }}. CI just
reported the following failing checks:

> {{ args.failed_checks }}

The last build log was:

```
{{ args.ci_log }}
```

{% if args.refine_feedback %}
A previous diagnose attempt got the following feedback (cycle
{{ args.cycle }}):

> {{ args.refine_feedback }}

Address that feedback in your next attempt.
{% endif %}

## Decide

Produce a `diagnose_artifact` with:

- `summary_title` — one-line headline (e.g. "Unit test TestFoo flakes
  on macOS runner").
- `summary_markdown` — a 1–3 paragraph diagnosis suitable for posting
  as a PR comment. Be concrete; cite the failing check names and any
  log line that points at the cause.
- `root_cause` — one sentence on what is actually broken.
- `fix_description` — one sentence on what you will change to fix it.
- `affected_files` — paths that the fix will touch.
- `failing_checks` — copy the failed_checks list back out.
- `confidence` — 0.0–1.0, your subjective certainty.
- `reasoning` — why you believe `root_cause`.

Submit through the `submit` tool. The schema is
`schemas/diagnose_artifact.json`.
