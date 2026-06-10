# Story: ui-fix — Review a UI Audit into Patterns, Fix Each Group, Prove It On-Screen

**Status:** Draft v1. Nothing implemented yet.
**Kind:**   story
**Epic:**   — standalone (depends on the shipped `visual-outputs.md` media substrate)

## Why

We just ran the `kitsoki-ui-review` skill and it dropped a gated
`verdict.json` + `review-report.md` under `.artifacts/ui-review/` with
**371 findings** (19 error · 24 warn · 328 info) across the web UI. The
report is a flat wall of rows — and the wrong instinct is to march down it
fixing 371 things. The 326 geometry findings are really ~3 root causes (a
recurring set of undersized trace controls, two sub-11px font floors, the
home rescan link); the single highest-leverage problem is the tour popover
that occludes the whole page at mobile width. **The work is not 371 fixes;
it's a dozen-ish patterns.**

So the operator wants two things this story must deliver:

1. **A review phase that identifies patterns** before any code is touched —
   read the audit, recognize that "tap-target < 24px on trace chips across
   every `iv-*` surface × 3 viewports" is *one* problem, rank the patterns
   by leverage, and let the operator confirm scope. Never blindly iterate
   hundreds of rows.
2. **A fix loop where each agent instance targets exactly one group** (a
   group is a single item *or* a pattern), with a human checkpoint on the
   diff, a deterministic no-LLM proof the finding cleared, and — using the
   newly-shipped **media artifacts** — a **before/after slideshow or video
   per group** so the improvement is visible, recorded, and reviewable, not
   just asserted.

## What changes

A new standalone story `stories/ui-fix/` — a **review → per-group fix
pipeline** with a checkpoint loop, structurally a cousin of
`stories/bugfix/` but iterating over *groups* instead of one bug:

- **`idle`** reads `verdict.json` and a `host.starlark.run` script does the
  *mechanical* collapse: drop below the severity floor, dedupe the
  byte-identical geometry/a11y rows by fingerprint. Pure function → a set
  of candidate findings.
- **`review`** is the new interpretive heart: an `host.oracle.decide`
  reads the deduped findings + the report and **identifies patterns** —
  emitting a ranked list of **groups**, each `{ title, pattern, member
  finding ids, surfaces, viewports, severity, before_frames, root_cause,
  recommendation }`. The operator confirms/edits/reorders/drops groups
  before anything is touched. This is the "don't blindly fix" gate.
- **`fixing`** loops the confirmed groups by cursor. For the current group
  **one** `host.oracle.task` instance is spawned, scoped to that group and
  to `tools/runstatus/src/`; it reads the pattern, its member findings,
  their frames, and the implicated components, and applies the minimal fix.
  Checkpoint: `accept` / `refine(feedback)` / `skip` / `quit`.
- **`verifying`** re-runs **only the deterministic geometry + axe audit**
  (no vision, no LLM, no cost) for the group's surfaces, captures fresh
  "after" frames, and binds `cleared: bool`. Regressed → back to `fixing`.
- **`showcase`** (new) produces a **before/after media artifact** for the
  cleared group — `host.contact_sheet` for a side-by-side slideshow, or
  `host.slidey.render` for a narrated MP4 — emits it via the
  `host.artifacts_dir` media branch to get a stable handle, and renders it
  inline with the `media` view element. Advance cursor.
- **`done`** is a **gallery**: every fixed group with its before/after
  media, plus skipped / still-failing buckets, and a pointer to a full
  re-review.

## Impact

- **Net-new:** `stories/ui-fix/` — 6 rooms, 2–3 prompts, 3 schemas, 2
  Starlark scripts (+ sidecars), 1 before/after slidey spec template, ~8
  flow fixtures, README.
- **Engine/host changes:** none — composes `host.starlark.run` (dedupe +
  audit delta), `host.oracle.decide` (pattern review), `host.oracle.task`
  (per-group fix, scoped writes), `host.run` (re-run the audit script),
  and the shipped `visual-outputs` media seam: `host.contact_sheet` /
  `host.slidey.render` (`internal/host/visual_producers.go`) →
  `host.artifacts_dir` media-emit (`internal/host/artifacts_dir_transport.go:238`)
  → the `media` view element (`internal/app/view_element.go:477`).
