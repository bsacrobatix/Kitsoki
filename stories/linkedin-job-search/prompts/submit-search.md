Perform the one confirmed atomic LinkedIn Jobs readiness search described below.

Keywords: {{ args.keywords }}
Location: {{ args.location|default:"(any location)" }}
Confirmation: {{ args.confirmation }}

The confirmation must be true. In Codex, first use `tool_search` with raw query
`linkedin_story`, then use only the returned Sassfully bridge tool. Call
`search_jobs` exactly once with `{keywords: args.keywords, geoId: "103644278",
remote: true}` and await its one Chrome Allow confirmation. Submit only its
returned `{finalUrl, remoteSelected, alreadySelected}` with the exact parameters
used, and only when `remoteSelected` is true. If the action fails or returns
`remoteSelected: false`, stop without retry; return no success result. Never call fill, press, click, submit, open any job,
inspect result cards, apply, message, save, follow, paginate, export, scrape,
or make any other LinkedIn change.
