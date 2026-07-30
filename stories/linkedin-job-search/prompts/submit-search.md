Drive the paired LinkedIn Jobs tab from the exact supplied URL, then return visible job-result cards.

Terms: {{ args.keywords }}
Location: {{ args.location|default:"(not specified)" }}
Jobs URL: {{ args.search_url }}

In Codex, first use `tool_search` with raw query `linkedin_story`, then use
only the returned bridge tool. First call `navigate` exactly once with the Jobs
URL above and no other fields. Never use a `/search/results/people/` URL and do
not append USA or remote to the keyword query. Every snapshot must be exactly
`{"action":"snapshot"}`: do not include `captureEvidence` or any other field.
If Remote is not already selected, use a semantic `click` target for the visible
Remote button or option, then use that same bare snapshot to wait for the
filtered page. Finish with exactly one `extract` of visible job-result cards. Return
`{visible_output, current_url, action_audit}`.
