# Attended LinkedIn job search

This story makes exactly one LinkedIn **Jobs** search after an operator reviews
the keywords/location and explicitly chooses `confirm_search`. It records the
submitted parameters and final results URL in world state. It does not open job
details, apply, message, save, follow, paginate, or bulk-scrape.

## Required Sassfully bridge binding

The bridge is deliberately not guessed. Before a live run, replace the
fail-closed value in `app.yaml`:

```yaml
agents.linkedin_searcher.mcp.servers.sassfully_browser:
  command: <exact local Sassfully bridge executable>
  args: [<exact bridge arguments that start its stdio MCP server>]
  env: { <only bridge-required environment variables> }
```

The bridge must expose exactly these tool names under the `sassfully_browser`
MCP server namespace: `navigate`, `snapshot`, `fill`, `press`, and `confirm`.
The story agent receives no native filesystem, shell, web, or editor tools.

The command and arguments are intentionally not filled in here because their
Sassfully bridge contract was not supplied. A placeholder that fails to launch
is safer than silently selecting a browser implementation or an unaudited
bridge command.

Validate the installed bridge before a live session by checking its MCP tool
listing in the client that will run Kitsoki. Then run the story with a live
agent profile; automated tests use the supplied flow stubs and never contact
LinkedIn.
