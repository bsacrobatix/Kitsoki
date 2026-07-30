Prepare, but do not submit, one LinkedIn Jobs search.

Keywords: {{ args.keywords }}
Location: {{ args.location|default:"(any location)" }}

Use only `sassfully_browser.linkedin_story` with its documented `action`
arguments. You may call only `navigate`, `snapshot`, and `fill`: navigate to
LinkedIn Jobs, snapshot, and fill exactly the supplied keywords and location.
Do not call `press`, do not submit, and do not open a job, inspect results,
apply, message, save, follow, paginate, export, or scrape. Return only the
parameters that are now filled and ready for the operator's confirmation.