- **Dependency:** the `visual-outputs.md` epic — **shipped (3/3)**. This
  story is its first real *consumer* in a pipeline. If that substrate
  regressed, this story's `showcase`/`done` media degrade to a path
  pointer (the TUI behaviour anyway).
- **Reuses existing tooling, not new tooling:** the verify step shells out
  to the already-shipped `docs/skills/kitsoki-ui-review/scripts/` capture +
  audit scripts; the story reimplements no measurement and no renderer.
- **Docs on ship:** `docs/stories/ui-fix.md`, entry in `proposals/README.md`,
  a cross-link from the `kitsoki-ui-review` skill ("found problems? drive
  `stories/ui-fix` to review them into patterns and fix each group").

## Moat alignment

The interpretive/deterministic split is the spine of the design, and every
step is a recorded datapoint:

- **Mechanical dedup** (`idle`) is **deterministic** — a pure Starlark
  fingerprint+collapse over `verdict.json`. Recorded as a host call.
- **Pattern identification** (`review`) is the one **interpretive scoping**
  decision — `host.oracle.decide`, pluggable operator, the groups it
  proposes and the operator's edits both land in the trace as a gate
  decision. This is where judgment lives, and it's isolated to one room.
- **Fix authoring** (`fixing`) is the one **interpretive execution** step —
  `host.oracle.task`, one instance per group, scoped tool grant; the diff
  and the agent's reasoning are recorded.
- **Verification** (`verifying`) is **deterministic** (`host.run` audit +
  Starlark delta). The accept→advance gate decides on `cleared`, a
  measurement — the fixer never grades its own homework.
- **Media production** (`showcase`) is **deterministic** (no LLM in the
  render loop — `visual-outputs.md` shared decision 4) and the resulting
  artifact is a first-class recorded datapoint, displayed from the record,
  not re-derived (memory: *narration-belongs-in-trace*).

## Reuse inventory

| Pipeline step | Mechanism | Reference |
|---|---|---|
| Load + mechanical dedup of findings | `host.starlark.run` over `verdict.json` (fingerprint, severity floor) → `bind:` | `starlark` skill; `host.starlark.run` in `stories/weather-report/` |
| Identify patterns → ranked groups | `host.oracle.decide` + `group_set.json` acceptance schema | `stories/bugfix/rooms/proposing.yaml` decide pattern; `stories/prd/` discovery |
| Operator confirms scope (reorder/drop/floor) | `choice: mode: form` + `available()` guards | `stories/oregon-trail/rooms/general_store.yaml`; `stories/dev-story/` banners |
| Per-group cursor; one agent per group | world `cursor` int over `groups`; `host.oracle.task` on `groups[cursor]` | `stories/dev-story/` one-at-a-time dispatch (see `work-decomposition.md` board) |
| Apply a group's fix (scoped code edit) | `host.oracle.task` (Read+Edit+Bash scoped to `src_root`) + acceptance schema + typecheck post-cmd | `stories/bugfix/` task pattern; `git-ops-story.md` `conflict` room |
| Checkpoint: accept / refine / skip / quit | checkpoint intents + `group_cycle`/`group_budget` gate | `stories/bugfix/rooms/proposing.yaml` accept/refine/restart_from/quit |
| Verify cleared (no LLM) | `host.run scripts/capture.sh` (scoped surfaces) + `host.starlark.run` delta over fresh `audit.json` | `git-ops-story.md` `host.run`; `kitsoki-ui-review` scripts |
| Before/after slideshow or video | `host.contact_sheet` / `host.slidey.render` → `host.artifacts_dir` media-emit → handle | `internal/host/visual_producers.go`; `internal/host/artifacts_dir_transport.go:238`; `docs/architecture/hosts.md` |
| Render the media inline | `media` view element bound to the artifact handle | `internal/app/view_element.go:477`; `docs/stories/story-style.md` §media |
| Idempotent load/review on reload | `once: true` on the dedup + decide invokes | `docs/stories/state-machine.md` §on_enter idempotent; `idempotent-on-enter.md` |

## Story graph

```
idle ──(on_enter: starlark dedupe verdict.json ──▶ findings)──▶ review
                                                                  │
   review: oracle.decide identifies PATTERNS ──▶ groups[]         │
           operator confirm / reorder / drop / floor              │
                                                                  ▼ start
   ┌──────────────────────────────────  fixing  ◀─────────────────┐
   │  on_enter: ONE oracle.task instance applies groups[cursor]    │
   │  (scoped to src_root + this group's findings only)            │
   │  checkpoint:                                                  │
   │    accept ──▶ verifying                                       │
   │    refine(feedback) ──▶ fixing (group_cycle++) ─(budget)─▶ skip
   │    skip ──▶ record skipped ──▶ advance cursor                 │
   │    quit ──▶ @exit:abandoned                                   │
   └──────────────────────────────────────────────────────────────┘
                                   │ accept
                                   ▼
                              verifying
              on_enter: host.run audit (geometry+axe only) on group surfaces
                        + capture "after" frames + starlark delta
                ├─ regressed ─▶ fixing (show audit delta)
                └─ cleared ───▶ showcase
                                   │
                                   ▼
                              showcase
              on_enter: contact_sheet|slidey.render(before+after frames)
                        ──▶ artifacts_dir media-emit ──▶ handle
              view: media element (before/after) ; confirm ──▶ advance cursor
                                   │
   cursor past end ──▶ done   (gallery: each fixed group's before/after media
                               · N fixed · M skipped · K still-failing)
```

`exits:` — `done: { requires: [groups] }` (cursor exceeds the group list),
`abandoned: {}` (quit mid-loop). Progress (`fixed`, `skipped`, their media
handles) is preserved so a partial-run `done` is honest.

## World schema (sketch)

```yaml
world:
  verdict_path:   { type: string, default: ".artifacts/ui-review/verdict.json" }
  frames_dir:     { type: string, default: ".artifacts/ui-review/frames" }  # "before" frames
  src_root:       { type: string, default: "tools/runstatus/src" }
  severity_floor: { type: string, default: "warn" }       # error | warn | info
  media_format:   { type: string, default: "slideshow" }  # slideshow | video
  findings:       { type: object, default: {} }            # deduped, pre-pattern
  # groups: ranked patterns from review. each:
  #   { id, title, pattern, root_cause, severity, member_ids:[...],
  #     surfaces:[...], viewports:[...], before_frames:[...], recommendation }
  groups:         { type: object, default: {} }            # {items: [...]}
  cursor:         { type: int,    default: 0 }
  current:        { type: object, default: {} }             # groups.items[cursor]
  fix_result:     { type: object, default: {} }             # oracle.task verdict
  verify_result:  { type: object, default: {} }             # {cleared, remaining, regressions, after_frames}
  media_handle:   { type: string, default: "" }             # this group's before/after artifact
  fixed:          { type: object, default: {} }             # {items:[{group, media_handle}]}
  skipped:        { type: object, default: {} }
  still_failing:  { type: object, default: {} }
  refine_feedback:{ type: string, default: "" }
  group_cycle:    { type: int,    default: 0 }
  group_budget:   { type: int,    default: 5 }
  abandon_reason: { type: string, default: "" }
```

## Per-room detail

### `idle` — load + mechanically dedupe the verdict

- **`on_enter`:** `host.starlark.run scripts/dedupe_findings.star` (inputs:
  `verdict_path`, `severity_floor`) → `bind: findings: result`. Drops
  findings below the floor and collapses byte-identical geometry/a11y rows
  by fingerprint `(source, check, selector)`, carrying the union of
  surfaces/viewports + a `count`. **No pattern judgment here** — that's
  `review`. `once: true`.
- **Intents:** synthetic `loaded` → `target: review`; empty → `say:` "No
  findings at or above {{ world.severity_floor }}." → `target: done`.
- **View:** "Loaded {{ world.findings.items|length }} deduped findings…".

### `review` — identify patterns into ranked groups (interpretive gate)

- **`on_enter`:** `host.oracle.decide` with `prompts/identify_patterns.md`
  (inputs: the deduped `findings`, the `review-report.md` text). The oracle
  **clusters by root cause** — merging findings that one code change fixes
  (e.g. the trace `<select>`'s `select-name` + `scrollable-region-focusable`
  → one "trace filter control a11y" group) and keeping genuinely distinct
  problems separate — and **ranks by leverage** (mobile tour-popover overlap
  first; it blocks the most content). Emits `group_set.json`. `bind: groups:
  submitted`. `once: true` (cleared on a re-review intent).
- **View:** a `list:` of proposed groups via `template:` — each
  `{rank}. {severity} · {title} — {pattern} ({member_ids|length} findings,
  {surfaces}×{viewports})`; a `kv:` header (N groups from M findings); then
  the action `list:`.
- **Intents:** `start` → `target: fixing` (cursor=0); `reorder ids=…` →
  reorder `groups`; `drop id=…` → remove a group; `merge a=… b=…` → fold two
  groups; `floor level=…` → re-dedupe + re-review; `re_review feedback=…`
  → clear `groups`, re-run the decide with the note as guidance (memory:
  *refine-honours-operator-guidance*); `quit` → `@exit:abandoned`.

> **Why an oracle here and not pure Starlark?** Recognizing that two
> different `check`s share one root cause — or that a tap-target finding and
> a touch-ergonomics finding on the same control are *one* fix — is
> interpretive. Deterministic dedup (idle) handles the identical rows;
> *pattern* identification is judgment, so it's an isolated, recorded
> `decide`. This resolves the prior draft's open question toward "yes,
> oracle pattern review," because the user requires it.

### `fixing` — one agent instance per group (checkpoint room)

- **`on_enter`:** `set: current: groups.items[cursor]`, then **one**
  `host.oracle.task` with `prompts/apply_fix.md`, scoped to the current
  group only. The agent gets Read + Edit + Bash **scoped to `src_root`**
  (`tools/runstatus/src/`) and the group's `pattern` + member findings +
  their `before_frames` + `recommendation`. Its brief: locate the
  implicated component(s)/style and make the *minimal* fix for **this group
  alone**. Emits `fix_verdict.json` `{ applied, files:[...], summary,
  diff_excerpt, notes }`. Acceptance post-cmd: `vue-tsc --noEmit` in
  `tools/runstatus` so a build-breaking fix is rejected before the operator
  sees it. `bind: fix_result: submitted`. `once: true` (cleared on
  `refine`).
- **Hard constraint in the prompt:** edit only files under `src_root`; do
  **not** touch tests, build config, or anything outside the SPA source, and
  do not attempt to fix findings belonging to *other* groups (memory:
  *task-agents-must-not-implement* / write-jail — the prompt is the v1
  guardrail; the engine allowlist is the `oracle-capability-model` epic).
- **View:** `kv:` Group N of M · severity · title; `template:` of `pattern`
  + `recommendation`; once the task returns, `template:` of
  `fix_result.summary` + `fix_result.diff_excerpt` + a `list:` of changed
  files; then the checkpoint `list:`.
- **Intents:**
  - `accept` → `target: verifying`.
  - `refine feedback=…` (required slot) → `set: refine_feedback`, clear
    `fix_result`, `group_cycle++`, guard `when: group_cycle < group_budget`
    → `target: fixing`; at budget → `say:` "Refine budget exhausted; skipping
    group." → treat as `skip`.
  - `skip` → append `current` to `skipped`, reset `group_cycle`, `cursor++`,
    clear per-group vars → `target: "{{ cursor < count ? 'fixing' : 'done' }}"`.
  - `quit` → `set: abandon_reason` → `@exit:abandoned`.

### `verifying` — prove the group cleared + capture "after" frames (no LLM)

- **`on_enter` (step 1 — re-capture):** `host.run` of
  `docs/skills/kitsoki-ui-review/scripts/capture.sh` scoped to
  `current.surfaces` → fresh frames + `audit.json`. Geometry + axe only — no
  vision, no `claude`, free. Bind the new frame paths into
  `verify_result.after_frames`.
- **`on_enter` (step 2 — delta):** `host.starlark.run scripts/verify_delta.star`
  (inputs: fresh `audit.json`, `current.member_ids`/fingerprints) → `bind:
  verify_result: result` = `{ cleared, remaining, regressions, after_frames }`.
  `cleared` = none of the group's fingerprints appear in the fresh audit.
- **Routing (emit_intent):**
  - `cleared == false` → `group_cycle++` → `target: fixing` with
    `verify_result.regressions` shown; at budget → skip-equivalent → advance.
  - `cleared == true` → `target: showcase`.
- **View:** "Re-auditing {{ current.surfaces|join:', ' }}…" then the
  cleared/remaining result via `kv:`.

> The accept→advance gate decides on `verify_result.cleared` — a
> deterministic measurement — never on `fix_result.applied`.

### `showcase` — before/after media for the cleared group (deterministic)

- **`on_enter`:** assemble the group's **before** frames (from `frames_dir`,
  via `current.before_frames`) and **after** frames (from
  `verify_result.after_frames`) into one media artifact:
  - `media_format == "slideshow"` → `host.contact_sheet` over a temp dir of
    {before, after} pairs (deterministic ffmpeg montage —
    `internal/host/visual_producers.go` `ContactSheetHandler`).
  - `media_format == "video"` → render a tiny before/after slidey spec from
    `spec/before_after.json` (a `host.starlark.run` fills in the labels —
    "Before: tap target 19×16px" / "After: 44×44px") → `host.slidey.render`
    `format: mp4` (`SlideyRenderHandler`).
  - Then `host.artifacts_dir` with `src_path` + `kind`
    (`slideshow`/`video`/`image`) → copies under `.artifacts/`, records an
    `artifact.emitted` datapoint, returns a stable handle
    (`internal/host/artifacts_dir_transport.go:238`). `bind: media_handle:
    result.handle`. `once: true`.
