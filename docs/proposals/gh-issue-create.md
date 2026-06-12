# Runtime: gh issue `create` op + constructorfabric pin

**Status:** Draft v1. Nothing implemented yet.
**Kind:**   runtime
**Epic:**   ./github-issues-tracker.md

## Why

`host.gh.ticket` (`internal/host/github.go`) already satisfies the `ticket`
interface for `search`/`get`/`comment`/`transition`/`list_mine` — every op
the dogfood loop reads or mutates with — **except filing a new issue**. The
local-files provider files implicitly (a writer drops an `issues/bugs/<id>.md`
file); there is no symmetric `host.gh.ticket` op, so nothing downstream can
create a GitHub issue. This is the one gap between "read tickets from GitHub"
(shipped, dormant) and "kitsoki's tracker *is* GitHub" (the epic). It's also
the substrate slices #2/#3/#4 all call, so it lands first and pins the
conventions (repo slug, label vocabulary, body metadata block) they reuse.

## What changes

Add a **`create`** op to `host.gh.ticket` and the conventions around it:

| iface op | gh call |
|---|---|
| `create` *(new)* | `gh issue create --repo <slug> --title <t> --body <b> --label <l>…` → returns `{id, url, number}` |

- **`create` op** — `internal/host/github.go`: a `ghTicketCreate` dispatcher
  alongside the existing five (`github.go:57-70`), shelling
  `gh issue create … --json number,url` (or parsing the issue URL `gh`
  prints), normalized through the same `ghIssueSummary` projection
  (`github.go:300`) so callers get the provider-neutral `{id, url, …}` shape.
- **Repo pin** — a `kitsoki-dev` world key `ticket_repo:
  constructorfabric/Kitsoki`, threaded into every `host.gh.ticket` call's
  `repo` arg (the provider already honors `repo`, `github.go:78`). This is
  the epic's "canonical even from a fork" decision: without it `gh` resolves
  the operator's `origin` remote, which on a personal fork is the wrong repo.
- **Label vocabulary** — a fixed mapping from the local frontmatter axes to
  GitHub labels, applied by `create` and understood by `transition`:
  `severity P0..P3` → labels `P0`..`P3`; `component: tui` → `comp:tui`;
  `target: kitsoki|story` → `target:*`; `status: in_progress` → an
  `in_progress` label (open/closed already handled by `transition`,
  `github.go:238`).
- **Body metadata block** — a fenced block the `create` op writes into the
  issue body to carry the fields GitHub has no home for (`trace_ref`,
  `kitsoki_rev`, `filed_by`, original `id`), and `get` parses back out:

  ````
  ```kitsoki
  trace_ref: <ref>
  kitsoki_rev: <sha>
  filed_by: <user>
  legacy_id: 2026-05-14T103205Z-tui-hang   # set by the migration (#4)
  ```
  ````

## Design

- **Shape.** `ghTicketCreate(ctx, args)` takes `title`, `body`, `repo`,
  `labels ([]string)`, `assignee?`; builds `gh issue create` args (mirroring
  `repoFlag`, `github.go:78`); execs via `cliExec` (`github.go:103` pattern);
  returns `Result{Data: {id, url, number}}`. Reuses `splitIssueID` /
  `ghIssueSummary` for symmetry with `get`.
- **Label set is data, not Go literals.** The severity/component/target/status
  → label map lives in one place the provider exposes, so #2/#3/#4 emit the
  same labels and the local-format reader can be retired without label drift.
  (Open question 1 — whether the map is a Go table or a `kitsoki-dev` world
  key. *Lean:* Go table for the fixed kitsoki axes, since they're the bug
  format's own enum, not per-project config.)
- **Auth & degradation.** Same as the existing ops: `ghAvailable` gate
  (`github.go:54`) → clean `Result.Error` when `gh` is missing/unauthed, so
  rooms route `on_error:`. `create` works for any authenticated user on a
  public repo; **label** application may 403 for a fork contributor without
  triage — in that case file the issue *without* labels and surface a warning
  rather than failing the create (epic open question 2).
- **Cassettes.** A recorded `gh issue create` exec cassette (and the
  augmented `get` showing the metadata block round-trip) under the
  `kitsoki-dev` flow fixtures, so the create path is exercised with **no real
  GitHub** (CLAUDE.md). Mirrors the existing `gh issue list/view/comment`
  fixtures.

## Impact

- **Code:** `internal/host/github.go` (new `ghTicketCreate` + the label-map
  helper + body-metadata read/write); the `create` case in the op switch
  (`github.go:57`). No engine change — it's one more op on an existing
  prefix-fallback handler.
- **Story:** `stories/kitsoki-dev/app.yaml` gains the `ticket_repo` world key
  (consumed by #4's binding); no room YAML changes here.
- **Tests:** Go unit test for `ghTicketCreate` against a stubbed `cliExec`
  (asserts the `gh issue create` argv, label flags, body block); an exec
  cassette fixture for the flow harness.
- **Docs on ship:** the `create` op row + label/body conventions into
  `docs/architecture/hosts.md` (the `host.gh.ticket` section).
- **Compat:** purely additive — the five existing ops are untouched; the
  default binding stays `host.local_files.ticket` until #4 flips it.

## Tasks

```
- [ ] `ghTicketCreate` op: `gh issue create … --json number,url`, normalized
      via ghIssueSummary; wire the `create` case into the op switch.
- [ ] Label map (severity/component/target/status → GitHub labels) in one
      place; applied by create, honored by transition; degrade to no-labels
      on a triage-permission 403 with a warning.
- [ ] Body `kitsoki` metadata block: write on create, parse on get
      (trace_ref / kitsoki_rev / filed_by / legacy_id).
- [ ] `ticket_repo: constructorfabric/Kitsoki` world key on kitsoki-dev;
      thread `repo` into the provider args (don't hard-code the slug in Go).
- [ ] Go unit test (stubbed cliExec) + exec cassette for create; round-trip
      test that get() recovers the metadata block.
- [ ] Migrate the create op + conventions into docs/architecture/hosts.md;
      update the epic slice row; trim this proposal.
```

## Open questions

1. **Label map: Go table or world key?** *Lean: Go table* — the
   severity/component/target axes are the bug format's own enum
   (`issues/README.md:52`), not per-project config; a downstream project that
   wants different labels can wrap the provider.
2. **`--json` on `gh issue create`?** Older `gh` prints only the issue URL on
   create. *Lean:* parse the URL (we already derive `number` from it via
   `splitIssueID`) and fall back to a follow-up `gh issue view` only if a
   field is needed that the URL doesn't carry.

## Non-goals

- Re-pointing the filing paths or the design pipeline — that's #2/#3 (this
  slice only adds the capability they call).
- The migration / rebind / freeze — that's #4.
- A general label taxonomy beyond the fixed kitsoki bug-format axes.
