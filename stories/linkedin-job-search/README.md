# Attended LinkedIn job search

This story makes exactly one LinkedIn **Jobs** search after an operator reviews
the keywords/location and explicitly chooses `confirm_search`. It records the
submitted parameters and final results URL in world state. It does not open job
details, apply, message, save, follow, paginate, or bulk-scrape.

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

The bridge must expose exactly these tool names under the `sassfully_browser`
MCP server namespace: `navigate`, `snapshot`, `fill`, `press`, and `confirm`.
The story agent receives no native filesystem, shell, web, or editor tools.

Set `SASSFULLY_LINKEDIN_PAIRING_CODE` in the environment that launches Kitsoki.
The `${...}` token is intentionally passed as MCP configuration interpolation;
the pairing code itself is never committed, rendered into the story, or put in
world state.

Validate the bridge tool listing in the client that runs Kitsoki before a live
session. Automated tests use the supplied flow stubs and never contact
LinkedIn.
