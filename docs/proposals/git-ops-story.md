# Story: git-ops — Interactive Git Workflow

**Status:** Draft v3. Session-mined, revised, then corrected against an adversarial
readiness review (engine-feature verification + worktree/merge/conflict redesign).
Nothing implemented yet.
**Kind:**   story
**Epic:**   — standalone

## Why

Operators working in a repo need a guided, deterministic git workflow:
staging, commit (with oracle-authored message), rebase, squash-merge, worktree
lifecycle, and conflict resolution — where each step is a predictable script and
the oracle only appears when a human judgment is unavoidable (commit message,
conflict resolution). Today these steps are raw terminal commands with no guardrails,
no sequencing enforcement, and no operator-facing narration.

The git-ops story provides a TUI hub that reads branch and worktree context, routes
to the right operation set, and shells out to git for every deterministic step while
calling the oracle only for two cases: authoring a commit message and resolving
a rebase/merge conflict.

> **Why worktrees are central:** Session mining showed worktree lifecycle appeared in
> 18 of 379 sessions — the most-repeated workflow class — and all real feature-branch
> work follows: `git worktree add .worktrees/<name> -b <branch> main` → work → rebase
> → squash → FF merge → `git worktree remove` + `git branch -d`. The current git
> mental model of "branch ops vs main ops" is secondary to this lifecycle arc.

## What changes

A new standalone story `stories/git-ops/` with rooms covering the full worktree
lifecycle plus all single-branch ops. On entry it detects the current branch and
lists worktrees; if on the configured integration branch it lands in `main_ops`,
otherwise in `branch_ops`. Each hub offers the legal operations for that context.
All git commands run via `host.run` in argv mode; the oracle appears in `commit`
(message authoring) and `conflict` (conflict guidance) only.

Shape: a hub-and-spoke story. No pipeline, no cycle budget — each operation is one
round-trip (shell out → show result → back to hub), except `conflict` which loops
until the operator resolves all markers and resumes the rebase.

## Impact

- **Net-new:** 17 rooms (incl. terminal `done`), 2 prompts (`commit_message.md`,
  `conflict_resolve.md`), 2 schemas (`commit_verdict.json`, `conflict_verdict.json`),
  an `agents:` block in `app.yaml` (declares the conflict-resolution agent), ~32 flow fixtures.
- **Engine/host changes:** none required for v1 — composes `host.run` (existing),
  `host.oracle.decide` + `validator:` (existing), `host.oracle.task` (existing).
  **Conflict-room safety is achieved with today's primitives, not a new sandbox:** the
  conflict agent is declared with `tools: [Read, Edit]` and **no `Bash`**, so it physically
  cannot run `git commit` / `git push` / `git checkout`; correctness is gated by the
  `acceptance.post_cmd` (`git diff --check`) plus a post-`--continue` `build_check_cmd` run.
  A *path-level* write fence (restrict Edit to conflicted files only) does not exist today and
  is noted as **future hardening** (task-fs-sandbox proposal) — v1 accepts the residual risk
  that the agent could Edit a non-conflicted file, which the build/test gate and `final_diff`
  review are expected to catch.
- **Docs on ship:** `docs/stories/git-ops.md`, this proposal deleted.

## TUI + web parity

This story must work identically in `kitsoki run` (TUI) and `kitsoki web` (browser).
The constraint shapes every view decision:

- **All views use `extends: "base"` typed elements.** No `view: |` strings —
  a single bad template expression silently ships zero bytes in the web renderer.
- **`code:` is web-hostile for raw git output.** Git command output must be rendered
  via `template:` or parsed into a `list:`. See [view element parity reference](../../memory/reference_view_element_tui_web_parity.md).
- **No ANSI in view content.** All `host.run` git calls must include env:
  `NO_COLOR=1 GIT_TERMINAL_PROMPT=0 GIT_PAGER=cat`. Note: user git configs with
  `color.ui=always` can override `--no-color`; the env vars are the reliable guard.
- **`prose:` collapses multi-line content.** Oracle summaries and conflict reasons
  must use `template:` not `prose:` so line breaks survive in both renderers.
- **Typed view must survive an empty world.** Every room's view must render ≥1
  visible line against `world = {}`. Status fields use `|default:"(pending)"`.
- **`git status --short` → structured `list:` via `stdout_json`.** See staging room.

## Natural-language routing: synonym catalog

Session mining showed operators use terse bare imperatives as primary inputs —
"commit", "merge", "doit" — not verbose forms. The router must handle these as
the canonical happy-path forms. Branch names and paths with `/` must be collected
inside the room via `param:`, not as inline slots (the tokenizer mangles slashes).

| Intent | Canonical synonyms |
|---|---|
| `commit` | commit, commit it, commit them, commit this, ok commit, commit your work, commit everything, single commit |
| `merge_into_main` | merge, ok merge, ok merge now, merge it, ship it, merge to main, merge into main, land this branch, doit |
| `squash` | squash, single commit, squash and merge, clean up commits, make a few broad commits, squash my history |
| `rebase` | rebase, rebase on main, rebase against main, rebase onto main, sync with main, pull in main, bring in the latest from main |
| `stage` | stage, stage everything, add all, just add the Go files, stage only my changes, leave the WIP alone |
| `look` | look, what branch are we on, is this committed, what's uncommitted, am I on main, what files are staged, refresh, status |
| `worktree_create` | create a worktree in .worktrees for ..., make it a worktree under .worktrees, create worktree, new worktree |
| `worktree_list` | review the existing worktrees, list worktrees, show worktrees, worktree audit |
| `cleanup` | remove the worktree, clean up the worktrees, get me to a clean git state, remove branch and worktree |
| `defer` | wait, let's wait, not yet, hold off, defer the merge, I want to merge more changes first |
| `undo` | undo last commit, drop the commit, uncommit this, amend, undo, reset |
| `pull` | pull, pull from upstream, git pull, sync upstream |
| `quit` | quit, exit, done, bye |

