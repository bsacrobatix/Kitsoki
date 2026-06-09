# Runtime: Layered, cache-friendly system prompt

**Status:** Draft v1. Nothing implemented yet.
**Kind:**   runtime
**Epic:**   — standalone

## Why

Kitsoki shells out to the `claude` CLI from **many** places, and they build the
system prompt two incompatible ways — neither layered, neither grounded in what
kitsoki *is*:

1. **Intent-routing harness** (`internal/harness/prompt.go:23`,
   `claude_cli.go:307`). `buildStablePrefix` emits a routing-specific preamble
   ("You are an intent-routing assistant…") + the Intent Library + the Tool
   Contract, passed via `--system-prompt` (which *replaces* Claude Code's default)
   plus `--exclude-dynamic-system-prompt-sections`. This path is healthy but
   bespoke.

2. **Every oracle host call** — `host.oracle.{ask,decide,task,converse,extract}`
   (`internal/host/oracle_helpers.go:98`, `agents.go:130`). The agent's authored
   `system_prompt` is forwarded via **`--append-system-prompt`**, so it stacks
   **on top of Claude Code's full default coding-agent system prompt** (cwd/env/git
   /memory + "you are Claude Code" tone). An oracle judge therefore inherits a
   large, wrong, irrelevant base prompt, and the author's persona is a footnote
   appended to it.

Neither path tells the model **what kitsoki is** (a deterministic decision graph;
the model is one *pluggable operator* at one recorded decision point; output is
schema-constrained and replayed), nor **what this project is about**. Every agent
re-derives its grounding from a one-paragraph `system_prompt`, and the oracle path
fights ~kilotokens of Claude-Code default that pull it toward acting like a coding
assistant.

We want one smartly-constructed, cache-friendly system prompt, composed from
**kitsoki overview → project context → task at hand**, authored through the
prompt-templating mechanism we already have (`@shared/`/`@story/` overlays,
`{% include %}`/`{% extends %}`), so each oracle *and* each project can tailor it.

## What changes

One shared composer (`internal/sysprompt`) builds every system prompt — for the
routing harness and every oracle verb — as three stacked layers, ordered
most-stable → least-stable so the prefix is byte-identical across a project's
calls and lands in Anthropic's prompt cache. Per-turn data stays out of the system
prompt (it already rides on stdin as the user message). All oracle verbs switch
from `--append-system-prompt` to `--system-prompt` so the kitsoki preamble
*replaces* Claude Code's default instead of stacking under it.

## Impact

- **Code seams:**
  - New `internal/sysprompt/` — the composer + embedded Layer-1 fragment.
  - `internal/harness/prompt.go:23` — `buildStablePrefix` becomes a thin call into the composer (Layer 3 = routing).
  - `internal/host/oracle_helpers.go:98` — `buildBaseCLIArgs` switches `--append-system-prompt` → `--system-prompt` (+ per-verb dynamic-section policy).
  - `internal/host/agents.go:130` — `effectiveSystemPrompt` becomes "compose Layer 3 from agent/inline persona", fed into the composer.
  - `internal/app/types.go` — new optional Layer-2 authoring field (see Vocabulary).
  - `internal/render/prompt_renderer.go` — the `PromptPath`/overlay renderer is reused verbatim to render each layer; no change beyond a new resolution entry for the builtin fragment.
- **Vocabulary:** one new app-meta field + one conventional prompt path + an embedded engine fragment (table below).
- **Stories affected:** all stories that declare `agents:` gain the kitsoki + project grounding for free; no story *must* change. `docs/skills/*` agents unaffected (they run under Claude Code proper, not the oracle path).
- **Backward compat:** existing `agents:` `system_prompt`/`system_prompt_path` keep working as Layer 3. The visible behavior change is that oracle agents **no longer see Claude Code's default system prompt** — intended, but `task` is treated specially (keeps repo context). Escape hatch below.
- **Docs on ship:** new `docs/architecture/system-prompt.md` (the layer model + cache rationale); cross-links from `docs/architecture/hosts.md`, `docs/stories/prompts.md`, `docs/stories/agents`/authoring.

## Vocabulary changes

