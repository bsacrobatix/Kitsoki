# Proposal — Meta mode (Phase C + D residue)

**Status:** v2 trimmed. Phase A (per-context agents + meta mode) and
Phase B (edit-mode removal) shipped on `feat/meta-mode`. Shipped
sections moved to [`../meta-mode.md`](../meta-mode.md). This file now
carries Phase C / D only.

**Recap.** Phase A delivered the `agents:` top-level YAML block with a
builtin override mechanism, the `meta_modes:` block with `/meta`
slash-command dispatch and persistent chats keyed by `(AppID, "meta:<n>",
state)`, the meta-mode controller (`internal/metamode`), and the
per-call `agent:` arg on `host.oracle.ask_with_mcp`. Phase B replaced
edit mode wholesale: each in-tree app declares `meta_modes.story`, the
Esc-menu "edit" entry is gone, and `internal/tui/edit.go` is deleted.
What remains in design lives below.

---

## 1. Phase C — `self` and `bug` meta modes

Two new modes plus their backing agents:

| Mode | Agent              | CWD                  | Tool scope                                | Side effect              |
|------|--------------------|----------------------|-------------------------------------------|--------------------------|
| self | kitsoki-engineer   | `${KITSOKI_REPO}`    | filesystem.{read,write} + shell + git     | Repo branch / PR         |
| bug  | bug-reporter       | n/a                  | bugs.create                               | Issue created in tracker |

Two new builtin agents in `internal/agents`. `kitsoki-engineer` is the
analogue of `story-author` but for the kitsoki repo itself: it edits
Go code rather than YAML, runs the test suite, and (probably) opens
PRs. `bug-reporter` is narrower — it gathers state + reproduction
context and creates an issue. Both are pre-registered as builtins so
no app needs to declare them; an app can opt out by overriding under
the same name in its `agents:` block.

### Open design questions

- **Where does a `self` meta-mode chat row live?** Today `(AppID, room,
  scopeKey)` keys against the running app — but the `self` mode is
  about kitsoki, not the running app. Two options: (a) key the row
  against a synthetic `app_id="kitsoki-self"` so the conversation
  survives across all apps, or (b) namespace under the running app
  like every other meta mode and accept that `self` chats are scoped
  to whichever app the user was in when they fired `/meta self`.
  Option (a) is the more useful behaviour ("I noticed a bug in kitsoki
  yesterday while playing cloak; I want to pick the conversation back
  up while playing dev-story today") but requires special-casing the
  scope-key derivation in the controller.
- **Tool gating for repo-write agents.** §10 of the shipped doc calls
  out that today's `tools:` list is metadata-only. Phase C is where
  this becomes load-bearing: a `kitsoki-engineer` whose YAML declares
  no `Bash` tool but whose claude subprocess can still shell out is a
  footgun. Two paths: (a) spin up a per-call MCP server registering
  only the allowed tool surface, (b) controller-side rejection of
  any tool-use beyond the allowlist. Recommend (b) for the first
  cut — the dispatcher already runs in-controller for the legacy
  authoring tokens, and rejecting unauthorised tool-use is a small
  extension. Move to (a) when the MCP plumbing is generally available.
- **Default-oracle builtin.** Today `default-oracle` is implicitly
  `story-author` for back-compat. Phase C/D probably ships a separate
  vanilla helpful-assistant `default-oracle` so the off-path runtime
  (when it eventually fires LLM calls) has a sensible default that is
  not the YAML editor. Open: does the back-compat alias survive Phase
  C? Recommend dropping it once apps have had a chance to declare an
  explicit `default-oracle:` override.
- **`bug` mode without a tool surface.** A `bug-reporter` agent that
  emits structured output (issue title + body + repro steps) but does
  not call any host tool feels under-specified — at minimum it needs a
  `bugs.create` (or `tickets.create`) host handler that takes a typed
  payload and POSTs to wherever the user wants bugs filed. That handler
  is unspecced and is technically pre-work for Phase C.

---

## 2. Phase D — TUI session list + bg-agent runtime

Two distinct surfaces grouped under the same phase because they are
the largest remaining ergonomic gaps.

### 2.1 Foyer "meta sessions" panel

`kitsoki chat list --room meta:story` is enough for power users but
hostile for casual reentry. A TUI panel — shown either as a foyer
overlay or as an inbox-style row — should let the user browse every
active meta chat across modes (`story`, `self`, `bug`) without typing
a CLI command. Use `Controller.ListChats` (already shipped) as the
data source; the renderer mirrors the inline `/meta list` output
already in the transcript surface.

Open: cross-app surfacing. If the panel shows only chats for the
currently-loaded app, the `self` mode's "cross-app" pitch is half
implemented. The panel needs to either be app-scoped (and the user
relies on `kitsoki chat list` for cross-app) or scope-aware (a tab
or filter for "this app" vs "all kitsoki sessions").

### 2.2 Background-agent runtime

The meta-mode proposal sketched `background_jobs.<name>.agent:` as a
fourth selection site for the agent name. Phase A did not ship this
slot because **kitsoki has no first-class `background_jobs:` YAML
type** today: background work is declared as `background: true` on a
transition `effects:` entry, with `on_complete:` for the result fan-in
(see [`background-jobs/`](../background-jobs/README.md)). Without a
top-level type to attach `agent:` to, the slot has no home.

When background jobs become a first-class type (separate proposal),
add `agent:` to the type and walk it in `validateAgentReferences` —
exactly the same shape `meta_modes[*].agent` and `off_path.agent`
already follow.

The "runtime" half: once the slot exists, the bg scheduler dispatches
the named agent on schedule and writes its outcome into the
`on_complete:` event the existing bg-jobs runtime already plumbs.
There is no new conversational frame — bg jobs are not chats — so
this is a smaller piece than meta mode proper.

---

## 3. Open questions

Carried over from the original §6 and still open:

1. **`default-oracle` registration after Phase B.** See §1 above.
2. **Room-level agent precedence vs. off-path.** Off-path runtime is
   not yet built (see [`../meta-mode.md`](../meta-mode.md) §10). When
   it is, off-path should plausibly inherit the room's agent so it
   becomes "free-form chat with the room's agent" — but this needs
   confirmation against the bg-jobs proposal which may want a
   different default.
3. **Cross-app `self` chat keying.** See §1.
4. **Tool-gating enforcement strategy.** Per-call MCP server vs.
   controller-side dispatcher rejection. See §1.

Questions resolved by shipped code (removed from the list): trigger as
slash-command vs FSM intent (slash); overlay-only vs FSM state move
(overlay); persist tri-state (nil → true); exit defaults to /onpath;
draft proposals survive exit; one active meta mode at a time.

---

## 4. Phasing

- **Phase C — `self` and `bug` meta modes.** Two new builtin agents
  (`kitsoki-engineer`, `bug-reporter`) + tool-gating enforcement +
  default-oracle separation from story-author + new host handler for
  bug filing. Cross-app chat keying decision.
- **Phase D — TUI session list + bg-agent runtime.** Foyer panel +
  cross-app session surfacing. Bg-jobs agent slot once bg-jobs
  becomes a first-class declarative type (separate proposal).
