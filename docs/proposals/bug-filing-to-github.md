# Runtime: bug filing creates a GitHub issue

**Status:** **Web path code-shipped + verified against real GitHub.**
`kitsoki web --ticket-repo <owner/repo>` routes `runstatus.bug.report` to a real
GitHub issue via `host.GitHubFileBug` (`internal/host/github_bug.go`,
`internal/runstatus/server/bug_report.go`, `server.go` `WithTicketRepo`,
`cmd/kitsoki/web.go`). Evidence (screenshot/HAR/rrweb/console) is uploaded as
assets on a `bug-evidence` release (gh/PAT can't attach binaries to an issue
natively) and linked + inlined in the body, which also carries the slice-#1
labels + `kitsoki` metadata block. Unit-tested with a stubbed `cliExec`.
**Verified for real:** https://github.com/bsacrobatix/Kitsoki/issues/3 (labels
P2/comp:web/target:kitsoki, evidence on the repo's `bug-evidence` release).
**Remaining:** the CLI `kitsoki bug create --github` path (web path shipped
first, as planned), and migrating the surface into `docs/stories/bugs.md` +
`docs/tui/web-ui.md`, then delete this proposal.
**Kind:**   runtime
**Epic:**   ./github-issues-tracker.md

## Why

Two paths file kitsoki bugs today and both write a local Markdown file via
`bugfile.Create` (`internal/bugfile/bugfile.go:71`):

- the CLI `kitsoki bug create` (`cmd/kitsoki/bug.go`), and
- the web `runstatus.bug.report` RPC
  (`internal/runstatus/server/bug_report.go:69`) — the just-shipped
  "Report bug" Meta-menu modal that captures a **screenshot + HAR + rrweb**
  (`docs/proposals/web-bug-report.md`, now implemented).

