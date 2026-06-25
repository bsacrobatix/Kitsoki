# Project onboarding — getting started with kitsoki in your project

You have the `kitsoki` binary installed (if not, do
[getting-started.md](getting-started.md) first). This guide takes *your*
repository — any language, any stack — from "kitsoki is on my PATH" to a
**fully working kitsoki environment** committed into the repo: a runnable
dev-story instance, the studio MCP registered for your coding agent, and the
kitsoki skill/agent toolkit installed.

> **The 30-second version.** From your project root:
> ```sh
> kitsoki run /path/to/Kitsoki/stories/dev-story/app.yaml   # → type: onboard .
> ```
> Walk the four onboarding rooms (review → apply) and you're done. The rest of
> this page explains what that produces and the standalone command behind it.

---

## What onboarding installs

Onboarding writes a small, **auditable, checked-in** set of files. None of it is
generated at runtime or hidden in a cache — it all lives in your repo so it
travels with every clone and every collaborator.

| Path | What | Why |
|---|---|---|
| `.kitsoki.yaml` | `story_dirs: [./stories]` + `default_story` | so `kitsoki` discovers your instance |
| `.kitsoki/project-profile.yaml` | declarative profile (stack, commands, conventions) | the discovered description of your project |
| `stories/<id>-dev/app.yaml` | a dev-story **instance** that imports `@kitsoki/dev-story` | your runnable engineer's-day hub |
| `stories/<id>-dev/README.md` | how to run the instance | — |
| `.mcp.json` | registers the **kitsoki studio MCP** server | so Claude Code / Cursor / any MCP client can drive kitsoki here |
| `.agents/skills/<name>/` · `.agents/agents/<name>.md` | the kitsoki skill + subagent **toolkit** (source of truth) | the Codex-standard location |
| `.claude/skills/<name>` · `.claude/agents/<name>.md` | relative symlinks into `.agents/` | so Claude Code discovers them |
| `.gitignore` | appended with kitsoki runtime entries | keeps sessions/artifacts out of git |

The layout mirrors the kitsoki repo's own convention exactly: `.agents/` is the
source of truth, `.claude/` is relative symlinks into it. Agent definitions are
Claude-specific, but they are still sourced from `.agents/agents` and linked the
same way (see the repo `AGENTS.md`).

The skills, agents, and base stories are **embedded in the binary**
(`internal/baseskills`, `internal/basestories`), so onboarding works in a
project that has no kitsoki source checkout on disk — the binary is the only
dependency.

---

## Two ways to run it

### 1. The onboarding pipeline (recommended)

The [dev-story](../stories/dev-story/README.md) hub ships a four-room onboarding
pipeline that **discovers** your project, lets you **review** the profile, then
**applies** everything above. Run the dev-story app from your project root and
type an onboarding request:

```sh
cd ~/code/my-project
kitsoki run /path/to/Kitsoki/stories/dev-story/app.yaml
#   > onboard .                 # or: onboard ~/code/my-project
#   > continue                  # review the discovered profile
#   > continue (confirm)        # apply: writes config + instance + toolkit + MCP
```

Discovery infers the project id, title, stack, and dev/test/build commands; the
apply step writes the files and runs the toolkit + MCP install. The full
mechanics — rooms, the discovery/apply scripts, the world keys, the no-LLM flow
fixture — are in
[stories/dev-story-onboarding.md](stories/dev-story-onboarding.md).

Headless equivalent (no TUI), useful for scripting or CI:

```sh
APP=/path/to/Kitsoki/stories/dev-story/app.yaml
kitsoki session create   --app "$APP" --key local:onboard
kitsoki session continue --app "$APP" --key local:onboard \
    --intent work --slots '{"request":"onboard /abs/path/to/my-project"}'
kitsoki session continue --app "$APP" --key local:onboard --intent init_discovered
kitsoki session continue --app "$APP" --key local:onboard --intent confirm_init
kitsoki session continue --app "$APP" --key local:onboard --intent init_applied
```

### 2. Just the toolkit + MCP — `kitsoki project-tools install`

If you only want the agent toolkit and the studio MCP (you already have an
instance, or you onboarded an older repo before this step existed), run the
standalone command the apply step calls:

```sh
cd ~/code/my-project
kitsoki project-tools install --target .
#   skills: 17 linked into .claude/skills
#   agents: 2 linked into .claude/agents
#   mcp:    registered kitsoki server in .../my-project/.mcp.json
```

It is idempotent: source trees are refreshed from the binary, our own symlinks
are re-pointed, an existing `.mcp.json` is **merged** (other servers preserved),
and a real file a human placed at a link path is left untouched. Add `--json`
for a machine-readable report.

---

## After onboarding — using it

**Run your instance.** From the project root, the default story is your new
instance:

```sh
kitsoki run stories/<id>-dev/app.yaml      # the engineer's-day workbench
```

**Drive kitsoki from your coding agent.** With `.mcp.json` registered, an MCP
client (Claude Code, Cursor, Claude Desktop) attached to this repo gets the
kitsoki **studio** tools — author/validate/test stories, drive sessions, render
the TUI/web — all through one facade. See
[architecture/mcp-studio.md](architecture/mcp-studio.md). The
`kitsoki-mcp-driver` agent (installed into `.claude/agents/`) is purpose-built
to orchestrate kitsoki entirely through that surface.

**Use the skills.** The installed skills (`.claude/skills/`) cover authoring,
debugging, UI demos/QA, dogfooding, and more — your agent discovers them
automatically.

---

## See also

- [getting-started.md](getting-started.md) — install the toolchain + binary
  first; §5 covers choosing the LLM provider/model.
- [stories/dev-story-onboarding.md](stories/dev-story-onboarding.md) — the
  onboarding pipeline in detail (the dev-story `init` rooms).
- [../stories/dev-story/README.md](../stories/dev-story/README.md) — the
  dev-story hub your instance imports.
- [architecture/mcp-studio.md](architecture/mcp-studio.md) — the studio MCP that
  `.mcp.json` registers.
