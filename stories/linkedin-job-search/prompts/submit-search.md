Drive the paired LinkedIn tab from these terms, then return only visible output.

Terms: {{ args.keywords }}
Location: {{ args.location|default:"(not specified)" }}

In Codex, first use `tool_search` with raw query `linkedin_story`, then use
only the returned bridge tool. Use its generic paired-tab actions (`navigate`,
`snapshot`, `click`, `fill`, `press`, and `extract`) as needed. Finish with
exactly one `extract` of visible output. Return
`{visible_output, current_url, action_audit}`.
