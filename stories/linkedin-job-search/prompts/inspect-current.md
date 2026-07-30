Read the already-open paired LinkedIn Jobs results page without changing it.

In Codex, first use `tool_search` with raw query `linkedin_story`. Then call the
returned bridge tool exactly twice: `{"action":"snapshot"}`, followed by
`{"action":"extract"}`. Do not call navigate, click, fill, press, or any other
action. Return `{current_url, remote_visible, cards, action_audit}`. Refuse a
non-Jobs URL or page without a visible Remote indicator.