Disambiguation must show candidates and allow free-text refinement, not just
"Multiple intents matched." Context-carrying follow-ups ("ok merge now" after a
preparation exchange) should use world state to disambiguate.

## Reuse inventory

| Pipeline step | Mechanism | Reference |
|---|---|---|
| Branch detection on entry | `host.run` argv + `bind: current_branch: stdout` + `emit_intent:` routing | `stories/bugfix/rooms/idle.yaml` `on_enter` pattern |
| Structured context gathering | Single JSON-emitting shell script (branch, on_integration, ahead, behind, has_uncommitted, worktree_list) | `stories/bugfix/rooms/idle.yaml` |
| Shell git operations (add, rebase, merge, pull) | `host.run` argv mode; `ok`/`exit_code` guard | `docs/architecture/hosts.md` §host.run |
| Commit message authoring | `host.oracle.decide` + `commit_verdict.json` schema; operator accept/edit | `stories/bugfix/rooms/proposing.yaml` decide+accept pattern |
| Conflict auto-resolution | `host.oracle.task` (Read+Edit+Bash in working_dir); acceptance post-cmd `git diff --check` | `stories/bugfix/` task pattern |
| Hub routing (back to hub from sub-room) | `target: branch_ops` / `target: main_ops` via world var | `stories/dev-story/` room import pattern |
| Operation-available guard | `available()` / `blocked_reason()` in `choices:` | `stories/dev-story/rooms/` readiness banners |
| Idempotent on_enter | `once: true` on branch-detect invoke | `docs/stories/state-machine.md` §on_enter must be idempotent |

## Story graph

```
                    ┌─────────────────────────────────────────┐
idle ──(on_enter    │  detect: branch, on_integration,        │
        host.run)──▶│  ahead/behind, has_uncommitted,         │
                    │  worktree_list; set route=on_main|      │
                    │  on_branch; emit_intent: "{{ route }}"  │
                    └─────────────────────────────────────────┘
                           │                │
                    on_main│                │on_branch
                           ▼                ▼
                       main_ops         branch_ops
                      ┌──────────┐    ┌────────────────┐
                      │ merge_   │    │ rebase         │──▶ conflict ──▶ (loop/abort)
                      │ branch   │    │ merge_into_main│──(3 guards)──▶ main_ops
                      │ pull     │    │   (dirty→stash)│──▶ squash ──▶ main_ops
                      │ stage    │    │ squash         │
                      │ commit   │    │ stage          │
                      │ undo     │    │ commit         │
                      │ look     │    │ undo           │
                      │ worktree_│    │ stash          │
                      │   list   │    │ worktree_list  │
                      │ cleanup  │    │ cleanup        │
                      │ quit     │    │ quit           │
                      └──────────┘    └────────────────┘

   staging ──▶ (classify: staged/modified/untracked/suspicious)
             ──▶ (show curated list; add_all is opt-in, not default) ──▶ back to hub

   commit  ──▶ (git diff --cached --stat) ──▶ (oracle: message) ──▶ (accept/edit/regen) ──▶ git commit ──▶ hub

   squash  ──▶ (git reset --soft integration_branch) ──▶ (oracle: single message for full diff) ──▶ git commit ──▶ main_ops

   rebase  ──▶ (git rebase integration_branch)   # LOCAL ref — no fetch (see note)
              ──(ok)──▶ branch_ops (rebase_done=true, rebase_base_sha=<sha>)
              ──(conflict)──▶ conflict

   conflict──▶ oracle.task (agent: Read+Edit, NO Bash) auto-resolves all markers
              acceptance: git diff --check  →  git rebase --continue --no-edit
              ──(resolved + build_check_cmd ok)──▶ branch_ops | conflict (new round)
              ──(build_check_cmd fails)──▶ escalation (resolution rejected — not accepted)
              ──(unresolvable)──▶ escalation: operator gives intent hint → retry
              ──(abort)   ──▶ git rebase --abort ──▶ branch_ops | main_ops

   merge_into_main   (INVARIANT: only reachable when branch is a descendant of
                      integration — guard 1 forces a re-rebase otherwise, so the
                      --no-ff merge below CANNOT itself conflict)
      ── guard 1: stale-rebase / descendant check
                  (git merge-base --is-ancestor integration HEAD AND
                   merge-base == stored rebase_base_sha) → else re_rebase_needed
      ── guard 2: locate integration worktree; check ITS tree dirty → stash_sandwich
      ── guard 3: MERGE_HEAD / rebase-in-progress in target worktree
      ──(clean)──▶ worktree-aware merge (no checkout — see merge_into_main detail)
                   → post-merge-verify (build_check_cmd) → cleanup_offer → main_ops

   stash_sandwich ──▶ (cwd=target-wt) git stash push -u → execute op → stash pop
                      ──▶ (conflict if pop conflicts)

   worktree_create ──▶ input: description → derive branch name ──▶ git worktree add .worktrees/<name> ──▶ hub
   worktree_list   ──▶ classify each (unique-commits / dirty / safe-to-remove) ──▶ hub
   cleanup         ──▶ git worktree remove + git branch -d ──▶ hub
   undo            ──▶ (choice: --mixed / --soft / --hard) ──▶ git reset HEAD~1 ──▶ hub
   pull            ──▶ git pull --rebase ──▶ main_ops | conflict (conflict_origin="pull")
   done            terminal
```

