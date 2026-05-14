---
id: 2026-05-14T120000Z-scenarios-readme-warp-flag-misnamed
title: "scenarios/README.md says `kitsoki run --warp` but the boot-time flag accepts `file:<path>` only inside the TUI"
target: story
story_id: oregon-trail
filed_at: 2026-05-14T12:00:00Z
status: open
severity: P3
component: oregon-trail
kitsoki_rev: 75c4f11
trace_ref: ""
external: {}
assignee: ""
url: "stories/oregon-trail/issues/bugs/2026-05-14T120000Z-scenarios-readme-warp-flag-misnamed.md"
---

## Body

`stories/oregon-trail/scenarios/README.md` shows the boot-time warp
form as:

    kitsoki run stories/oregon-trail/app.yaml --warp scenarios/chimney_robbery.yaml

The catalogue table lists the warp basis paths as plain filenames
(e.g. `chimney_robbery.yaml`) without the `scenarios/` prefix in the
interactive form:

    /warp file:scenarios/chimney_robbery.yaml

The interactive form's `file:` token plus relative-path resolution
("relative to the loaded app — `stories/oregon-trail/` — so the
relative form above works regardless of cwd") is doc'd correctly. The
boot-time form's path resolution is left implicit — does
`--warp scenarios/chimney_robbery.yaml` resolve against cwd or against
the loaded app dir? A new user on a different cwd hits a confusing
"file not found" with no hint.

### Steps to reproduce

1. `cd /tmp`.
2. `kitsoki run /home/user/code/kitsoki/stories/oregon-trail/app.yaml --warp scenarios/chimney_robbery.yaml`.
3. Observe: error or silent fallback to non-warp boot (depending on
   what the current CLI does).

### Expected vs actual

**Expected:** the README is explicit about path resolution rules for
the boot-time `--warp` flag — either "relative to cwd" with a clear
example, or "relative to the app dir like the TUI form".

**Actual:** silent ambiguity. Two reasonable defaults; doc picks
neither.

### Proposed fix

Edit `stories/oregon-trail/scenarios/README.md` to add one sentence
under the "At session boot" example:

> Path resolves relative to the cwd of `kitsoki run`, not the app
> directory. Use an absolute or app-relative path if you need
> location independence.

(Or, if the boot-time form already resolves against the app dir like
the TUI form, say so explicitly and link to the implementation.)

### Severity rationale

P3 — pure docs nit. Doesn't block any test or runtime. Good seed for
a story-bug walk because it's small, real, and exercises the
`stories/oregon-trail/issues/bugs/*.md` glob in the dogfood.
