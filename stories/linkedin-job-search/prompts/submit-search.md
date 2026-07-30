Perform the one confirmed LinkedIn Jobs direct URL navigation described below.

Keywords: {{ args.keywords }}
Location: {{ args.location|default:"(any location)" }}
Confirmation: {{ args.confirmation }}
Direct URL: {{ args.search_url }}

The confirmation must be true. In Codex, first use `tool_search` with raw query
`linkedin_story`, then use only the returned Sassfully bridge tool. Call
`navigate` exactly once with the supplied Direct URL and await the single Chrome
Allow confirmation for that navigation. Then call `snapshot` exactly once and
verify it returns a LinkedIn Jobs URL. Submit only that verified URL and the
exact parameters used. Never call fill, press, click, submit, open any job,
inspect result cards, apply, message, save, follow, paginate, export, scrape,
or make any other LinkedIn change.
