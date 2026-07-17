# Agent Mode (`agent:<name>` sessions)

An **agent session** is a normal story session whose AppDef is synthesized in
memory from an agent definition — a one-room story addressed by the virtual
story path `agent:<name>`. There is no new session kind, schema column, or
RPC contract: `agent:<name>` is an additional value accepted anywhere a story
path is accepted, and everything downstream (rooms, traces, chats, resume,
daemon jobs, web) treats the result as an ordinary story.

This composes two mechanisms the codebase already ships: the synthesized
implicit root ([the blank root that grows](../../stories/imports.md#the-blank-root-that-grows--the-implicit-project-root))
and the [`workbench:` room macro](../../architecture/room-workbench.md) — see
that page's [Synthesized agent root](../../architecture/room-workbench.md#the-synthesized-agent-root-agentname)
section for the load-time contract. The resolver/synthesizer lives in
`internal/agentroot`.

## Starting an agent session

```sh
kitsoki run agent:kitsoki-mcp-driver             # TUI session directly in the agent
kitsoki run agent:kitsoki-mcp-driver --continue  # resume (re-resolves + re-synthesizes)
kitsoki agent run kitsoki-mcp-driver             # sugar; identical, all run flags pass through
kitsoki session create --app agent:demo --key jira:PROJ-1   # headless keyed session
kitsoki session continue --app agent:demo --key jira:PROJ-1 --raw "..."
```

Every surface that loads a story path accepts the scheme:

- **CLI** — `run`, `session create/continue`, `turn`, `drive`, `render`,
  `inspect` all head-check the scheme before treating the argument as a file
  path (`cmd/kitsoki/session.go`, `loadAgentSchemeApp`).
- **Web** — `runstatus.session.new {story_path: "agent:<name>"}` works
  unchanged; the home screen's Agents section is backed by
  `runstatus.agents.list` (below) and a card click opens a session on the
  row's precomputed `story_path`. In daemon mode the session is a durable
  artifact job with a stable `/s/<id>` link.
- **TUI** — `/agents` opens the catalog selector and switches into the chosen
  agent through the same story-switch loop `/stories` uses.
- **MCP studio** — `session.new {story_path: "agent:<name>"}`
  (`internal/mcp/studio/session_runtime.go`); no wire-contract change.
- **Flow tests** — `kitsoki test flows agent:<name> --flows <fixture.yaml>`
  runs a flow fixture against the synthesized root (`internal/testrunner`
  head-checks the scheme; `--flows` is required since there is no app dir to
  default from).

`/reload` (and definition edits generally) re-resolve and re-synthesize the
root — an agent TOML or library edit takes effect on the same
`Reload` + `RerunOnEnter` path a story file edit travels.

## Resolution order

`agent:<name>` reuses the freestanding-agent search order of
[`kitsoki agent launch`](launch.md), story-independent:

1. **Project TOML dirs**, in order: `.kitsoki/agents/`, `.codex/agents/`,
   `~/.codex/agents/`. Per directory, `<name>.local.toml` shadows
   `<name>.toml`; `extends` chains and the embedded-base overlay fallback
   behave exactly as they do for `agent launch`.
2. **Embedded agent library** (`agents/<name>.md`, materialized from the
   baseskills bundle).
3. **Builtin registry** (`internal/agents.NewBuiltins`).

The first source that defines the name wins; an unknown name is a load error
that lists the known catalog (same UX as an invalid story path). Collisions
are deterministic and visible: `agent list` reports the shadowed sources.

## The catalog

```sh
kitsoki agent list           # NAME  SOURCE  EFFECT  DESCRIPTION (+ shadows)
kitsoki agent list --json    # full rows, multi-line descriptions preserved
```

The same merged catalog backs the TUI `/agents` selector and the web home
screen via the RPC:

```
runstatus.agents.list {} → []AgentInfo
```

Each row carries `{name, source, description, effect, shadows, story_path}`
(`internal/runstatus/server/provider.go`); `story_path` is the precomputed
`agent:<name>` value `session.new` accepts, so clients never string-build the
scheme. The RPC is an optional provider extension (`AgentLister`) — providers
that don't implement it report an empty catalog, never an error.

## What gets synthesized

`agentroot.Synthesize` builds a one-room story and runs it through the normal
loader, so every load-time pass (workbench desugaring, off-ramp capture,
validation) sees exactly what a hand-written story would produce:

- **App identity**: `app.id = "agent:<name>"`; `app.version` is a content
  hash of the resolved definition, so resume detects definition drift the
  same way a story edit does. Sessions bind as
  `sessions.app_id = "agent:<name>"` — the app_id *is* the binding.
- **One `agents:` decl** translated from the definition (system prompt,
  model, effort, tools/toolbox, cwd, MCP servers). A builtin's env-based
  default cwd (the `kitsoki-*` builtins' `${KITSOKI_REPO}`) is expanded at
  resolve time; when the variable is unset the cwd is dropped and the session
  working directory applies, so every row `agent list` returns stays
  startable in the same environment.
- **One room, `agent`**, in one of two shapes by the resolved
  [effect class](../../architecture/room-workbench.md):
  - **`write` agents** desugar via a `workbench:` block — the proven
    landing-room shape: `write_mode: read_only` gate, off-ramp Q&A,
    free-text capture, and an `on_enter host.agent.task` dispatch reading
    `{{ world.workdir }}`. Zero new permission surface.
  - **`read`/`pure`/`external` agents** get the conversational shape
    (`agent_off_ramp: {agent: <name>, capture_free_text: true}`): free text
    sinks into a persistent per-room conversation. External agents take this
    shape too (the workbench's forced `read_only` posture contradicts an
    external-effect agent at load time) but keep the launch-policy preflight
    below — their tool surface is the most privileged tier.

Backend and model selection compose unchanged: `--agent`/`$KITSOKI_AGENT`
picks the [backend](backends.md), [harness profiles](harness-profiles.md)
pick provider/model/effort, and a model/effort pinned by the resolved
definition rides the synthesized agent decl with the same precedence every
agent call already honors.

## Sandboxing

Agent mode adds no sandbox machinery; it composes the existing gates:

1. **Launch-policy preflight** — synthesizing a `write` or `external` agent
   runs the [`agent_launch_policy`](launch-policy.md) check (verb
   `agent.mode`) against the session working directory *before any session
   exists*. A denial fails the load with the auditable decision plus capsule
   guidance: run from a managed capsule workspace
   (`scripts/dev-workspace.sh create` / `kitsoki capsule`) or extend
   `allowed_roots`. Read-only agents never dispatch mutating work and are
   exempt. In-session dispatches are gated again by the same policy on the
   orchestrator context.
2. **Write-mode gate** — a write agent's workbench room starts
   `read_only`; every mutating tool call holds for an operator write-mode
   grant (or a headless deny). This, not the static tool list, is the
   runtime protection.
3. **Project TOML mapping** — a freestanding TOML agent declares no tool
   list, so its surface derives from `sandbox_mode`: `read-only` resolves to
   a read agent with `[Read, Grep, Glob]`; anything else resolves to a write
   agent with the full `[Read, Grep, Glob, Edit, Write, Bash]` workbench
   surface behind the gates above.

## Agent mode vs `kitsoki agent launch`

| | [`agent launch`](launch.md) | agent mode (`agent:<name>`) |
|---|---|---|
| What runs | forks the native backend CLI (`claude`/`codex`/…) as an external process | an in-kitsoki story session dispatching the agent through `host.agent.*` |
| Surface | argv/dry-run plans, interactive backend TUIs, CodeAct | rooms, write-mode gate, traces, persistent chats, web/TUI/MCP session surfaces |
| Definitions | same freestanding TOML / library / builtin definitions | same — one search order, two consumers |
| Policy | launch-policy preflight per launch | same policy, verb `agent.mode`, at synthesis + per dispatch |

Use `agent launch` when you want the backend CLI itself (or a one-shot
external process); use agent mode when you want the agent inside a governed,
resumable kitsoki session.

## Deferred: `/agent` switching

Switching agents mid-session is deliberately not implemented. The seam is the
TUI's `/stories` story-switch outer loop (exit → re-enter the run loop with
the new path), which `/agents` already rides for *new* sessions — a future
`/agent <name>` can reuse that path, or later the harness-profile
`SetSelection` next-turn-snapshot pattern for same-session switching. Nothing
in agent mode binds session identity to a backend-native session id, so both
options stay open.