- **View:** `kv:` Group · "Fixed ✓"; a **`media` element** bound to
  `world.media_handle` (renders inline `<video>`/`<img>` in `kitsoki web`; a
  labeled `.artifacts/` pointer in the TUI); a `list:` `next` / `look`.
- **Intents:** `next` → append `{ group: current, media_handle }` to `fixed`,
  reset `group_cycle`, `cursor++`, clear per-group vars →
  `target: "{{ cursor < count ? 'fixing' : 'done' }}"`; `look` → `.`.

> Production is deterministic and LLM-free — the labels come from the
> finding deltas, not a model. The before/after artifact is the recorded
> proof of the fix, displayed from the artifact record.

### `done` — before/after gallery + summary

- **View:** `kv:` totals (Fixed / Skipped / Still-failing of the original
  group count); then **a `media` element per fixed group** (a gallery of
  before/after artifacts from `world.fixed.items[*].media_handle`); `list:`
  of skipped / still-failing via `template:`; closing `prose:` pointing at
  the full re-review (`run the kitsoki-ui-review skill again to regenerate
  verdict.json and re-gate — including the vision pass this story
  deliberately skips`).
- No re-entry. Reached via cursor past end, or `idle`→empty short-circuit.

### Net-new files

```
stories/ui-fix/
├── app.yaml
├── rooms/
│   ├── idle.yaml
│   ├── review.yaml
│   ├── fixing.yaml
│   ├── verifying.yaml
│   ├── showcase.yaml
│   └── done.yaml
├── prompts/
│   ├── identify_patterns.md     # findings + report → ranked root-cause groups
│   └── apply_fix.md             # one group → minimal scoped fix
├── schemas/
│   ├── group_set.json           # { items: [{id,title,pattern,root_cause,severity,member_ids,surfaces,viewports,before_frames,recommendation}] }
│   ├── fix_verdict.json         # { applied, files, summary, diff_excerpt, notes }
│   └── verify_result.json       # { cleared, remaining, regressions, after_frames }
├── scripts/
│   ├── dedupe_findings.star     # verdict.json → deduped findings (+ .star.yaml)
│   ├── verify_delta.star        # fresh audit.json + fingerprints → cleared?
│   └── before_after_spec.star   # group + frames → slidey before/after spec (video mode)
├── spec/
│   └── before_after.json        # slidey spec template (video mode)
├── flows/
│   ├── happy_path.yaml
│   ├── review_merge_then_start.yaml
│   ├── refine_once_then_accept.yaml
│   ├── refine_budget_exhaust_skip.yaml
│   ├── verify_regression_then_fix.yaml
│   ├── showcase_slideshow.yaml
│   ├── empty_findings.yaml
│   └── quit_mid_loop.yaml
└── README.md
```

