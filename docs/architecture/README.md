# Architecture

How Kitsoki works: the deterministic engine, the LLM boundary, and the
surfaces where the runtime reaches the outside world.

If you are writing a story, start with [`../stories/`](../stories/README.md).
If you are testing or debugging one, use [`../tracing/`](../tracing/README.md).

## Reading paths

| Goal | Read |
|---|---|
| Understand the thesis | [`concept.md`](concept.md) -> [`overview.md`](overview.md) |
| Evaluate the design against other systems | [`concept.md`](concept.md) -> [`prior-art.md`](prior-art.md) |
| Change runtime behavior | [`overview.md`](overview.md) -> [`hosts.md`](hosts.md) -> [`semantic-routing.md`](semantic-routing.md) |
| Work on external agents | [`mcp-studio.md`](mcp-studio.md) -> [`agent-plugin.md`](agent-plugin.md) -> [`agent-launch-policy.md`](agent-launch-policy.md) |
| Contribute safely | [`developer-guide.md`](developer-guide.md) -> [`../tracing/testing.md`](../tracing/testing.md) |

## Start here

- [`concept.md`](concept.md) — control inversion, progressive determinism, and
  why the LLM returns bounded results instead of driving the workflow.
- [`overview.md`](overview.md) — system layers, turn loop, LLM boundary,
  multi-surface sessions, persistence, replay, and trust model.
- [`prior-art.md`](prior-art.md) — what Kitsoki borrows from and rejects from
  interactive fiction, statecharts, workflow engines, and dialogue managers.

## Runtime core

- [`hosts.md`](hosts.md) — authoritative host-effect contracts.
  [`hosts/`](hosts/README.md) is the shorter family index.
- [`starlark.md`](starlark.md) — deterministic glue scripts, CodeAct snippets,
  cassettes, validation, and the narrow `ctx` capability surface.
- [`semantic-routing.md`](semantic-routing.md) — deterministic routing,
  synonyms, templates, typed slots, the turn cache, and replay tooling.
- [`system-prompt.md`](system-prompt.md) — layered, cache-friendly prompts for
  routing and `host.agent.*` calls.
- [`room-workbench.md`](room-workbench.md) — the `workbench:` primitive and its
  deterministic-seam rule.
- [`prompt-intercept.md`](prompt-intercept.md) — pre-LLM prompt classification,
  conservative gates, and decision recording.
- [`ambient-mining.md`](ambient-mining.md) — propose/apply mining loop for
  staged story improvements.

## Agents and studio

- [`mcp-studio.md`](mcp-studio.md) — the MCP facade for authoring, driving,
  testing, inspecting, and exporting Kitsoki sessions.
- [`agent-plugin.md`](agent-plugin.md) — external agent declarations and the
  `invoke: host.agent.<verb>` contract.
- [`agent-providers.md`](agent-providers.md) — per-call Anthropic-compatible
  backend selection.
- [`agent-backends.md`](agent-backends.md) — `--agent claude|copilot|codex`
  backend behavior and parity expectations.
- [`agent-cli.md`](agent-cli.md) — standalone JSON/CLI access to
  `host.agent.*` verbs.
- [`agent-launch.md`](agent-launch.md) and
  [`agent-launch-policy.md`](agent-launch-policy.md) — launch-plan resolution
  and protected-checkout guardrails.
- [`operator-ask.md`](operator-ask.md) — forwarding agent questions to a live
  operator instead of using headless-broken `AskUserQuestion`.
- [`harness-profiles.md`](harness-profiles.md) — repeatable profile selection
  for synthetic, replayed, and live harnesses.

## Integrations and surfaces

- [`transports.md`](transports.md) — external thread transports and session
  keys.
- [`github-agent.md`](github-agent.md) and
  [`github-app-setup.md`](github-app-setup.md) — `@kitsoki` dispatch and GitHub
  App setup.
- [`artifact-annotation.md`](artifact-annotation.md) — unified annotations for
  screenshots, videos, rrweb captures, HTML, and Slidey decks.
- [`visual-ambient.md`](visual-ambient.md) — frame/point/element context fed
  into visual oracles; generalized by artifact annotations.

## Project infrastructure

- [`developer-guide.md`](developer-guide.md) — repository map, local commands,
  tests, PR gates, and extension points.
- [`credentials.md`](credentials.md) — credential storage, env naming, and
  precedence.
- [`embeddings.md`](embeddings.md) — embedding index/store/sidecar and
  `host.agent.search`.
- [`capsules.md`](capsules.md) — hermetic repository fixtures for tests and
  agent validation.
- [`decomposition-graph.md`](decomposition-graph.md) and
  [`graph-grouping-taxonomy.md`](graph-grouping-taxonomy.md) — graph model,
  validation, areas, and initiatives.
- [`repo-history-training.md`](repo-history-training.md) — manifest and
  promotion rule for repo-history training data.

## See also

- [`../tracing/trace-format.md`](../tracing/trace-format.md) — the audit trail
  that makes architecture decisions replayable.
- [`../case-studies/bug-fix.md`](../case-studies/bug-fix.md) — a real workflow
  decomposed into deterministic rooms.
