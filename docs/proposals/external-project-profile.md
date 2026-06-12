# Runtime: Target-project profile for dev-story

**Status:** Draft v1. Nothing implemented yet.
**Kind:**   runtime
**Epic:**   ../external-project-targeting.md

## Why

Retargeting `dev-story` at a foreign repo today means editing the pipeline:
the proposal/PRD publish scripts hardcode kitsoki's templates and a flat
`docs/proposals/` / `docs/prd/` home, and the ticket provider reads
`<cwd>/issues/bugs/` literally — `world.repo_root` is declared but **not
yet wired into `iface.ticket.*` args** (the "Wave 3.5" TODO in
`stories/kitsoki-dev/app.yaml`). For a target with a different doc shape
(gears-rust's `gears-sdlc` templates, `cpt-` IDs, per-gear placement) the
output is wrong, and `make check` rejects it. This slice makes the
**document shape, placement, id-scheme, and repo root** configuration the
profile fills — so #2/#4 can retarget without touching glue.

## What changes

Add a small **doc-profile** surface to `dev-story` (and `prd`, which it will
import per #3), consumed by the publish glue and the author agent:

- `template_dir` — dir of `<kind>.md` author templates (default: kitsoki's
  `docs/proposals/templates/`). The author copies the matching template
  from here instead of an embedded one.
- `publish_durable_path` — already exists on `prd`
  (`stories/prd/scripts/prd_publish.py`); generalize the design pipeline
  the same way, and make it accept a **placement rule**, not just a flat
  dir, so a target can say "under `gears/<gear>/docs/`".
- `doc_placement` — `flat` (`<durable>/<slug>.md`, today's behavior) or
  `per_scope` (`<scope_root>/<scope>/<sub>/<slug>.md`), with the scope
  (`gear`) supplied by `world`. Keeps kitsoki's flat default; unlocks
  gears-rust's per-gear tree.
- `repo_root` passthrough — thread `world.repo_root` into `iface.ticket.*`
  args (closing the Wave 3.5 TODO) so the ticket adapter (#2) and the
  workspace/vcs hosts all operate against the same external tree as
  `world.workdir`.

No new effects, deciders, or host *interfaces* — this is world keys +
parameterized glue (`scripts/*_publish.py`, the proposal-pipeline analogue)
+ the author prompt reading `template_dir`.

## Design

### World keys (dev-story)

```yaml
world:
  repo_root:            { type: string, default: "" }   # passed to iface.ticket.* (was inert)
  template_dir:         { type: string, default: "docs/proposals/templates" }
  publish_durable_path: { type: string, default: "docs/proposals" }
  doc_placement:        { type: string, default: "flat" }   # flat | per_scope
  doc_scope:            { type: string, default: "" }        # e.g. the gear name, for per_scope
```

### Publish glue

Generalize the two publish scripts to one placement resolver shared by the
design pipeline and `prd`:

- `flat` → `<workdir>/<publish_durable_path>/<slug>.md` (unchanged).
- `per_scope` → `<workdir>/<publish_durable_path>/<doc_scope>/<sub>/<slug>.md`
  (e.g. `gears/<gear>/docs/PRD.md`). `<sub>` is fixed per doc kind
  (`PRD.md`, `DESIGN.md`, `ADR/NNNN-<id>.md`) by the profile, not the slug.

Resolver lives next to `stories/prd/scripts/prd_publish.py`; the proposal
pipeline's publish step calls the same resolver so both kinds place docs
identically for a given profile.

### Author template selection

The draft step copies `<template_dir>/<kind>.md` (it already classifies the
kind, `stories/dev-story/rooms/design_draft.yaml`); pointing `template_dir`
at a target's template set is all retargeting the *shape* needs. The
`cpt-`-ID minting that gears-rust wants is part of #4's template + prompt,
not this seam.

## Impact

- **Files:** `stories/dev-story/app.yaml` (+world keys), the proposal
  pipeline publish room + `stories/prd/` publish glue (placement resolver),
  the ticket-arg shaping where `repo_root` is dropped today.
- **Compat:** defaults reproduce today's behavior exactly (flat
  `docs/proposals`, kitsoki templates) — `kitsoki-dev` is unaffected.
- **Docs on ship:** a "doc profile" subsection in the dev-story README +
  the external-targeting guide.

## Tasks

- [ ] Add the five world keys to `dev-story` (and ensure `prd` reads the
      shared ones once #3 imports it).
- [ ] Thread `world.repo_root` into `iface.ticket.*` args; update the
      local-files provider to honor it (closes the Wave 3.5 TODO).
- [ ] Extract a placement resolver from `prd_publish.py`; add `per_scope`;
      point the design pipeline's publish at it.
- [ ] Make the draft step copy from `world.template_dir`.
- [ ] Flow fixtures: a `per_scope` publish flow + a `repo_root`-passthrough
      ticket flow (mock provider), asserting the resolved paths.
- [ ] Migrate the doc-profile contract into `docs/stories/` and trim this
      proposal.

## Open questions

1. **Is `doc_placement` an enum or a small template string** (e.g.
   `"gears/{scope}/docs"`)? *Lean: enum first (`flat`/`per_scope`) — least
   surprise, covers kitsoki + gears-rust; promote to a path template only if
   a third target needs it.*

## Non-goals

- The gh ticket adapter (#2) and the gears-rust template content / `cpt-` ID
  minting (#4) — this slice only opens the seams they fill.
