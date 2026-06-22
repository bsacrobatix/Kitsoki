# Recipe: smoke-test async work through studio MCP

Use `kitsoki mcp-test` when you need to verify the studio MCP server without
reloading an LLM client. This smoke runs a real MCP client over stdio, keeps one
server-side session handle alive across calls, waits for a background job to
finish, captures an inbox notification id, teleports back to the task, and
re-renders the current TUI frame.

The smoke uses only replay/direct session driving and `host.run`; it does not
call a real LLM.

```sh
GOCACHE="$PWD/.cache/go-build" \
go run ./cmd/kitsoki mcp-test \
  --timeout 20s \
  --server-arg mcp \
  --server-arg --stories-dir --server-arg ./stories \
  --server-arg --db --server-arg .artifacts/mcp-test/async-teleport.db \
  --calls '[
    {
      "tool": "session.new",
      "args": {
        "story_path": "testdata/apps/background_jobs/app.yaml",
        "key": "async-teleport"
      }
    },
    {
      "tool": "session.submit",
      "args": {
        "handle": "async-teleport",
        "intent": "enter"
      },
      "expect": {
        "structuredContent.outcome.state": "running"
      }
    },
    {
      "tool": "session.inspect",
      "args": {
        "handle": "async-teleport"
      },
      "expect": {
        "structuredContent.async.jobs_terminal": 1,
        "structuredContent.async.notifications_unread": 2
      },
      "save": {
        "notification_id": "structuredContent.notifications.0.id"
      },
      "retries": 30,
      "interval_ms": 100
    },
    {
      "tool": "session.teleport",
      "args": {
        "handle": "async-teleport",
        "notification_id": "${notification_id}"
      },
      "expect": {
        "structuredContent.outcome.state": "running"
      }
    },
    {
      "tool": "session.inspect",
      "args": {
        "handle": "async-teleport"
      },
      "expect": {
        "structuredContent.async.notifications_unread": 1
      }
    },
    {
      "tool": "render.tui",
      "args": {
        "handle": "async-teleport"
      },
      "expect": {
        "structuredContent.frame.metadata.state": "running"
      }
    }
  ]'
```

Why the explicit `--db` matters: studio MCP opens the chat/job schema on start.
In sandboxes or read-only developer environments, the default shared
`sessions.db` may not be writable. Pointing at `.artifacts/mcp-test/*.db` keeps
the smoke self-contained and disposable.

Useful `mcp-test --calls` fields:

| Field | Purpose |
|---|---|
| `tool` | MCP tool name to call. |
| `args` | JSON object passed as tool arguments. |
| `expect` | Dot-path assertions against the MCP `CallToolResult` JSON. Array indexes are supported, for example `structuredContent.notifications.0.id`. |
| `save` | Captures dot-path values into `${name}` variables for later calls. |
| `retries` / `interval_ms` | Repeats the tool call until expectations pass, useful for async `session.inspect` polling. |

The expected proof at the end is:

- `session.inspect.async.jobs_terminal == 1`
- `session.inspect.async.notifications_unread == 2`
- `session.teleport` succeeds using the captured notification id
- a final `session.inspect` reports `notifications_unread == 1`
- `render.tui` reports the reacquired frame's state as `running`

`render.web` is a separate browser-capable seam. In the plain `kitsoki mcp`
stdio server it currently returns a text fallback unless a host injects the
webshot provider; the studio package still has no-LLM stub coverage for the
`render.web` MCP tool, but this stdio smoke intentionally gates only the
currently-wired CLI surface.
