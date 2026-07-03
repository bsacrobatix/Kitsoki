# Story: dev-story project onboarding completion tail

**Status:** Trimmed after the first-run onboarding stack shipped. The current
baseline is documented in [`docs/project-onboarding.md`](../project-onboarding.md)
and [`docs/stories/dev-story-onboarding.md`](../stories/dev-story-onboarding.md):
no-arg `kitsoki run` / `kitsoki web` start the embedded dev-story root in an
unconfigured project, the `init` rooms discover and apply a checked-in
`.kitsoki` setup, generated profiles are schema-validated, the generated config
uses `root.import: dev-story`, stack defaults are inferred, toolkit + MCP setup
is loud and retryable, and associated Claude/Codex transcript history is written
as an operator-review seed. This proposal now tracks only the remaining
completion work before deletion.
**Kind:**   story
**Epic:**   standalone

## Why

Project onboarding is now usable as the front door for a normal repository, but
two parts of the original project-init ambition intentionally remain outside
the shipped baseline:

- transcript history is detected and handed off, but there is not yet an
  operator-controlled path that promotes the seed into concrete, reviewed
  profile/story customizations;
- apply validates the profile and generated story instance, but it does not yet
  prove the target project's own readiness loop after writing files.

Those are useful follow-up slices, but they should not keep stale design text
describing the already-shipped first-run path in `docs/proposals/`.

## What Changes

Keep the shipped onboarding spine as the base:

- `kitsoki run` with no app path starts the embedded dev-story root in an
  unconfigured project.
- `kitsoki web` also exposes the implicit root when no local story directory is
  configured yet.
- `init_discover.py` and `init_apply.py` discover, review, validate, and apply a
  checked-in `.kitsoki` setup without a real LLM in tests.
- generated profiles record the selected starter story, deterministic repo
  evidence, and queued project-local customizations under `onboarding`.
- transcript discovery writes `.context/kitsoki-session-mining-seed.md` and a
  pending `mining` job, but does not run a mining agent during onboarding.

Add only the remaining tail work:

1. **Consent-backed transcript promotion.** Turn the seed note into a normal
   operator-reviewed mining path that can propose changes to
   `onboarding.story_customizations`, profile commands, rules, or docs
   placement. Tests must use recorded or synthetic fixtures, never a live LLM.
2. **Post-apply readiness verification.** Add a deterministic optional verify
   step that runs the target profile's declared local checks, records the
   outcome in the profile or onboarding report, and clearly distinguishes
   pre-existing project failures from onboarding regressions.
3. **Completion audit.** Once those two slices ship or are explicitly deferred
   elsewhere, migrate any lasting behavior to narrative docs and delete this
   proposal.

## Impact

- **Story layer:** likely one small continuation room or action from
  `init_done` for transcript seed review, plus a verify action that reuses the
  existing profile command fields.
- **Scripts:** deterministic helpers may be needed to render seed evidence,
  update the profile, and run the declared checks with bounded timeouts.
- **Docs:** update `docs/project-onboarding.md` and
  `docs/stories/dev-story-onboarding.md` with any new operator path. Do not add
  another shipped-feature doc under `docs/proposals/`.
- **Compatibility:** additive. Existing onboarded projects keep their generated
  `.kitsoki.yaml`, `.kitsoki/project-profile.yaml`, and materialized
  `.kitsoki/stories/<id>-dev/app.yaml`.

## Tasks

- [x] Ship first-run `kitsoki run` implicit root for unconfigured projects.
- [x] Ship first-run `kitsoki web` implicit root discovery.
- [x] Generate `.kitsoki.yaml` with `project_profile` and
  `root.import: dev-story`.
- [x] Validate generated profiles and generated dev-story instances before
  reporting apply success.
- [x] Infer common Go, Rust, Node, Python, Makefile, git, and non-git defaults.
- [x] Persist associated Claude/Codex transcript discovery as a reviewable seed
  handoff without running live mining.
- [x] Move shipped behavior into narrative onboarding docs.
- [ ] Add an operator-controlled, no-live-LLM-tested transcript promotion path.
- [ ] Add deterministic post-apply readiness verification for profile-declared
  checks.
- [ ] Delete this proposal after the remaining tail is shipped or split into
  narrower active proposals.

## Open Questions

- Should readiness verification run automatically after apply, or remain an
  explicit action from `init_done` so onboarding never surprises a target repo by
  running project commands?
- Should accepted transcript-mined customizations update only
  `.kitsoki/project-profile.yaml`, or also regenerate the materialized local
  story wrapper when the customization affects story behavior?

## Non-goals

- No automated test may call a real LLM or incur provider cost.
- No first-class long-running dev-server lifecycle host is required for this
  tail; use declared project commands and bounded host runs.
- No project-specific special cases should be added to the shared dev-story
  root. Project shape belongs in the generated profile and local wrapper.