## World schema

```yaml
world:
  integration_branch:       { type: string, default: "main" }
  current_branch:           { type: string, default: "" }
  on_integration:           { type: bool,   default: false }
  working_dir:              { type: string, default: "." }  # operator-set at launch via --world
  main_worktree_path:       { type: string, default: "" }   # worktree holding integration_branch; merge target
  route:                    { type: string, default: "" }   # "on_main"|"on_branch" — idle's templated emit_intent
  git_status:               { type: object, default: {} }  # { staged, modified, untracked, suspicious }
  commits_ahead:            { type: int,    default: 0 }
  commits_behind:           { type: int,    default: 0 }
  has_uncommitted:          { type: bool,   default: false }
  worktree_list:            { type: object, default: {} }  # [{ path, branch, bare }]
  staged_diff_stat:         { type: string, default: "" }
  commit_message:           { type: string, default: "" }
  last_op_output:           { type: string, default: "" }
  last_op_ok:               { type: bool,   default: true }
  rebase_done:              { type: bool,   default: false }
  rebase_base_sha:          { type: string, default: "" }  # merge-base SHA at last rebase — stale-check key
  conflict_origin:          { type: string, default: "" }  # "rebase" | "pull"
  conflict_files:           { type: string, default: "" }
  conflict_verdict:         { type: object, default: {} }
  conflict_intent_guidance: { type: string, default: "" }
  merge_branch_name:        { type: string, default: "" }
  stash_ref:                { type: string, default: "" }  # set when stash-sandwich is active
  stash_worktree:           { type: string, default: "" }  # worktree the stash-sandwich op runs in
  last_op_outcome:          { type: string, default: "" }  # keys conditional sub-views (see Named states)
  refactor_mode:            { type: bool,   default: false } # operator-set at launch via --world; flows commit prompt
  squash_mode:              { type: bool,   default: false } # set by squash on_enter, cleared after the squash commit
  build_check_cmd:          { type: string, default: "go build ./... && go test ./..." }
  build_check_disabled:     { type: bool,   default: false } # skip post-merge / post-conflict build gate
```

## Per-room detail

### `idle` — detect branch and worktree context, route to hub

- **`on_enter`:** Single JSON-emitting shell script (bash-mode) → `bind: (multiple) via stdout_json`.
  Emits: `{ branch, on_integration, ahead, behind, has_uncommitted, worktree_list }`.
  Runs `git update-index -q --refresh` first to avoid false dirty entries from mtime-only touches.
  Checks for `MERGE_HEAD` and `rebase-apply`/`rebase-merge` in progress.
  `once: true`.
- **Routing:** the JSON script sets a `route` world var to `"on_main"` or `"on_branch"`;
  the `on_enter` effect list then fires a single templated `emit_intent: "{{ route }}"`
  (the engine takes one intent name, optionally templated — there is no pipe-alternation
  form). Intents `on_main` → `target: main_ops`, `on_branch` → `target: branch_ops`.
- **Edge cases with named states:**
  - Already-on-main with no divergence → stays in `main_ops` with info banner.
  - No common ancestor with integration branch → `no_common_ancestor` state before offering merge/rebase.
  - `cwd` not inside managed repo → error state, not silent.
- **View:** minimal — "Detecting branch…" or blank (< 100 ms before routing fires).

> **Design note:** One JSON-emitting script rather than two sequential `host.run` calls —
> one trace entry, all values bound atomically. `worktree_list` gathered here so hubs
> can show it without a separate on_enter call.

### `main_ops` — hub for integration branch

- **`on_enter`:** Same status-gather script as `idle` (no `once:` — refresh on each return).
- **View:** `kv:` Branch / Status summary; `list:` of worktrees (classified) when non-empty;
  `list:` of available operations with `hint:`.

### `branch_ops` — hub for feature branch

- **`on_enter`:** Same status-gather script.
- **View:** `kv:` Branch / Status / Rebase Done / Commits Ahead / Behind;
  `list:` of operations. `merge into main` is greyed when `!world.rebase_done`.
- **Intents:** `rebase` → reset `rebase_done: false`, record current merge-base SHA as
  `rebase_base_sha` → `target: rebase`.

### `staging` — classify changes, then interactive git add

Session mining finding: `git add -A` must be **opt-in**, not the default. Dominant pattern
is selective staging. Before any staging choice, classify the working tree.

- **`on_enter`:** JSON-emitting script classifies changes into buckets:
  `{ staged, modified, untracked, suspicious }` where `suspicious` = binary files,
  credential-pattern files (`.npmrc`, `*.env`, `*credentials*`), and junk patterns
  (`reconstructed_*.yaml`). Runs `git check-ignore` on candidates to detect gitignore gaps.
  Runs `git update-index -q --refresh` first.
- **View:** `list:` Staged; `list:` Modified/untracked; `list:` Suspicious (flagged);
  `list:` Actions:
  - `add file path=…` — "stage a specific path" *(primary intent)*
  - `add all` — "git add -A *(explicit opt-in — stages everything including suspicious)*"
  - `reset` — "git reset HEAD"
  - `done` — return to hub
  - `look` — refresh
- **Suspicious-file guard:** If `suspicious` is non-empty and operator attempts `add all`,
  show a confirmation interstitial listing the suspicious files before proceeding.

### `commit` — oracle-authored commit message

- **`on_enter`:** `git diff --cached --stat --no-color` → `bind: staged_diff_stat: stdout`.
  `once: true`. If empty → emit `nothing_staged` → route back to hub (oracle NOT called).
