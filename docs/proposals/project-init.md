# Story: dev-story project onboarding completion tail

**Status:** Trimmed after the first-run onboarding stack shipped. The current
baseline is documented in [`docs/project-onboarding.md`](../project-onboarding.md)
and [`docs/stories/dev-story-onboarding.md`](../stories/dev-story-onboarding.md):
no-arg `kitsoki run` / `kitsoki web` start the embedded dev-story root in an
unconfigured project, the `init` rooms discover and apply a checked-in
`.kitsoki` setup, generated profiles are schema-validated, the generated config
uses `root.import: dev-story`, stack defaults are inferred, toolkit + MCP setup
is loud and retryable, and associated Claude/Codex transcript history is written
as an operator-review seed with a disabled runtime mining scope. Onboarding also
writes an explicit `.kitsoki/check-readiness.py` verifier for the declared
post-apply checks; the verifier can persist a summarized `readiness:` result
back into the profile when the operator passes `--update-profile`, but does not
run project commands automatically. Emitted session-mining recipe reports can
also be promoted into pending profile customizations with
`.kitsoki/promote-session-mining.py`. This proposal now tracks only the
remaining completion work before deletion.
**Kind:**   story
**Epic:**   standalone

## Why

Project onboarding is now usable as the front door for a normal repository, but
two parts of the original project-init ambition intentionally remain outside
the shipped baseline:

- transcript history is detected, scoped in `.kitsoki.yaml`, and handed off; an
  explicit helper can promote emitted mining reports into pending
  profile/story customizations, but onboarding still needs a first-class
  in-story review surface for that loop;
- apply validates the profile and generated story instance and writes an
  explicit verifier that can feed results back into the profile's `readiness`
  block, but onboarding still needs a more integrated operator surface for
  deciding when to run it.

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
- transcript discovery writes `.context/kitsoki-session-mining-seed.md`, a
  pending profile `mining` job, and disabled `.kitsoki.yaml` mining scope, but
  does not run a mining agent during onboarding.

Add only the remaining tail work:

1. **Consent-backed transcript promotion.** Turn the seed note into a normal
   operator-reviewed mining path that can propose changes to
   `onboarding.story_customizations`, profile commands, rules, or docs
   placement. The generated promotion helper can record pending customization
   entries from emitted recipe reports; remaining work is the in-story review
   and accept/refine UX. Tests must use recorded or synthetic fixtures, never a
   live LLM.
2. **Post-apply readiness verification.** Add a deterministic optional verify
   path that runs the target profile's declared local checks and clearly
   distinguishes pre-existing project failures from onboarding regressions. The
   generated verifier script exists and can update the profile explicitly;
   remaining work is an in-story operator action around that script.
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
- [x] Pre-fill disabled runtime mining scope for the discovered transcript
  sources.
- [x] Generate a deterministic promotion helper that records emitted mining
  recipes as pending profile customizations.
- [x] Generate an explicit `.kitsoki/check-readiness.py` verifier for
  profile-declared checks without running project commands during onboarding.
- [x] Let explicit readiness runs update the profile's `readiness:` summary.
- [x] Move shipped behavior into narrative onboarding docs.
- [ ] Add an in-story operator review/accept/refine path for promoted
  transcript-mined customizations.
- [ ] Add an in-story operator action for running and reviewing readiness
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
