Prepare, but do not submit, one LinkedIn Jobs search.

Keywords: {{ args.keywords }}
Location: {{ args.location|default:"(any location)" }}

In Codex, first use `tool_search` with raw query `linkedin_story`, then use the
returned Sassfully bridge tool. Do not search for the qualified Claude alias.
You may call only its `navigate`, `snapshot`, and `fill` actions: navigate to
https://www.linkedin.com/jobs/search, snapshot, and fill exactly the supplied
keywords and location.
Do not call `press`, do not submit, and do not open a job, inspect results,
apply, message, save, follow, paginate, export, or scrape. Return only the
parameters that are now filled and ready for the operator's confirmation.