- **Oracle:** `host.oracle.decide` with `prompts/commit_message.md`.
  Schema `commit_verdict.json`: `{ type, scope, summary, body, message }`.
  A `validator:` block is required to prevent silent fallback to prose extraction.
  Oracle receives: staged diff stat, changed package list, `refactor_mode`, `current_branch`.
- **Intents:** accept → `git commit -m "{{ world.commit_message }}"` → hub;
  `edit message=…` → set + re-enter; `regenerate` → clear + re-enter; `back` → hub.
- **Amend detection:** If most-recent commit is from the same session and diff looks like
  a continuation, surface an `amend` option alongside `accept`.
- **Squash-mode:** When `squash_mode == true`, oracle receives `git diff {{ world.integration_branch }}..HEAD`
  rather than just `--cached`.

### `squash` — squash all branch commits into one

Named intent distinct from plain merge. Trigger synonyms: "squash", "single commit",
"squash and merge".

> `git rebase -i` is blocked by CLAUDE.md agents. Only non-interactive forms are used.
> The `git reset --soft` mechanic is the approved squash path.

- **Descendant precondition (mandatory guard):** `git merge-base --is-ancestor
  {{ world.integration_branch }} HEAD`. **If HEAD is *not* a descendant of the integration
  branch, `git reset --soft {{ integration_branch }}` would stage the entire reverse-diff of
  everything on integration — silent corruption.** When the guard fails → `re_rebase_needed`
  state (rebase first). Squash is therefore never reachable from a diverged branch.
- **`on_enter`:** guard above → `git diff --stat {{ world.integration_branch }}..HEAD --no-color`
  → `bind: staged_diff_stat: stdout`. Set `squash_mode: true`.
- **Flow:** oracle → `git reset --soft {{ world.integration_branch }} && git commit -m '...'`
  → clear `squash_mode` → `merge_into_main` (or `main_ops` if already clean).

### `rebase` — deterministic rebase against integration branch

- **`on_enter`:** `git rebase {{ world.integration_branch }}`. No `once:`.
  On ok: record current `git merge-base HEAD {{ world.integration_branch }}` as `rebase_base_sha`,
  set `rebase_done: true`. On conflict: `target: conflict`.
- **Pre-rebase safety:** Auto-create backup tag `{{ world.current_branch }}-pre-rebase-backup`
  before rebasing more than 1 commit. Show tag name in view.
- **No auto-fetch (documented limitation):** rebase targets the **local** integration ref.
  If `integration_branch` tracks a remote, the rebase base can silently lag the remote, and
  the stale-rebase guard (local merge-base vs stored SHA) will not detect remote advancement.
  Operators must `pull` on main first. README must state this prominently. (Auto-fetch is a
  v2 ergonomics item; push remains a non-goal — fetch was deferred to keep v1 fully local.)

### `conflict` — oracle auto-resolution with operator escalation

The oracle attempts to resolve all conflicts automatically. Operator is only asked when
the oracle cannot resolve with confidence — they provide high-level guidance, not raw edits.

- **`on_enter` (step 1):** `git diff --diff-filter=U --name-only --no-color` → `bind: conflict_files`.
  Special case: if only `go.sum` is conflicted → run `go mod tidy` directly, skip oracle.
- **`on_enter` (step 2):** `host.oracle.task` with `prompts/conflict_resolve.md`, run with a
  **named agent declared `tools: [Read, Edit]` and NO `Bash`** (the `agents:` block in
  `app.yaml`). Denying `Bash` is the v1 write-fence: the agent physically cannot
  `git commit` / `git push` / `git checkout` / stage / continue — it can only edit the working
  tree, and the deterministic story drives every git command. Default strategy: "take main's
  version + re-apply my additive changes on top." For non-ASCII files: write from scratch.
  `acceptance.post_cmd: git diff --check` (rejects leftover conflict markers + whitespace
  errors; retries the agent on failure). `bind: conflict_verdict: submitted`.
  > **Known limitation:** `tools: [Read, Edit]` prevents git mutation but does **not** confine
  > *which files* the agent may Edit (no path-level fence today — see task-fs-sandbox). The
  > post-`--continue` build gate below is the real correctness backstop, not `git diff --check`.
- **Routing:** `resolved == true` → `git rebase --continue --no-edit` (story-driven, not the
  agent) → **then run `build_check_cmd` (unless `build_check_disabled`)**:
  - build ok → set `rebase_done: true` → hub.
  - build fails → **the resolution is rejected** (it was syntactically clean but semantically
    wrong): route to escalation with the build output as guidance. `git diff --check` alone is
    insufficient — it cannot catch a compile-breaking merge.
  - new conflict round (`--continue` re-conflicts) → clear `conflict_files` + `conflict_verdict`
    → `target: conflict`.
  `resolved == false` → escalation view.
- **Escalation intents:** `guide intent=…` → set `conflict_intent_guidance`, clear verdict/files
  → `target: conflict`; `abort` → `git rebase --abort`, clear conflict vars, set `rebase_done: false`
  → hub via `conflict_origin`; `look` → `.`.
- **Note:** `conflict_intent_guidance` must be cleared after successful resolve to prevent
  leaking into future conflict rooms.

### `merge_into_main` — merge feature branch into integration branch

