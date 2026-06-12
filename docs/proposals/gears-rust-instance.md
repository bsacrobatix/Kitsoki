# Story: gears-rust instance — the worked external target

**Status:** Draft v1. Nothing implemented yet.
**Kind:**   story
**Epic:**   ../external-project-targeting.md

## Why

The profile (#1), gh ticket adapter (#2), and prd→proposal chain (#3) are
each generic. This slice proves they compose by filling the profile for a
**real foreign repo** — `constructorfabric/gears-rust` — and is the example
every future target copies. gears-rust is a demanding first target: it
mandates a spec chain (**PRD → DESIGN → ADR → FEATURE**, templates at
`docs/spec-templates/gears-sdlc/<TYPE>/template.md`), `cpt-{system}-{kind}-
{slug}` IDs with cross-doc traceability, **per-gear** placement
(`gears/<gear>/docs/{PRD,DESIGN}.md`, `ADR/NNNN-*.md`), GitHub-issue tickets,
**DCO** (`git commit -s`) + Conventional Commits `<type>(<gear>): …`, and a
`make check` gate (CONTRIBUTING.md). If the seams hold for gears-rust, a
laxer target is trivial.

## What changes

Add `stories/gears-rust/` — an instance app that imports `dev-story` as
`core` (the `kitsoki-dev` pattern, `stories/kitsoki-dev/app.yaml`) and fills
the profile:

- **Tickets** → bind `ticket` to the gh adapter (#2); `repo_root` =
  gears-rust checkout (passthrough from #1).
- **vcs / workspace / ci** → `vcs: host.git` with **DCO + conventional-commit**
  message shaping; `workspace: host.git_worktree`; `ci: host.local` running
  **`make check`** (and `make test`) as the build/test verb.
- **Docs** → `template_dir` pointed at gears-rust's `gears-sdlc` templates
  (vendored into `stories/gears-rust/templates/` or referenced in the
  checkout); `doc_placement: per_scope` with `doc_scope` = the target gear;
  `publish_durable_path` = `gears` so PRD/DESIGN/ADR land under
  `gears/<gear>/docs/`.
- **cpt-IDs** → the author prompts (PRD + proposal/DESIGN) mint and thread
  `cpt-{gear}-{kind}-{slug}` IDs per the gears-sdlc template, so output
  passes their traceability checks.

Launch:
```
kitsoki run stories/gears-rust/app.yaml --warp scenarios/gears-rust.yaml
# warp sets: workdir/repo_root → the gears-rust checkout, doc_scope → gear,
#            judge_mode, gh repo slug.
```

## Design

- **Instance app** — copy `kitsoki-dev/app.yaml`, swap `host_bindings`
  (`ticket: host.gh.ticket`), re-declare hosts (now including prd's, from
  #3, and the gh adapter's), set the world profile keys (#1).
- **Commit discipline** — DCO `-s` and `<type>(<gear>):` are message-shaping
  on the `vcs.commit` effect (a prompt/template the commit step fills), not
  engine changes. gears-rust's branch names (`feature/`, `fix/`) map to
  `world.feature_branch` formatting.
- **`make check` as ci** — `ci: host.local` with the build/test verbs bound
  to `make check` / `make test`; `on_error` surfaces failures the way the
  bugfix pipeline already does.
- **Templates** — vendor the four `gears-sdlc` templates (PRD, DESIGN, ADR,
  FEATURE) into `stories/gears-rust/templates/` so the story is
  self-contained for tests (cassette/flow runs don't need the checkout); a
  scenario can repoint `template_dir` at the live checkout for real runs.
- **The PRD→DESIGN walk** (from #3): `prd` writes `gears/<gear>/docs/PRD.md`
  with `cpt-` IDs; its done arc seeds the proposal/DESIGN intake, which
  authors `gears/<gear>/docs/DESIGN.md` (and ADRs) threading the same IDs.
- **Profiles are the generalization** — `stories/gears-rust/` *is* the
  template for a second target: copy the dir, change the bindings/world,
  done. Capture that in the README so "generic" is concrete, not aspirational.

## Impact

- **Files:** new `stories/gears-rust/{app.yaml, scenarios/gears-rust.yaml,
  templates/*, flows/*, README.md}`. No engine change (all seams from #1–#3).
- **Compat:** entirely additive — a new instance alongside `kitsoki-dev`.
- **Docs on ship:** `stories/gears-rust/README.md` as the copy-me example +
  the external-targeting guide's "worked example" section.

## Tasks

- [ ] Scaffold `stories/gears-rust/app.yaml` from `kitsoki-dev` (imports
      `core` = dev-story + the prd chain from #3; `host_bindings` with the gh
      ticket adapter; full `hosts: declared` allow-list).
- [ ] `scenarios/gears-rust.yaml` — `workdir`/`repo_root`, `doc_scope`,
      `template_dir`, gh repo slug, `judge_mode`.
- [ ] Vendor the four `gears-sdlc` templates into `templates/`; set
      `doc_placement: per_scope`, `publish_durable_path: gears`.
- [ ] Author-prompt addenda for `cpt-` ID minting + the PRD/DESIGN/ADR
      shapes; DCO + conventional-commit message shaping on `vcs.commit`.
- [ ] Bind `ci` verbs to `make check` / `make test`.
- [ ] Flow fixtures (mock oracle + exec cassettes, no real gh/LLM):
      `ticket pickup (gh) → bugfix → make check`; `prd → DESIGN` writing
      under `gears/<gear>/docs/` with threaded `cpt-` IDs.
- [ ] `README.md` documenting launch, the profile, and "copy this dir for a
      new target."
- [ ] On ship: extract the reusable bits (the profile checklist, the
      copy-me steps) into `docs/stories/` external-targeting guide; trim this
      proposal to anything still gears-rust-specific, else delete.

## Open questions

1. **Vendor the `gears-sdlc` templates or reference the live checkout?**
   *Lean: vendor for self-contained tests, allow a scenario to repoint
   `template_dir` at the checkout for real runs — gets both determinism and
   freshness.*
2. **Instance home — kitsoki repo vs `.kitsoki/` in gears-rust?** (epic Q1)
   *Lean: kitsoki repo, `workdir`-agnostic via warp, so the same dir copies
   into gears-rust later if they want it vendored.*
3. **How much `cpt-`-ID validation do we replicate** vs trust gears-rust's
   own Cypilot checks at `make check`? *Lean: mint correctly-shaped IDs and
   let their gate validate — don't reimplement their traceability checker.*

## Non-goals

- Replacing gears-rust's Cypilot SDLC, PR-review (`.prs/`), or `make check` —
  the instance produces conforming artifacts and runs their gate; it does
  not supersede their process.
- A second target in this slice — generality is *demonstrated* by the
  copy-me README, exercised when a real second target lands.
