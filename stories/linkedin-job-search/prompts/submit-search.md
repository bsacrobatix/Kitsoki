Perform the one confirmed LinkedIn Jobs direct URL navigation and Remote filter
selection described below.

Keywords: {{ args.keywords }}
Location: {{ args.location|default:"(any location)" }}
Confirmation: {{ args.confirmation }}
Direct URL: {{ args.search_url }}

The confirmation must be true. In Codex, first use `tool_search` with raw query
`linkedin_story`, then use only the returned Sassfully bridge tool. Call
`navigate` exactly once with the supplied Direct URL and await Chrome Allow.
Then call `select_remote` exactly once and await its Chrome Allow confirmation;
this action is the bridge's constrained Remote filter control, not a generic
click. Then call `snapshot` exactly once and verify it reports a safe LinkedIn
Jobs URL with the Remote filter active. Submit only that verified URL and the
exact parameters used. Never call fill, press, click, submit, open any job,
inspect result cards, apply, message, save, follow, paginate, export, scrape,
or make any other LinkedIn change.