## Flow fixtures

All Mode-2, intent-only, no LLM. `host.starlark.run` runs for real against a
checked-in tiny `verdict.json`/`audit.json` fixture; `host.oracle.decide`
(pattern review) and `host.oracle.task` (fix) are stubbed via flow
`host_handlers`; `host.run` (capture), `host.contact_sheet`/`host.slidey.render`,
and `host.artifacts_dir` are stubbed with canned results (the media stub
returns a fixed handle id — no ffmpeg/slidey in CI).

- **`happy_path`** — `idle` (fixture: 5 findings) → `review` (decide stub: 2
  groups) → `start` → `fixing` (task stub: applied) → `accept` → `verifying`
  (audit stub: cleared) → `showcase` (media stub: handle) → `next` → group 2
  → … → `done`. Asserts `fixed|length == 2`, each carries a `media_handle`.
- **`review_merge_then_start`** — `review` → `merge a=g1 b=g2` (groups
  collapse to 1) → `start`. Asserts group count dropped.
- **`refine_once_then_accept`** — `fixing` → `refine feedback="use the
  shared token, not a magic px"` (group_cycle→1) → `fixing` → `accept`.
- **`refine_budget_exhaust_skip`** — `refine` × `group_budget` → auto-skip.
- **`verify_regression_then_fix`** — `accept` → `verifying` (cleared=false)
  → `fixing` → `accept` → `verifying` (cleared) → `showcase`.