| Kind | Name | Shape | Notes |
|---|---|---|---|
| embedded fragment | `sysprompt/kitsoki.md` | pongo template, `go:embed` | Layer 1. What kitsoki is / how it works / the operator's role. Identical for every call everywhere → top of prompt → maximal cache reuse. Not normally overridden; overridable via `@shared/` for experiments. |
| app-meta field | `app.context` / `app.context_path` | string / path | Layer 2. The project's domain, purpose, voice. Rendered once per process; stable for the project's lifetime. Path resolves through the existing overlay search. |
| convention | `prompts/_project.md` | prompt file | Optional fallback for Layer 2 when `app.context_path` is unset — discovered via `PromptPath`, so an overlay/import can supply it without touching `app.yaml`. |
| (existing) | `AgentDecl.system_prompt[_path]`, off-path `persona`/`agent` | string / path | Layer 3, the task persona. Unchanged surface; now composed *under* Layers 1–2 instead of on top of Claude Code's default. |

## The model

Three layers, composed once, joined most-stable → least-stable:

```
┌─ Layer 1 — KITSOKI (engine builtin, identical everywhere) ────────────┐
│  What kitsoki is: a deterministic decision graph. You are ONE          │  ← best
│  pluggable operator at ONE recorded decision point. Your output is     │  cache
│  schema-constrained and may be replayed. Decide; do not converse.      │  hit
├─ Layer 2 — PROJECT (per-app, authored; stable for process lifetime) ───┤
│  This app is "Oregon Trail": a survival sim with a frontier voice…     │
├─ Layer 3 — TASK (per-call; stable across one agent/verb's calls) ──────┤
│  Verb contract (route an intent / emit a typed verdict / do agentic    │  ← least
│  work) + the agent persona (system_prompt) + (routing only) the        │  stable
│  Intent Library + Tool Contract.                                       │
└────────────────────────────────────────────────────────────────────────┘
        ⇩ passed as claude --system-prompt (REPLACES CC default)
   Per-turn context (state/view/allowed intents/world/recent turns) and the
   user utterance ride on STDIN as the user message — never in the system
   prompt, so the prefix stays byte-stable and cacheable.
```

**Interpretive vs deterministic:** the *content* of each layer is deterministic
text (templates rendered against the app + a fixed scope); the only interpretive
act remains the model's verdict, recorded as today. The composer adds no new
decision point — it reshapes the prompt the existing decision is made under.

### Composer API (sketch)

```go
package sysprompt

// Verb selects per-verb policy (Tool Contract inclusion, replace-vs-keep
// dynamic CC sections — see policy table).
type Verb int
const ( Route Verb = iota; Ask; Decide; Task; Converse; Extract )

type Spec struct {
    Verb       Verb
    AgentPrompt string            // Layer-3 persona (resolved system_prompt / inline / off-path persona)
    Routing    *RoutingExtras     // intent library + tool contract; nil for non-routing verbs
}

type Composed struct {
    SystemPrompt   string
    Layers         []string // {"kitsoki","project","task"} present — for the trace, not the secret
    ExcludeDynamic bool     // → --exclude-dynamic-system-prompt-sections
}

// Compose renders the three layers through the app's PromptPath renderer and
// joins them. Layer 1 is the embedded fragment; Layer 2 is app.context[_path]
// (or prompts/_project.md); Layer 3 is verb-specific.
func (c *Composer) Compose(spec Spec) (Composed, error)
```

Both call sites collapse onto this:

- **Harness** (`buildStablePrefix`): `Compose(Spec{Verb: Route, Routing: …})`.
- **Oracle handlers** (`buildBaseCLIArgs`): `Compose(Spec{Verb, AgentPrompt: effectiveSystemPrompt(...)})`, then `--system-prompt <composed>` (+ exclude flag per policy) instead of `--append-system-prompt`.

### Authoring through the existing mechanism

Each layer is rendered by the **same** `render.AppRenderer`/`PromptPath` used for
prompts today (`internal/render/prompt_renderer.go`), so every layer can
`{% include "@shared/…" %}` or `{% extends "@story/…" %}`. That is how "each
oracle can tailor the system prompt intelligently, and projects can too":

- A **project** sets `app.context_path: prompts/_project.md`, and that file may
  `{% include "@shared/domain-glossary.md" %}`.
- An **oracle/agent** keeps authoring `system_prompt_path:` as today; the file can
  now `{% extends "@shared/judge-base.md" %}` to share a judge persona across
  agents.
- The **kitsoki** layer is an embedded default; an experiment can shadow it with a
  `@shared/sysprompt/kitsoki.md` overlay without recompiling.