> **Worktree-aware merge (no `git checkout`).** The story's central premise is that work
> happens in a linked worktree under `.worktrees/`. **You cannot `git checkout {{ integration_branch }}`
> from a linked worktree — git refuses, because the integration branch is already checked out
> in another worktree.** So the merge runs *in the integration worktree*, in place, without ever
> changing the current worktree's HEAD:
>
> - **`idle`/hub `on_enter`** records `main_worktree_path` = the worktree whose branch is
>   `integration_branch` (parsed from `git worktree list --porcelain`).
> - **Merge call:** `host.run` with `cmd: "git merge --no-ff '{{ world.current_branch }}'"` and
>   **`cwd: "{{ world.main_worktree_path }}"`** — `host.run`'s first-class `cwd:` arg sets the
>   process dir (confirmed in `docs/architecture/hosts.md` §host.run; templated from world per the
>   `stories/fix-tests` pattern). No `git checkout`, no `git -C`.
> - **Fallback:** if cwd *is* the integration checkout (operator on a feature branch with no
>   worktree), `main_worktree_path == working_dir` and the same call is a plain in-place merge.
> - All other git host calls set `cwd: "{{ world.working_dir }}"`; only the merge and its
>   build-verify/stash target `main_worktree_path`.

Three mandatory pre-merge guards run on entry in sequence:

1. **Stale-rebase / descendant check:** `git merge-base --is-ancestor {{ world.integration_branch }} HEAD`
   **and** `git merge-base HEAD {{ world.integration_branch }}` equals the stored `rebase_base_sha`.
   If HEAD is not a descendant, or the merge-base has moved (integration advanced) → `re_rebase_needed`.
   `rebase_done == true` alone is insufficient. **This guard is also a correctness invariant:**
   because the branch is guaranteed a strict descendant of integration, the `--no-ff` merge below
   is fast-forwardable and therefore **cannot itself produce a conflict** — no cross-worktree
   conflict handling is needed. (`--no-ff` still records a merge commit for history.)
2. **Dirty-tree + MERGE_HEAD check on the *target* worktree:** `git status --porcelain` with
   `cwd: "{{ world.main_worktree_path }}"`. If dirty → `stash_sandwich` (which stashes *in the
   target worktree*). Check `{{ main_worktree_path }}/.git*/MERGE_HEAD` and rebase-in-progress markers →
   `merge_in_progress` error state. (Guard the *target* worktree, not cwd — the merge lands there.)
3. **Merge strategy is `--no-ff`, unconditionally.** It always records a merge commit and, given
   guard 1, always succeeds on a clean target tree — there is no "FF not possible" branch.
   Operators who want linear history choose the explicit `squash` operation *before* merging.

- **`on_enter`:** guards → `git merge --no-ff '{{ world.current_branch }}'` with
  `cwd: "{{ world.main_worktree_path }}"` → on ok: post-merge-verify → cleanup offer → `target: main_ops`.
- **Post-merge verification:** `host.run` of `world.build_check_cmd` (in the target worktree;
  skipped when `build_check_disabled`). On failure → `post_merge_test_fail` state (offers
  `git merge --abort` in the target worktree / rollback).
- **Cleanup offer:** After success, automatically offer `git worktree remove` + `git branch -d`.
- **'Without pushing':** default behavior — no remote push.

### `stash_sandwich` — stash WIP around a merge or rebase

- `git stash push -u -m 'git-ops-wip'` (with `cwd: "{{ world.stash_worktree }}"`) → bind
  `stash_ref` → execute pending op → `git stash pop` (same `cwd`) → if pop conflicts:
  route to `conflict` room (first-class, not linear).
- **Stash *before* the operation, unconditionally when the target tree is dirty** — not gated on
  file-overlap. A dirty target worktree blocks the merge/checkout outright regardless of which
  files overlap, so overlap detection is both unreliable and unnecessary here.
- `stash_worktree` is the worktree the pending op runs in: the target (integration) worktree for
  `merge_into_main`, the current worktree for `rebase`.

### `worktree_create` — create a new worktree in `.worktrees/`

- Form — ask for a short description to derive branch name (slugify).
- Guards: check `git worktree list` for existing registration; check for stale dir.
  `git worktree add '.worktrees/{{ derived_name }}' -b '{{ branch_name }}' {{ world.integration_branch }}`.
  Enforce absolute path under project root `.worktrees/` (not nested under a subdirectory).
- **Error states with named routes:**
  - Branch exists, no worktree → offer to create worktree for existing branch.
  - Already registered → route to `worktree_list`.
  - Stale dir on disk, not git-registered → offer `git worktree repair` or manual removal.

### `worktree_list` — audit existing worktrees