- **`showcase_slideshow`** — reach `showcase`; assert `host.contact_sheet`
  then `host.artifacts_dir` media-emit were called and `media_handle` bound.
- **`empty_findings`** — verdict below floor → short-circuit to `done`.
- **`quit_mid_loop`** — `fixing` → `quit` → `@exit:abandoned`; partial
  `fixed` (with media handles) preserved.

## Tasks

```
## 1. Scaffold
- [ ] 1.1 app.yaml: world schema, hosts (host.starlark.run, host.oracle.decide, host.oracle.task, host.run, host.contact_sheet, host.slidey.render, host.artifacts_dir), root: idle
- [ ] 1.2 All room files, typed `extends: "base"` views, full intent/transition skeletons
- [ ] 1.3 schemas/{group_set,fix_verdict,verify_result}.json; stub prompts
- [ ] 1.4 scripts/dedupe_findings.star + verify_delta.star (+ sidecars); tiny verdict.json + audit.json fixtures

## 2. Lock the graph
- [ ] 2.1 Probe dedupe: `kitsoki test flows` on dedupe_findings.star — assert collapse count
- [ ] 2.2 Probe review: decide stub → groups; merge/drop/reorder intents mutate `groups`
- [ ] 2.3 Probe the loop: accept advances cursor; skip records skipped; refine budget → auto-skip
- [ ] 2.4 verifying regression path routes back to fixing; cleared routes to showcase
- [ ] 2.5 showcase: contact_sheet + artifacts_dir stubs bind media_handle; media element renders in done gallery
- [ ] 2.6 All flow fixtures pass: `kitsoki test flows stories/ui-fix/app.yaml`

## 3. Prompts + real wiring
- [ ] 3.1 identify_patterns.md: cluster-by-root-cause + rank-by-leverage instruction; group_set schema
- [ ] 3.2 apply_fix.md: minimal-fix, src_root + this-group-only write-jail, frames + recommendation inputs, vue-tsc acceptance
- [ ] 3.3 verifying wired to the real capture.sh (per-surface) + verify_delta matches real audit.json shape
- [ ] 3.4 showcase wired to real host.contact_sheet (slideshow) and host.slidey.render (video) + before_after_spec.star; confirm a real before/after artifact emits + renders in `kitsoki web`
- [ ] 3.5 Live single-group round-trip: real verdict.json, fix the select-name a11y group, accept, watch re-audit clear it, see the before/after slideshow inline

## 4. Live + document
- [ ] 4.1 End-to-end on the real verdict: review into groups, fix several, confirm a kitsoki-ui-review re-run shows the geometry/a11y gate improved; done gallery shows before/after per group
- [ ] 4.2 README.md: entry, exits, world contract (verdict_path, frames_dir, src_root, media_format), host requirements, "vision verify is out of scope" note
- [ ] 4.3 Migrate to docs/stories/ui-fix.md; cross-link from kitsoki-ui-review skill; delete this proposal; update proposals/README.md
```

