# Harness Profiles

> **Status:** operator-facing reference for the `harness_profiles:` block and
> the live provider/model switch. A **harness profile** is a named, operator-
> selectable bundle of the oracle-selection axes — *which backend CLI is forked*,
> *which endpoint it talks to*, and *which model it defaults to* — that a live
> session can switch between from the TUI (`/provider`, `/model`) or the web
> header picker. The switch takes effect on the **next** turn.

A kitsoki session has four orthogonal oracle-selection axes, each documented on
its own page:

- **backend** — which coding-agent CLI is forked
  ([oracle-backends.md](./oracle-backends.md)): `claude | copilot | codex`.
- **provider** — an env retarget of the forked CLI's subprocess
  ([oracle-providers.md](./oracle-providers.md)).
- **plugin** — an alternate component that answers
  ([oracle-plugin.md](./oracle-plugin.md)), e.g. `builtin.local_llm` (llama.cpp).
- **model** — the `--model` passed to the call.

Historically each axis was frozen at startup and reachable only through flags,
env, or per-story YAML — never from a live session. A **harness profile**
collapses these axes behind one operator-facing name so an operator picks a
*profile* (and optionally a *model*) instead of learning the taxonomy.

## Configuration

Profiles are declared in the machine-global `.kitsoki.yaml` (the same file that
carries `story_dirs`), loaded on both `kitsoki run` (TUI) and `kitsoki web`:

```yaml
# .kitsoki.yaml
default_profile: claude-native          # the profile new sessions start on
harness_profiles:
  claude-native:                        # your native Anthropic Claude Code subscription
    backend: claude                     # (ambient auth; the default)

  synthetic-claude:                     # claude-code pointed at synthetic.new
    backend: claude
    model: hf:Qwen/Qwen2.5-Coder-32B-Instruct
    models:                             # catalog the /model command + web dropdown list
      - hf:Qwen/Qwen2.5-Coder-32B-Instruct
      - hf:meta-llama/Llama-3.3-70B-Instruct
    env:
      ANTHROPIC_BASE_URL: https://api.synthetic.new/anthropic
      ANTHROPIC_AUTH_TOKEN: "${SYNTHETIC_API_KEY}"

  synthetic-codex:                      # the codex CLI pointed at synthetic.new
    backend: codex
    env:
      OPENAI_BASE_URL: https://api.synthetic.new/openai
      OPENAI_API_KEY: "${SYNTHETIC_API_KEY}"

  codex-native: { backend: codex }      # codex's own config/auth

  llama-local:                          # a local llama.cpp model (plugin path)
    plugin: builtin.local_llm
    model: h200/gpt-oss-120b
```

| Field | Meaning |
|---|---|
| `backend` | `claude \| copilot \| codex`; empty ⇒ `claude`. Ignored when `plugin` is set. |
| `model` | default `--model` for the profile; an explicit per-effect/agent model still wins. |
| `models` | catalog the `/model` command and web dropdown list. When set, `model` (and any operator model pick) must be a member. |
| `env` | env overrides merged onto the forked CLI subprocess. `${VAR}`-expanded at **load time** (an unset var is a hard error, mirroring `providers:`). **Never recorded in traces.** |
| `plugin` | routes through an oracle plugin (e.g. `builtin.local_llm`) instead of forking a backend CLI. |
| `default_profile` (top-level) | the profile new sessions start on; must name a declared profile. Omitted ⇒ the flag-derived static default (today's `--oracle`/`--model`). |

**Secrets** never live in the file: `env` values use `${VAR}` interpolation
against the process environment. With no `harness_profiles:` block the static
flag/env path is preserved byte-for-byte.

### Why `env` works for codex/openai, not just claude

A provider/profile's `env` is merged onto **every** backend CLI's subprocess
environment (`internal/host/oracle_runner.go`, `envWithProvider`), not only the
`claude` one. So a `claude`-backed profile sets `ANTHROPIC_BASE_URL`/
`ANTHROPIC_AUTH_TOKEN` (which the `claude` CLI reads) and a `codex`-backed
profile sets `OPENAI_BASE_URL`/`OPENAI_API_KEY` (which the `codex` CLI reads).
That is what makes "synthetic.new on codex" real with no engine change — the
merge was already backend-agnostic; only the *variable names* are backend-
specific.

## Selecting a profile

| Surface | How |
|---|---|
| **TUI** | `/provider` lists the profiles (active one marked) and `/provider <name\|n>` switches; `/model` lists the active profile's catalog and `/model <id\|n>` switches the model. See [docs/tui/README.md](../tui/README.md). |
| **Web** | A provider dropdown + a dependent model dropdown in the session header; switching fires the `runstatus.session.set_selection` RPC. See [docs/web/README.md](../web/README.md). |

Both drive the same orchestrator API — `Profiles()`, `Selection()`,
`SetSelection(profile, model)` — exposed to the web via the optional
`HarnessController` driver interface.

### Resolution & precedence

The selection is held per session behind a mutex and **resolved once per
dispatch**. Precedence (highest first), preserving today's mental model:

> per-effect `with: { model / provider }` › agent default › **active profile** › flag-derived static default

So a story/effect that pins `model: opus` or names a `provider:` still wins over
the operator's profile — the profile only fills what the call left blank, and is
installed as an implicit, lowest-precedence provider (`applyProvider` in
`internal/host/agents.go`).

### Next-turn semantics

A switch rebuilds the selection lazily: **every interpretive call from the switch
on uses the new selection; the one already in flight finishes on the old one.**
There is no mid-flight cancellation. A single snapshot per dispatch means a
concurrent switch can never tear one call.

## Trace

Every `oracle.call.start` already stamps the model; a session that selected a
profile also stamps `profile` (`OracleCalledPayload.Profile`), so a transcript
line reads `oracle.decide · profile=synthetic-codex · model=hf:Qwen…`. Only the
profile name, backend, and model are recorded — **never the `env` secrets**.

## Real-provider runbook

The three providers the feature targets, end to end:

1. **Put your synthetic.new key in the environment** the kitsoki process inherits
   — it is referenced as `${SYNTHETIC_API_KEY}` in `.kitsoki.yaml`, never inlined:

   ```bash
   export SYNTHETIC_API_KEY=sk-...        # in your shell / profile / .envrc
   ```

   (The Anthropic-compatible base URL for synthetic.new is assumed to be
   `https://api.synthetic.new/anthropic` and the OpenAI-compatible one
   `https://api.synthetic.new/openai`; adjust the `env` URLs if yours differ.)

2. **Native Anthropic Claude Code** — `claude-native`: ambient auth, nothing to
   set. Verified live: a real `kitsoki turn` routes free text → intent with the
   `claude` backend (no cassette).

3. **Launch with the profiles and pick live:**

   ```bash
   kitsoki web --stories-dir stories            # or: kitsoki run <story>/app.yaml
   ```

   In the web header, pick `synthetic-claude` (and a model from its catalog), or
   `synthetic-codex`, then submit a turn. The trace row shows `profile=…` and the
   chosen model's real answer. In the TUI, `/provider synthetic-codex` then drive.

Automated tests never exercise these — real-LLM verification is operator-driven
and gated, per CLAUDE.md.

## Backward compatibility

Fully additive, default-off. Existing flags/env/`providers:`/`oracle_plugins:`
keep working unchanged; a profile is a *named* bundle of the same overrides
applied through the same merge points. With no profiles declared, `/provider`
and `/model` report the single flag-derived default and the web picker hides.
