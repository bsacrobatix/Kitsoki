# Attended LinkedIn job search

This v0 story deterministically constructs
`https://www.linkedin.com/jobs/search-results/?keywords=<urlencoded>&geoId=103644278`
for a base US search, then waits for `confirm_search`. The only browser effects
are one Chrome-confirmed navigation, one Chrome-confirmed constrained
`select_remote` action, and one safe snapshot that verifies the Remote filter.
It does not fill fields, press controls, open job details, apply, message, save,
follow, paginate, or bulk-scrape.

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
`linkedin_story`. This v0 agent may use only `navigate`, `select_remote`, and
`snapshot`. The bridge must accept the exact base US route, require a visible
Chrome confirmation for each state-changing action, confine `select_remote` to
the Remote filter/option, and return remote-selection evidence for the snapshot.
The story agent receives no native filesystem, shell, web, or editor tools.

Set `SASSFULLY_LINKEDIN_PAIRING_CODE` in the environment that launches Kitsoki.
The `${...}` token is intentionally passed as MCP configuration interpolation;
the pairing code itself is never committed, rendered into the story, or put in
world state.

Validate the bridge tool listing in the client that runs Kitsoki before a live
session. Automated tests use the supplied flow stubs and never contact
LinkedIn.
