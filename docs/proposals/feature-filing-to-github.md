# Story: feature filing mints a GitHub issue

**Status:** Draft v1. Nothing implemented yet.
**Kind:**   story
**Epic:**   ./github-issues-tracker.md

## Why

The design pipeline (`stories/dev-story/`, run via `stories/kitsoki-dev/`)
publishes an accepted design twice: it writes the proposal doc into
`docs/proposals/`, and it mints a **feature ticket** at
`issues/features/<id>.md` linking back to the proposal — both in
`publish_design.py` (`write_feature_ticket`,
`stories/dev-story/scripts/publish_design.py:93-137`), driven from the
`design_draft` accept arc (`stories/dev-story/rooms/design_draft.yaml`).

For the cutover, the feature ticket must be a **GitHub issue** instead of a
local file, so a published design shows up where contributors and the
dogfood loop now look (epic shared decision #1). This is the feature-side
twin of slice #2's bug-side change.

## What changes

Replace `write_feature_ticket`'s file write with a `host.gh.ticket` `create`
call (slice #1):

- The publish step mints a GitHub issue titled from the design, labelled
  `target:kitsoki` + `comp:proposal` (the local format used
  `component: proposal`, `publish_design.py`), bodied with the proposal link
  + the originating idea, and returns `{ticket_id, ticket_url}` in the same
  JSON envelope the room already consumes (`ticket_id`/`ticket_path`/… →
  now `ticket_url`).
- The `design_ticket_dir` world key (`stories/dev-story/app.yaml:374-379`,
  default `issues/features`) is **retired** for the kitsoki-dev instance in
  favour of the `ticket_repo` pin (slice #1). The proposal doc still lands in
  `docs/proposals/` — only the *ticket* moves to GitHub.

One sentence: **publishing a design opens a labelled GitHub feature issue on
`constructorfabric/Kitsoki` linking the proposal, instead of writing
`issues/features/<id>.md`.**

## Design

- **Where the call lives.** `publish_design.py` runs under `host.run`
  (`design_draft.yaml`), so it can't call the Go host registry directly. Two
  shapes (Open question 1): *(a)* the script shells `gh issue create` itself
  (it already shells subprocesses), or *(b)* the room gains a
  `host.gh.ticket.create` `invoke:` step after the script writes the
  proposal, threading the proposal path in. *Lean: (b)* — keep issue creation
  on the one audited provider (slice #1: cassettes, label map, body metadata,
  degradation) rather than a second `gh` shell-out in Python that bypasses
  the `cliExec` cassette seam.
- **Envelope compat.** The room reads `ticket_id`/`ticket_title` from the
  publish result (`design_draft.yaml`). Keep those keys; add `ticket_url`;
  drop `ticket_path`. The "design published, ticket F-… filed" confirmation
  view becomes "…GitHub issue #N filed" with the URL.
- **Labels/body** reuse slice #1's map and metadata block — a feature issue
  is just a ticket with `comp:proposal`, so no new vocabulary.
- **Cassette.** The `design_draft` publish flow fixture
  (`stories/kitsoki-dev/flows/…`) records the `gh issue create` exec so the
  design walk's tests never hit real GitHub (CLAUDE.md).

## Impact

- **Code/story:** `stories/dev-story/scripts/publish_design.py` (drop
  `write_feature_ticket`'s file write; or keep it writing only the proposal
  and hand ticket-minting to the room); `stories/dev-story/rooms/design_draft.yaml`
  (the `host.gh.ticket.create` step + updated confirmation view, if option b);
  `stories/kitsoki-dev/app.yaml` (retire `design_ticket_dir`, rely on
  `ticket_repo`).
- **Flows:** the design-publish flow fixture + exec cassette for create.
- **Docs on ship:** the design-pipeline publish section of the
  `dev-story` / `kitsoki-dev` READMEs (a published design files a GitHub
  issue).
- **Compat:** `external-project-targeting`'s gears-rust instance keeps its
  own `design_ticket_dir` behaviour — only the kitsoki-dev instance flips to
  gh. The seam stays per-instance (it already is).

## Tasks

```
- [ ] Decide call shape (script shell-out vs room invoke); lean: room invoke
      on host.gh.ticket.create.
- [ ] Publish step mints a GitHub feature issue (target:kitsoki +
      comp:proposal labels; proposal link + idea in body); returns ticket_url.
- [ ] Update design_draft confirmation view + the publish JSON envelope
      (ticket_url replaces ticket_path).
- [ ] Retire design_ticket_dir on kitsoki-dev; rely on ticket_repo (slice #1).
- [ ] Publish flow fixture + exec cassette; assert no real gh runs.
- [ ] Migrate the publish surface into the dev-story/kitsoki-dev READMEs;
      trim this proposal.
```

## Open questions

1. **Script shell-out vs. room `invoke`.** *Lean: room invoke on
   `host.gh.ticket.create`* — single audited provider, cassette coverage,
   shared label/body map. The script keeps writing the proposal doc only.
2. **Does the design walk pick the issue back up** (e.g. assign it, comment)?
   *Lean: out of scope here* — `list_mine`/`get`/`comment` already exist
   (`github.go`) and the existing-state/PRD→Design chain is unchanged; this
   slice only changes where the ticket is *minted*.

## Non-goals

- Bug filing (slice #2) and the migration of existing `issues/features/`
  (slice #4).
- Changing the proposal-doc publish (`docs/proposals/` write) — unchanged.
- The PRD→Design chain or gears-rust's publish behaviour
  (`external-project-targeting.md`) — untouched.