## Open questions

1. **Auto-apply vs. propose-only.** This draft has `host.oracle.task`
   *apply* each group's fix (scoped writes) with a human diff checkpoint —
   mirroring `bugfix`. *Lean: auto-apply with checkpoint — matches "fix item
   by item," and the vue-tsc acceptance + deterministic verify keep it
   honest; nothing lands unreviewed.* (Flip to propose-only and `fixing`
   loses its write grant and `verifying`/`showcase` become operator-driven.)
2. **Media default: slideshow or video?** `slideshow` (`host.contact_sheet`)
   is fast, dependency-light (ffmpeg only, already required by the capture
   tooling), and reads fine as a static side-by-side. `video`
   (`host.slidey.render`) is richer (labeled callouts) but needs slidey on
   PATH/`SLIDEY_HOME` (`visual-outputs.md` cross-cutting OQ 1). *Lean:
   `slideshow` default, `video` opt-in via `media_format` — keeps the happy
   path dependency-light, video for a polished review artifact.*
3. **Group granularity is the oracle's call — how much can the operator
   override?** The `review` room offers reorder/drop/merge but not "split a
   group." *Lean: ship reorder/drop/merge in v1; add `split` only if dogfood
   shows the oracle over-merges. Keeping the override set small keeps the
   gate legible.*