- `git worktree list --porcelain` → classify each as: `has-unique-commits / clean-merged / dirty / stale-not-registered`.
- `list:` with classification and per-worktree actions.
- Stale agent worktrees (`.worktrees/agent-*`, per CLAUDE.md's project-root convention) shown in a separate bucket.
- Intents: `prune`, `remove path=…`, `back`.

### `cleanup` — remove worktree + branch after merge

- Default: remove both worktree and branch. "Delete worktree but keep branch" and
  "delete both" are distinct intents with separate `host.run` paths.
- After removal: `git worktree prune` to clear stale refs.

### `undo` — undo last commit

- Show last commit summary; offer `--mixed` (default), `--soft`, `--hard`.
- `--hard` requires explicit confirmation interstitial.
- Amend sub-case: route to `commit` room with `amend_mode: true` → `git commit --amend`.

### `merge_branch_pick` + `merge_exec` — merge a named branch (on main)

- Form for branch name → `merge_exec` room: same dirty-tree and MERGE_HEAD guards as
  `merge_into_main`. `git merge --no-ff {{ world.merge_branch_name }}`.
- Two separate rooms (name input vs execution) so trace shows user-provided branch name
  as a world transition before the git op fires.

### `pull` — git pull --rebase from upstream

- Check for upstream tracking ref first — if none (new branch), skip pull with info message.
- `git pull --rebase` → on ok: `main_ops`; on conflict: `conflict` with `conflict_origin="pull"`.

### `done` — terminal

```yaml
done:
  view:
    extends: base
    blocks:
      body:
        - prose: "git-ops session complete. Branch: {{ world.current_branch }}."
```

## Named states — where each lives

The graph and per-room detail route to several named outcome states. **None of these are
separate room files.** Following the dev-story convention, an outcome that carries its own
operator choices is a **conditional sub-view inside its host room** (a `when:`-guarded view
block keyed on a world var such as `last_op_outcome`), and a pure-informational outcome is a
**guard banner** rendered in the room it occurs in (greying the now-illegal operations via
`available()` / `blocked_reason()`). This keeps the room tree at 17 files.

| Named state | Host room | Realised as | Operator affordance |
|---|---|---|---|
| `re_rebase_needed` | `merge_into_main`, `squash` | banner | greys merge/squash, highlights `rebase` |
| `no_common_ancestor` | `idle`/hub | banner | suppresses merge/rebase; explains divergence |
| `merge_in_progress` | `merge_into_main` | banner + route | routes into `conflict` (continue) or offers abort |
| `post_merge_test_fail` | `merge_into_main` | sub-view | `retry` build / `rollback` (`merge --abort`) |
| `stale_worktree` | `worktree_create` | sub-view | `repair` / manual-remove / cancel |
| `nothing_staged` | `commit` | banner + route | info, returns to hub (oracle NOT called) |
| `no_tracking` / `pull_no_tracking` | `pull` | banner + route | info, returns to hub |
| `defer` | `merge_into_main` | banner + route | info, returns to hub (`rebase_done` preserved) |
| `already_on_main…` | `main_ops` | banner | "you're already on main" info |
| escalation (conflict) | `conflict` | sub-view | `guide intent=…` / `abort` / `look` |

## Flow fixtures

All fixtures are intent-only, no LLM. `host.run` calls are stubbed via flow `host_handlers`.

**Critical requirement:** At least one fixture per hub intent must fire via a realistic
**free-text utterance**, not slot injection. Slot injection bypasses the semantic routing
tier and gives false confidence — this is the documented failure mode from dogfood sessions.

### Happy-path fixtures

- **`happy_path_commit`** — `idle` (stub: branch=`feat/x`) → `branch_ops` → `stage` (add_all) →
  `commit` (oracle stub: `"feat: add x"`) → accept → `branch_ops`.
  Free-text trigger: `"commit"` (bare imperative).

- **`happy_path_rebase_merge`** — `branch_ops` → `rebase` (stub: exit_code=0) →
  `branch_ops` (`rebase_done=true`) → `merge_into_main` (descendant: yes; merge-base ==
  `rebase_base_sha`; target tree clean; merge stub: exit_code=0) → post-merge-verify
  (build_check_cmd stub: ok) → cleanup offer → `main_ops`.

- **`happy_path_worktree_lifecycle`** — `main_ops` → `worktree_create desc="add login"` →
  worktree created at `.worktrees/add-login` → `branch_ops` → `rebase` (stub: ok) → `squash`
  (oracle stub) → `merge_into_main` (stub: ok) → `cleanup` → `main_ops`.
  Free-text trigger: `"create a worktree in .worktrees for add login"`.
  Asserts: `.worktrees/` path enforced; cleanup offered after merge.

- **`happy_path_squash_merge`** — `branch_ops` → `squash` (oracle stub) → accept → `main_ops`.
  Free-text trigger: `"single commit"` (bare terse form).
  Asserts: `squash_mode == true`, `git reset --soft` called.

### Conflict fixtures

- **`conflict_auto_resolved`** — `rebase` (exit_code=1) → `conflict` (oracle.task: `resolved=true`) →
  `git rebase --continue` (ok) → `branch_ops` (`rebase_done=true`).

- **`conflict_second_round`** — `rebase` (exit_code=1) → `conflict` (resolved=true) →
  `git rebase --continue` (exit_code=1 again, new round) → clears state → re-enters `conflict`.
  Asserts: second-round handled as a loop, not a dead end.

- **`conflict_escalate_then_guide`** — `conflict` (resolved=false, unresolvable_files=["foo.go"]) →
  escalation → `guide intent="keep ours"` → re-enters `conflict` (resolved=true) → `branch_ops`.
  Asserts: `conflict_intent_guidance` cleared after resolve.

- **`conflict_abort`** — `conflict` (resolved=false) → `abort` → `branch_ops` (`rebase_done=false`).

- **`conflict_build_reject`** — `conflict` (oracle.task: `resolved=true`, `git diff --check` ok) →
  `git rebase --continue` (ok) → `build_check_cmd` stub: **exit_code=1** → resolution rejected →
  escalation (build output as guidance). Asserts: a syntactically-clean but build-breaking
  resolution does NOT set `rebase_done=true` — `git diff --check` alone never accepts a merge.

- **`pull_conflict`** — `pull` (exit_code=1, conflict) → `conflict` (`conflict_origin="pull"`) →
  auto-resolved → `main_ops`. Asserts: `conflict_origin` routes back to `main_ops`.

### Staging fixtures

- **`staging_classify_suspicious`** — `staging` (stub: git_status includes `.npmrc`) →
  classified as suspicious → `add all` → confirmation interstitial → operator confirms →
  `git add -A`. Asserts: suspicious-file gate fires.

- **`staging_selective`** — `staging` → `add file` (the path `internal/host/run.go` is collected
  via the room's `param:`, **not** an inline slot — slashes are mangled by the tokenizer per the
  synonym-catalog rule) → back to hub. Asserts: `git add` called with the param path, not `-A`.

### Merge-guard fixtures

- **`stale_rebase_check`** — `merge_into_main` (stub: current merge-base ≠ stored `rebase_base_sha`)
  → `re_rebase_needed` state. Asserts: `rebase_done=true` alone does not allow merge.

- **`dirty_tree_stash_merge`** — `merge_into_main` (dirty tree with overlapping files) →
  `stash_sandwich` → stash pushed → merge (ok) → stash pop → `main_ops`.

- **`merge_head_blocked`** — `merge_into_main` (stub: `.git/MERGE_HEAD` exists) →
  `merge_in_progress` error state shown.

- **`merge_from_worktree`** — `merge_into_main` from a *linked* worktree (stub: cwd is
  `.worktrees/feat-x`, `main_worktree_path` ≠ cwd; branch is a descendant of integration) →
  asserts the merge runs with `cwd: {{ main_worktree_path }}` and **no `git checkout` is ever
  invoked**. Guards against the worktree/checkout incompatibility.

- **`merge_descendant_guard`** — `merge_into_main` (stub: HEAD not a descendant of integration,
  `rebase_done=true`) → `re_rebase_needed`. Asserts: `--no-ff` merge is never attempted on a
  non-descendant branch.

### Natural-language routing fixtures (free-text, no slot injection)

- **`route_commit_bare`** — utterance `"commit"` → routes to `commit` room.
- **`route_merge_doit`** — utterance `"doit"` (with `rebase_done=true`) → routes to `merge_into_main`.
- **`route_squash_single`** — utterance `"single commit"` → routes to `squash`.
- **`route_rebase_sync`** — utterance `"sync with main"` → routes to `rebase`.
- **`route_stage_add_all`** — utterance `"stage everything"` → routes to `staging`, then `add_all`.
- **`route_cleanup`** — utterance `"remove the worktree"` → routes to `cleanup`.
- **`route_defer_wait`** — utterance `"let's wait"` in `merge_into_main` → `defer` state.
- **`route_undo`** — utterance `"undo last commit"` → routes to `undo` room.

### Worktree edge-case fixtures

- **`worktree_stale_dir`** — `worktree_create` (stub: dir exists, not registered) → `stale_worktree` state.
- **`worktree_branch_exists`** — `worktree_create` (branch exists, no worktree) → offer to add worktree.
- **`worktree_already_registered`** — `worktree_create` (already registered) → route to `worktree_list`.

### Edge-state fixtures

- **`commit_nothing_staged`** — `commit` (stub: diff stat is empty) → back to hub, oracle NOT called.
- **`pull_no_tracking`** — `pull` (stub: no upstream tracking ref) → info message, return to hub.
- **`already_on_main_with_branch_intent`** — idle with `on_integration=true`, operator types
  "merge into main" → "you're already on main" info state.

## Net-new files

```
stories/git-ops/
├── app.yaml                        # world schema, hosts, intents+synonyms, agents: (conflict agent)
├── rooms/
│   ├── idle.yaml
│   ├── main_ops.yaml
│   ├── branch_ops.yaml
│   ├── staging.yaml
│   ├── commit.yaml
│   ├── squash.yaml
│   ├── rebase.yaml
│   ├── conflict.yaml
│   ├── merge_into_main.yaml
│   ├── merge_branch.yaml          # includes merge_exec
│   ├── pull.yaml
│   ├── stash_sandwich.yaml
│   ├── worktree_create.yaml
│   ├── worktree_list.yaml
│   ├── cleanup.yaml
│   ├── undo.yaml
│   └── done.yaml                   # terminal
├── prompts/
│   ├── commit_message.md
│   └── conflict_resolve.md
├── schemas/
│   ├── commit_verdict.json        # { type, scope, summary, body, message }
│   └── conflict_verdict.json      # { resolved, confidence, unresolvable_files, resolution_summary, reason }
├── flows/
│   ├── happy_path_commit.yaml
│   ├── happy_path_rebase_merge.yaml
│   ├── happy_path_worktree_lifecycle.yaml
│   ├── happy_path_squash_merge.yaml
│   ├── conflict_auto_resolved.yaml
│   ├── conflict_second_round.yaml
│   ├── conflict_escalate_then_guide.yaml
│   ├── conflict_abort.yaml
│   ├── conflict_build_reject.yaml
│   ├── pull_conflict.yaml
│   ├── staging_classify_suspicious.yaml
│   ├── staging_selective.yaml
│   ├── stale_rebase_check.yaml
│   ├── dirty_tree_stash_merge.yaml
│   ├── merge_head_blocked.yaml
│   ├── merge_from_worktree.yaml
│   ├── merge_descendant_guard.yaml
│   ├── route_commit_bare.yaml
│   ├── route_merge_doit.yaml
│   ├── route_squash_single.yaml
│   ├── route_rebase_sync.yaml
│   ├── route_stage_add_all.yaml
│   ├── route_cleanup.yaml
│   ├── route_defer_wait.yaml
│   ├── route_undo.yaml
│   ├── worktree_stale_dir.yaml
│   ├── worktree_branch_exists.yaml
│   ├── worktree_already_registered.yaml
│   ├── commit_nothing_staged.yaml
│   ├── pull_no_tracking.yaml
│   └── already_on_main_with_branch_intent.yaml
└── README.md
```

## Tasks

```
## 1. Scaffold
- [ ] 1.1 app.yaml: world schema, hosts list, root: idle, include: rooms/*.yaml
- [ ] 1.2 All room files with typed extends: "base" views — placeholder body, full intent/transition skeletons
- [ ] 1.3 schemas/commit_verdict.json and schemas/conflict_verdict.json
- [ ] 1.4 Stub prompts/commit_message.md and prompts/conflict_resolve.md

## 2. Lock the graph
- [ ] 2.1 Probe idle routing: kitsoki turn app.yaml --state idle --intent on_main --world '{}'
- [ ] 2.2 Probe each hub → sub-room → back arc with kitsoki turn
- [ ] 2.3 Stale-rebase + descendant check: verify merge_into_main blocks when HEAD is not a
      descendant of integration OR merge-base moved (not just rebase_done)
- [ ] 2.4 merge_into_main guard order: verify guards fire in sequence (descendant → dirty → MERGE_HEAD)
- [ ] 2.5 Squash descendant guard: verify `git reset --soft` is never reached on a diverged branch
- [ ] 2.6 Worktree-aware merge: verify no `git checkout` is emitted and the merge runs with
      `cwd: {{ main_worktree_path }}` (run `merge_from_worktree` fixture)
- [ ] 2.7 Flow fixtures pass: kitsoki test flows stories/git-ops/app.yaml

## 3. Prompts + oracle integration
- [ ] 3.1 commit_message.md: conventional-commit schema, refactor_mode + squash_mode branches, validator: block
- [ ] 3.2 conflict_resolve.md + agents: block — declare the conflict agent with `tools: [Read, Edit]`
      and NO Bash (the v1 write-fence); conflict-file list, go.sum special case
- [ ] 3.3 Conflict build-gate: verify a clean-but-build-breaking resolution is rejected, not accepted
      (run `conflict_build_reject` fixture)
- [ ] 3.4 Live commit round-trip: kitsoki run, stage a real file, accept oracle message, verify git log

## 4. Natural-language routing
- [ ] 4.1 Confirm all terse synonym forms (bare imperatives) route correctly via kitsoki run
- [ ] 4.2 Run all route_* free-text fixtures — no slot injection
- [ ] 4.3 Disambiguation view shown when input matches multiple intents

## 5. Live + document
- [ ] 5.1 End-to-end worktree lifecycle: create → rebase → squash → merge → cleanup
- [ ] 5.2 End-to-end conflict path: rebase → conflict → auto-resolve → branch_ops
- [ ] 5.3 Conflict agent fence verified live: Bash-denied agent cannot commit/push/checkout;
      build_check_cmd gate rejects a semantically-wrong resolution (document residual: no path-level
      Edit fence — future task-fs-sandbox hardening)
- [ ] 5.4 Live worktree merge: from an actual `.worktrees/<x>` checkout, land onto main without
      `git checkout` failing — the failure the redesign exists to prevent
- [ ] 5.5 README.md: entry, exits (none — hub), world contract, host requirements, working_dir config
- [ ] 5.6 Migrate to docs/stories/git-ops.md; delete this proposal; update proposals/README.md
```

## Open questions

1. **go.sum conflict auto-resolution:** If only `go.sum` is conflicted, run `go mod tidy` directly, skip oracle. Lean: yes.

2. **Stale-agent worktree bucket path:** worktree_list flags stale agent worktrees. Per CLAUDE.md these live under project-root `.worktrees/`, not `.claude/worktrees/` — the bucket should scan `.worktrees/` (corrected from v2).

### Resolved by the v3 adversarial-review pass

- **Write fence for oracle.task (conflict room)** → **resolved:** v1 uses the agent tool-allowlist
  (`tools: [Read, Edit]`, **no Bash**) so the agent cannot run git, plus a post-`--continue`
  `build_check_cmd` gate for semantic correctness. Path-level Edit fencing deferred to task-fs-sandbox
  (documented residual risk). No engine change required for v1.
- **`merge_into_main` checkout step** → **resolved:** **no `git checkout`** — `git checkout` fails
  from a linked worktree because integration is checked out elsewhere. Merge runs in place via
  `git merge --no-ff` with `cwd: "{{ main_worktree_path }}"`. Guard 1's descendant invariant means
  this merge cannot conflict.
- **Auto-fetch before rebase** → **resolved:** no fetch; rebase targets the local integration ref.
  README documents that operators must `pull` main first if it tracks a remote. (v2 ergonomics item.)
- **`build_check_cmd` escape hatch** → **resolved:** `build_check_disabled: bool` world var added;
  skips both the post-merge and post-conflict build gates.
- **Per-call working directory** → **resolved + verified in code:** `host.run` exposes a first-class,
  world-templatable `cwd:` arg (`internal/host/handlers.go:99`, `docs/architecture/hosts.md` §host.run,
  live use in `stories/fix-tests/rooms/pipeline.yaml`). Every git call sets `cwd: "{{ world.working_dir }}"`;
  the merge/stash/build-verify target `cwd: "{{ world.main_worktree_path }}"`. No `git -C` or `cd &&` needed.

## Non-goals (v1)

- **Push to remote / PR creation** — immediate next ask after v1. Document natural phrasing
  that would misroute ("push to origin", "create pr", "open pr") so the story surfaces
  "not yet" rather than silently misrouting. `ahead/behind` upstream tracking shown in hub view.
- **Interactive conflict editor** — oracle guidance tells the operator what to fix; they edit in their own editor.
- **Branch creation / checkout** — `worktree_create` handles the common new-branch-from-main case.
- **Cherry-pick, bisect** — future extension points.
- **`git rebase -i`** — blocked by CLAUDE.md agents. Non-interactive forms only.
- **Autosquash / fixup** — `git commit --fixup=<sha>` + `GIT_SEQUENCE_EDITOR=: git rebase --autosquash`.
  Well-scoped v2 addition; non-interactive mechanics are achievable.
- **Force-push** — must require two-step confirmation; never routed directly. v2 push room.
- **Submodules / worktrees outside `.worktrees/`** — `.worktrees/` convention enforced; arbitrary multi-tree setups are not in scope.
