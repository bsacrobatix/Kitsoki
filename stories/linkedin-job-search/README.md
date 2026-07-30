# Attended LinkedIn job search

This v0 story deterministically constructs
`https://www.linkedin.com/jobs/search-results/?keywords=<urlencoded>&geoId=103644278&f_WT=2`
for a remote-only search, then waits for `confirm_search`. The only browser
effect is one Chrome-confirmed navigation to that URL followed by one safe
snapshot of the current Jobs URL. It does not fill fields, press controls,
open job details, apply, message, save, follow, paginate, or bulk-scrape.

## Sassfully bridge binding

The story starts the local bridge with this stdio MCP configuration:

```yaml
agents.linkedin_searcher.mcp.servers.sassfully_browser:
  command: node
  args:
    - /Users/brad/code/studio-sassfully/.capsules/workspaces/extension-local-test-20260730/packages/feedback-extension/story-bridge/stdio-server.mjs
    - --pairing-code
    - ${SASSFULLY_LINKEDIN_PAIRING_CODE}
```

The bridge exposes one tool under the `sassfully_browser` MCP server namespace:
`linkedin_story`. It may advertise `navigate`, `snapshot`, `fill`, and `press`,
but this v0 agent may use only `navigate` and `snapshot`. The bridge must accept
the exact direct-URL route above and require one visible Chrome confirmation for
its `navigate` action.
The story agent receives no native filesystem, shell, web, or editor tools.

Set `SASSFULLY_LINKEDIN_PAIRING_CODE` in the environment that launches Kitsoki.
The `${...}` token is intentionally passed as MCP configuration interpolation;
the pairing code itself is never committed, rendered into the story, or put in
world state.

Validate the bridge tool listing in the client that runs Kitsoki before a live
session. Automated tests use the supplied flow stubs and never contact
LinkedIn.
