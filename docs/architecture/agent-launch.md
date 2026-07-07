# Agent Launch CLI

`kitsoki agent launch` turns either a reusable story `agents:` declaration or a
freestanding Codex agent file into a concrete task-agent CLI launch for Claude,
Codex, or another supported backend. It is intentionally a resolver over
existing agent and harness-profile config, not a second agent schema.

## Contract

Story-backed launch uses a story app and agent name:

```sh
kitsoki agent launch --app stories/git-ops/app.yaml --agent conflict_resolver --task "Resolve the listed conflicts"
```

Freestanding Codex launch omits `--app` and resolves
`.codex/agents/<name>.toml` from the current project, or the file passed with
`--agent-file`:

```sh
kitsoki agent launch --agent kitsoki-mcp-driver --backend codex --task-file .context/drive.md
```

To open an interactive Codex session with the same freestanding agent and MCP
attachment, omit the task; no task file is required:

```sh
kitsoki agent launch --agent kitsoki-mcp-driver --backend codex
```

`run_as_user` delegation is currently disabled at runtime. Kitsoki still parses
existing `.kitsoki.local.yaml` `agent_user_delegation:` blocks so local config
files keep loading, but `kitsoki agent launch` ignores `wrapper_bin`, launches
the normal backend binary, and omits `run_as_user` from the launch plan:

```yaml
agent_user_delegation:
  enabled: true
  run_as_user: kitsoki-agent
  wrapper_bin: /Users/Shared/kitsoki/agent-bin
```

The setup story and receipt shape remain in the tree for a future re-enable,
but missing or incomplete delegation config does not produce a macOS setup
warning while runtime delegation is disabled.

To open a normal interactive backend session without an app, agent file, MCP
wrapper, or Kitsoki replacement prompt, use raw interactive launch:

```sh
kitsoki agent launch --raw --interactive --backend codex --working-dir /tmp/kitsoki-capsules/clean-repo
```

For freestanding Codex agents, `[mcp_servers.*]` blocks are materialized into
the same `--mcp-config` shape Claude uses, then translated into Codex
`-c mcp_servers...` overrides. This is the Codex analogue of launching a
Claude Code agent with the studio MCP attached, for example
`claude --agent kitsoki-mcp-driver`.

To give a launched agent code-as-action without giving it Bash/Python/Node,
attach the standalone CodeAct MCP server instead of shell tools:

```toml
[mcp_servers.codeact]
command = "kitsoki"
args = [
  "mcp-codeact",
  "--working-dir", ".",
  "--capabilities-json", '{"fs":{"read":["**"]},"vcs":"read"}',
]
```

The server exposes `codeact_eval`; its startup capabilities are the authority
ceiling, so the launched agent can supply snippets and data but cannot grant
itself new filesystem, probe, or network access.

Task-backed freestanding launch uses `codex exec`. Freestanding launch with no
task uses top-level `codex [OPTIONS] [PROMPT]`, so the terminal opens the Codex
TUI with the agent instructions as the initial prompt. Interactive Codex launch
uses `--dangerously-bypass-approvals-and-sandbox`, so the agent TOML
`sandbox_mode` is not forwarded on this path. Pass `--interactive` only when you
want to force the interactive path despite other launch inputs.

Raw interactive launch also uses the backend's top-level interactive CLI, but
passes no app/agent prompt at all. For Codex it also uses
`--dangerously-bypass-approvals-and-sandbox`. It is intended for native logged-in
host CLI sessions, especially macOS subscription workflows.

By default it is a no-provider dry run. It prints a JSON launch plan with:

- the selected backend and binary,
- the concrete argv after backend translation,
- the resolved working directory,
- the effective model and effort,
- the resolved tool surface,
- redacted provider environment keys,
- the stdin prompt that will be sent to the backend.
- the `launch_policy` decision when `agent_launch_policy:` is enabled.

Pass `--exec` to actually run the selected CLI:

```sh
kitsoki agent launch --app stories/prd/app.yaml --agent author --profile synthetic-codex --task-file .context/prd-task.md --exec
```

Tests and normal dry-run inspection do not call live LLMs.

## Resolution

For story-backed launch, the command composes three existing layers:

1. Story `agents:` supplies the persona, default cwd, tools, model, effort, and
   provider name.
2. `.kitsoki.yaml` / `.kitsoki.local.yaml` `harness_profiles` supply backend,
   model, effort, and provider env. `--profile` selects one; otherwise the
   config `default_profile` is used when present.
3. Task flags supply per-launch overrides such as `--working-dir`, `--model`,
   `--effort`, `--backend`, `--add-dir`, and `--env KEY=VALUE`.

Profile model and effort override story-local defaults so a Codex or
synthetic.new profile does not inherit a Claude-only model id from the room
agent. When `--backend` explicitly selects a different backend than the
implicit default profile, that default profile's model, effort, and env are not
applied; pass an explicit matching `--profile` when you want profile settings.
An explicit `--profile` whose backend conflicts with `--backend` is rejected.
Extra env values are merged last and are redacted in dry-run output.

For freestanding launch, `.codex/agents/<name>.toml` supplies developer
instructions, model, effort, and MCP servers. The harness profile, when
selected, still wins for backend/model/effort/env so the operator can reuse the
same profile names as story sessions.

## Backends

The command builds the same neutral Claude-shaped invocation used by host agent
handlers, then asks the host backend translator for the concrete invocation.
That means Codex dry-runs show the real `codex exec ...` argv, including model
and working-directory translation, while Claude dry-runs show the direct Claude
argv.

`--exec` uses the same host runner as the existing harness path, with the
selected backend installed on context and provider env applied to the child
process.

## Safety

Read-only agents (`external_side_effect: false`) launch with Claude's enforcing
`default` permission mode and a hard deny-list for mutation tools. All launches
deny headless escape tools such as `AskUserQuestion`, `Agent`, and `Task`.

When `.kitsoki.yaml` / `.kitsoki.local.yaml` enables
[`agent_launch_policy:`](./agent-launch-policy.md), launch planning rejects
protected roots, protected branches, and non-capsule workspaces before emitting
a command plan. The same guard applies to raw interactive sessions.

Launch policy is a preflight guard, not a kernel/filesystem sandbox. Use it to
keep agents out of the protected checkout and inside opened capsules. The
macOS `run_as_user` wrapper path is temporarily disabled, so write-capable
backend CLIs currently run as the invoking user. Use `with.sandbox` on hosted
calls when a story also needs runtime supervision.