### Replace-vs-append and the dynamic-sections policy

`--exclude-dynamic-system-prompt-sections` only takes effect with `--system-prompt`.
Routing-class verbs want a hermetic prompt; `task` agents doing real repo work
legitimately want Claude Code's cwd/git/env sections. Policy:

| Verb | base prompt | CC dynamic sections (cwd/git/env/memory) |
|---|---|---|
| route (harness) | replace (`--system-prompt`) | exclude |
| ask | replace | exclude |
| decide | replace | exclude |
| converse | replace | exclude |
| extract | replace | exclude |
| **task** | replace | **keep** (agentic work needs repo context) |

Escape hatch: an agent may set `inherit_claude_default: true` to fall back to the
old `--append-system-prompt` behavior for a migration period (default false).

### Trace + isolation

- `OracleCalled.SystemPrompt` (`internal/host/oracle_dispatch.go:159`) keeps
  recording the composed prompt; add a `system_prompt_layers` breadcrumb
  (`["kitsoki","project","task"]` + per-layer byte counts) so the timeline shows
  which layers were present without re-deriving them. Never log secrets — same
  discipline as the credential resolver.
- `--setting-sources project,local` isolation (`agents.go:276`) is untouched;
  Layer 1 is *engine* text, orthogonal to operator-global plugin isolation.

### Cache rationale

Anthropic caches a stable prefix (≥1024 tokens). Ordering Layer 1 (global) → 2
(project) → 3 (per-agent) and keeping per-turn data on stdin makes the system
prompt byte-identical across every call a given agent/verb makes in a project →
cache hits across a whole run. The existing 1024-token-threshold caveat in
`prompt.go:19` now applies to the *composed* prefix, which the kitsoki + project
layers push comfortably over the line for most apps.

## Delivery (phased)

- **Phase 0 — composer + Layer 1.** New `internal/sysprompt` with the embedded
  kitsoki fragment; route the harness through it (adds Layer 1 to routing; no
  other behavior change). Unit tests assert layer order + presence; harness
  golden test updated.
- **Phase A — oracle verbs.** `buildBaseCLIArgs` → `--system-prompt` + per-verb
  dynamic-section policy + `inherit_claude_default` escape hatch. Update the
  `--append-system-prompt` assertions in `internal/host/oracle_*_test.go` to the
  new flag (these are stub-CLI tests — no real LLM). Live smoke test of one
  `decide` and one `task` via `--harness claude`.
- **Phase B — Layer 2 authoring.** `app.context[_path]` + `prompts/_project.md`
  convention; loader resolves the path through `PromptPath`. Add a project
  context to one demo app (e.g. Oregon Trail) as the worked example.
- **Phase C — docs + cleanup.** Write `docs/architecture/system-prompt.md`,
  cross-link, delete this proposal.

## Open questions

1. **`task` default — keep or exclude CC sections?** The table keeps them, betting
   that task agents want repo context. If most tasks are self-contained, flip the
   default and let repo-aware tasks opt in. Needs one real dogfood task to settle.
2. **Layer 2 surface — `app.context` field vs pure convention?** A field is
   discoverable and validated; the `prompts/_project.md` convention composes
   better with overlays/imports. Proposal ships both (field wins, convention is
   the fallback) — is that one knob too many?
3. **Inline `system_prompt` semantics.** Today inline `system_prompt` *replaces*
   the agent's prompt (`effectiveSystemPrompt`). Under layering it still only
   replaces **Layer 3** — confirm that's the intended scope (Layers 1–2 always
   apply; an author can't drop the kitsoki grounding per-call). This protects the
   moat (every call is grounded) but removes a foot-gun some power user may want.
4. **Does Layer 1 belong in the live (Messages API) harness too?** The live
   harness sets `system` on the request directly; it should compose identically so
   `--harness live` and `--harness claude` produce the same grounding. Confirm the
   composer is harness-agnostic (it is, by construction) and wire `live*.go` in
   Phase 0.

## Testing

Per repo policy: **no real LLM**. Composer is pure string assembly → table tests
on layer order/presence/escaping. Oracle-handler tests keep using the stub CLI
(`fake-oracle.sh`/`fake-claude.sh`) and assert the new `--system-prompt` flag and
the per-verb exclude policy. One gated live smoke test (`--harness claude`) per
the integration-verify rule, run only when explicitly requested.