4. **Verify scope: per-surface vs. full re-capture.** Per-group re-capture
   is fast but could miss a regression on an unrelated surface. *Lean:
   per-surface for the per-group gate + a single full geometry/axe re-audit
   folded into `done` as a backstop; vision is always a manual re-run.*

## Non-goals

- **Re-running the vision pass during verify.** Vision is LLM-backed and
  costs money; per-group verify is geometry + axe only. A full re-review
  (vision included) is a deliberate manual `kitsoki-ui-review` invocation
  after the session — called out in `done`.
- **Authoring the audit or the media tools.** Producing `verdict.json` is
  the `kitsoki-ui-review` skill's job; rendering is the shipped
  `visual-outputs` host calls' job. This story *reviews, fixes, and
  showcases* — it builds neither the auditor nor the renderer.
- **Fixing anything outside `tools/runstatus/src/`.** TUI rendering issues
  are a different surface and a different review.
- **Cross-group fixes in one agent.** Each `host.oracle.task` instance
  targets exactly one group; a fix that the agent notices would help another
  group is left for that group's turn (recorded in `fix_result.notes`).
- **Engine-level write sandboxing** for the fix task — the prompt-level
  src_root + this-group-only constraint is the v1 guardrail; the durable
  allowlist is the `oracle-capability-model.md` epic.
```
