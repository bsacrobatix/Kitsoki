# Story: Dynamic Workflow Authoring Surfaces

**Status:** Draft v1. Nothing implemented yet.
**Kind:**   story
**Epic:**   [`dynamic-workflows.md`](dynamic-workflows.md)

## Why

The operator should not have to know whether the current entrypoint is MCP, CLI, TUI, or web. The same request, "make a workflow for this task and run it with gates," should produce the same draft package, validation result, and launch receipt. Studio MCP already exposes authoring and driving primitives, while `kitsoki web` hosts live sessions that the browser can observe and drive (`cmd/kitsoki/web.go:65`). The missing piece is one shared product lane that all surfaces call.

## What Changes

Add a dynamic workflow authoring lane with a shared backend function and thin surface adapters:

- **Studio MCP:** `workflow.create` or a `session.drive` route in dev-story that writes a draft package, validates it, and optionally launches it.
- **CLI:** `kitsoki workflow create "..."` and `kitsoki workflow run <draft-id>`.
- **TUI:** a dev-story room reached naturally from landing, with review and launch choices.
- **Web:** a runstatus action/modal that starts the same flow and navigates to the returned session URL.

The first version can live as a dev-story room backed by host calls, then expose the same host function through MCP/CLI.

## Impact

- **Story files:** `stories/dev-story/rooms/`, prompts, schemas, deterministic flows.
- **MCP/CLI/web/TUI:** thin launch adapters and status rendering.
- **Hosts:** draft generation host function, validator call, launch call.
- **Docs on ship:** `stories/dev-story/README.md`, `docs/architecture/mcp-studio.md`, `docs/web/README.md`.

## Story Shape

```text
landing
  -> dynamic_workflow_intake
  -> dynamic_workflow_draft_review
  -> dynamic_workflow_validation_failed | dynamic_workflow_ready
  -> dynamic_workflow_running
  -> dynamic_workflow_done
```

The room should be conversational at intake but explicit at gates:

- `revise` updates the generated draft prompt or selected files.
- `validate` runs deterministic validation again.
- `launch` opens a session only when validation passed.
- `export` is hidden until a run has a trace and validation evidence.

## Deterministic vs Interpretive

Interpretive:

- turning the operator's natural-language request into draft YAML;
- proposing gate names and validation commands;
- summarizing warnings for the operator.

Deterministic:

- writing the package;
- loading and validating the package;
- launching the session;
- rendering status and run URLs;
- exporting flow/cassette starters.

## MCP / CLI / Web Parity

All surfaces should return a common receipt:

```json
{
  "workflow_id": "dwf_...",
  "draft_dir": ".artifacts/dynamic-workflows/dwf_.../story",
  "validation": { "ok": true, "warnings": [] },
  "session": { "id": "...", "trace": "...", "url": "http://127.0.0.1:.../runs/..." },
  "next_actions": ["open", "revise", "export"]
}
```

If no web server is available, `url` is empty and the receipt includes the command needed to open the trace in runstatus.

## Tasks

```text
## 1. Shared Host/API
- [ ] 1.1 Add a dependency-injected create/validate/launch service.
- [ ] 1.2 Expose the service to Studio MCP without duplicating CLI code.
- [ ] 1.3 Add no-LLM unit tests around the service using fake generator output.

## 2. Dev-story Lane
- [ ] 2.1 Add rooms for intake, review, validate, launch, and done.
- [ ] 2.2 Add schemas/prompts for draft generation and warning summaries.
- [ ] 2.3 Add flow fixtures with mocked agent output and host handlers.

## 3. Surface Adapters
- [ ] 3.1 CLI `kitsoki workflow create/run/status` commands.
- [ ] 3.2 TUI command/menu path from dev-story landing.
- [ ] 3.3 Web action that calls the same backend and navigates to the run.
- [ ] 3.4 MCP tool or documented `session.drive` path.

## 4. Docs
- [ ] 4.1 Document the operator flow and no-LLM test posture.
- [ ] 4.2 Move shipped design to docs and trim/delete this proposal.
```

## Verification

- `story.validate` for dev-story after room additions.
- `story.test` or `go run ./cmd/kitsoki test flows stories/dev-story/app.yaml --flows <dynamic fixtures>`.
- MCP smoke using replay/flow cassettes only.
- Web Playwright test with `kitsoki web --flow`, no live LLM.

## Open Questions

1. Should MCP expose a first-class `workflow.create` tool or drive the dev-story room only? *Lean: first-class tool plus story room, sharing one service.*
2. Should CLI creation default to launch or stop at review? *Lean: stop at review unless `--run` is passed.*

## Non-goals

- Making generated workflows bypass operator review.
- Replacing dev-story's design pipeline.
- Adding a visual workflow editor in v1.