For the cutover (epic shared decision #1) both must instead create a **GitHub
issue** on `constructorfabric/Kitsoki`. The web path adds a wrinkle the CLI
doesn't have: it produces **binary evidence** that has nowhere to live in a
plain issue body — and the user's call is to **upload it to the issue**.

## What changes

Route both filing paths through the slice-#1 `host.gh.ticket` `create` op
instead of `bugfile.Create`, and upload the web modal's evidence as GitHub
issue attachments:

- **CLI** — `kitsoki bug create` calls `create` (title/body/labels from the
  same `CreateRequest` fields, `bugfile.go:29-58`) and prints the issue URL
  instead of a file path. The `severity`/`component`/`target` fields become
  labels (slice #1's map); `trace_ref`/`kitsoki_rev`/`filed_by` go in the
  body metadata block.
- **Web RPC** — `runstatus.bug.report` creates the issue, then **uploads**
  `screenshot.png`, `har.json`, `session.rrweb.json` and rewrites the body's
  `## Artifacts` section to point at the uploaded attachment URLs. The
  existing two-phase preview/report flow (`bug_report.go:53-150`) and the
  server-side HAR scrub (`bug_report.go:41-46`, `internal/runstatus/harscrub/`)
  are unchanged — only the *sink* changes from `bugfile.Create` + sibling
  `<id>.artifacts/` folder to a GitHub issue + uploaded attachments.

One sentence: **filing a bug — from the shell or the web modal — opens a
labelled GitHub issue on `constructorfabric/Kitsoki`, with the web modal's
evidence uploaded to it.**

## Design

- **Attachment upload.** `gh` has **no native binary-attachment command**, so
  the upload uses the GitHub REST path `gh` *can* reach:
  `gh api` against the repo's asset-upload endpoint, or — the robust route —
  attach by referencing files committed to a `.artifacts/` ref and linking
  them. *Lean (Open question 1):* use the documented
  user-content upload endpoint that the web UI uses to upload the bytes, then
  a body-edit (`PATCH /repos/{owner}/{repo}/issues/{n}`) inserting the markdown
  image/file links; if that proves unreachable from `gh api` with the
  operator's token scope, fall back to committing artifacts under a
  repo-side `issues-artifacts/<issue>/` path and linking by raw URL. **This is
  the slice's main risk and must be spiked before the web path lands** —
  the CLI path (text-only) has no such dependency and can ship first.
- **Shared sink helper.** Both call sites already funnel through one package
  (`bugfile`, extracted precisely so "non-CLI callers reuse the exact same
  creation orchestration", `bugfile.go:6-10`). Keep that: add a
  `bugfile`-level seam that, given a `CreateRequest`, dispatches to the
  `host.gh.ticket` `create` op (via the host registry) rather than the file
  writer — so the CLI and the RPC stay symmetric and the label/body-metadata
  mapping lives in one place.
- **Body composition.** Reuse the existing frontmatter→body logic
  (`cmd/kitsoki/bug.go:154-190` per `web-bug-report.md`): the prose body
  carries repro/expected/actual; the `kitsoki` metadata block (slice #1)
  carries `trace_ref`/`kitsoki_rev`/`filed_by`; the `## Artifacts` section
  carries the attachment links (web path only).
- **Degradation.** No `gh` / not authed → the same clean error the provider
  already returns (epic decision #3); the CLI prints it, the web modal shows
  it in the result toast (`web-bug-report.md` "error" state). A label 403 on
  a fork contributor files the issue unlabelled with a warning (slice #1).
- **Scrub still runs first.** Evidence is scrubbed *before* upload exactly as
  today (`bug_report.go:41-46`) — uploading to a **public** repo raises the
  stakes the `web-bug-report.md` "Anonymization is best-effort" section
  already flagged; the existing review-before-file modal step is the
  mitigation and stays.

## Impact

- **Code:** `internal/bugfile/` (new gh-create dispatch seam + label/body
  mapping reuse from slice #1); `cmd/kitsoki/bug.go` (print URL not path);
  `internal/runstatus/server/bug_report.go` (create issue + upload
  attachments + rewrite `## Artifacts` links). No web-frontend change — the
  modal's capture/scrub/preview is unchanged; only the backend sink moves.
- **Tests:** bugfile-level test that `create` is called with the right
  title/labels/body (stubbed host registry, no real `gh`); a
  `runstatus.bug.report` backend test asserting issue-create + attachment
  upload against a stubbed gh/REST seam; the existing anonymizer tests
  unchanged.
- **Docs on ship:** `docs/stories/bugs.md` (filing now targets GitHub; the
  `external:`/local format is archived) and `docs/tui/web-ui.md` (the Report
  bug result toast links a GitHub issue).
- **Compat:** with hard cutover (epic decision #1) the local-file sink is
  **removed** from these paths, not kept as a fallback. `bugfile`'s file
  writer stays in the tree only as long as the migration tool (#4) reuses it
  to *read* the old pile.

## Tasks

```
- [ ] Spike the attachment-upload route (gh api user-content vs repo-side
      committed artifacts); decide before the web path lands.
- [ ] bugfile gh-create dispatch seam (CreateRequest → host.gh.ticket.create);
      label + body-metadata mapping reused from slice #1.
- [ ] CLI: `kitsoki bug create` files a GitHub issue, prints the URL.
- [ ] Web RPC: `runstatus.bug.report` creates the issue, uploads
      screenshot/har/rrweb, rewrites the `## Artifacts` links.
- [ ] Tests: bugfile create-call test; backend create+upload test (stubbed
      gh/REST, no network); anonymizer tests unchanged.
- [ ] Migrate the filing surface into docs/stories/bugs.md + docs/tui/web-ui.md;
      trim this proposal.
```

## Open questions

1. **Attachment-upload mechanism** — `gh api` user-content upload vs.
   committing artifacts repo-side and linking. *Lean: spike `gh api` first*
   (self-contained issue, the user's stated preference); fall back to
   repo-side commit only if token scope blocks it.
2. **Does the web modal need the issue number before or after upload?** Create
   returns the number (slice #1), then upload + body-edit — a two-call
   sequence. *Lean: yes, two calls* (create → attach → edit-body), matching
   GitHub's own model; record both in the trace.

## Non-goals

- Changing the modal's client-side capture/scrub/preview — unchanged from
  the shipped `web-bug-report.md`.
- The `transition`/resolve path (already in `host.gh.ticket`,
  `github.go:217`) — this slice is filing only.
- Feature filing (that's #3) and the migration (that's #4).
