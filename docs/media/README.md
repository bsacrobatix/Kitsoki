# Media Artifacts

Kitsoki has two long-lived media families:

- **Product-site demo videos**: tour-driven MP4s generated from the feature
  catalog and deterministic no-LLM runs.
- **Slidey decks with embedded clips**: Slidey JSON decks that may embed rrweb
  captures as replayable video scenes.

Generated media belongs in `.artifacts/` or in gitignored staging directories.
Committed media should be a source artifact: a catalog entry, a recording spec,
an rrweb clip intentionally embedded by a deck, or a small static image that a
deck/site needs to render.

## Product Demo Videos

Source of truth:

- `features/<id>.yaml` declares title/copy, tour steps, the demo binding, and
  optional QA scenarios.
- `tools/runstatus/src/tour/generated/<id>.ts` is generated from the feature
  YAML by `make features`.
- `tools/runstatus/tests/playwright/*-video.spec.ts` or `kitsoki tour` records
  the feature, always with deterministic flows/cassettes and no live LLM.

Generated outputs:

- `.artifacts/<demo>/` contains the canonical `<videoBase>.mp4`, the
  `<videoBase>.mp4.chapters.json` sidecar, and numbered `NN-<stepId>.png`
  screenshots.
- `tools/site/src/public/media/<feature>/` is staged from `.artifacts/` by the
  site build. It is not the source of truth.
- `tools/site/.vitepress/dist/media/<feature>/` is built site output.

Commands:

```bash
make demo-feature FEATURE=agent-actions  # one feature
make demos                               # every stale feature demo
make render-tour                         # stitched complete-product-tour
make site                                # stage media and build the site
make media-check                         # no-LLM media contract check
```

Vision QA is gated and never part of automated tests:

```bash
make feature-qa FEATURE=agent-actions
make tour-qa
```

## Current Product-Site Inventory

The feature catalog currently stages these demo ids when their artifacts exist:
`agent-actions`, `chat-stream`, `design-walkthrough`, `dev-story-bugfix`,
`diagram-showcase`, `harness-picker`, `meta-mode`, `mockup-video`,
`multi-story`, `onboarding-tour`, `operator-ask`, `review`, `story-editor`,
`trace-features`, `trace-introspection`, `weather-report`, and `web-inbox`.
`complete-product-tour` is stitched from section clips instead of recorded by a
single spec.

## Slidey Deck Clips

The current checked-in Slidey decks live under `docs/decks/`. That directory is
useful for existing examples, but it should not become a dumping ground for every
generated deck and every intermediate clip.

Until a dedicated deck catalog exists, use this rule:

- A committed deck file may live in `docs/decks/<deck-id>.slidey.json`.
- Any committed rrweb clip it references must live under
  `docs/decks/assets/<deck-id>/`.
- Generated deck renders, MP4s, HTML bundles, screenshots, and throwaway clips
  belong under `.artifacts/<deck-id>/` — with ONE exception:
  `docs/decks/bundled/<deck-id>.html`, the committed self-contained bundle
  (`slidey bundle <deck> <html>`) that a feature's `demo.embed` serves as its
  site demo (below). It is committed because the Pages build cannot run the
  slidey CLI; re-bundle it whenever the source deck or its clips change.
- Decks produced by stories for runtime use belong with the story, such as
  `stories/<story>/baked/`, not in `docs/decks/`.

### Deck embeds on the product site (`demo.embed`)

A rrweb-native story-demo (its `*-video.spec.ts` is a permanent stub — the
walk is captured by a companion `*-rrweb-capture.spec.ts` and consumed by a
Slidey deck, never rendered to MP4) presents on its `/features/<id>` page as an
embedded deck clip instead of a video:

- `features/<id>.yaml` sets `demo.embed: { deck, rrweb }` — the source deck
  JSON plus the clip's `rrweb` path as written in that deck. Codegen resolves
  the scene index from the deck (never authored by hand) and emits
  `demo.embed.{deckHtml,sceneIndex}` into the features index.
- The committed `docs/decks/bundled/<deck-id>.html` is staged verbatim to
  `tools/site/src/public/decks/` (shared — several features can embed different
  scenes of one deck) and the page renders it in an iframe at `?scene=N`.
- `make features-check` validates the binding (deck exists, rrweb scene
  resolves, bundled html present); `make media-check` re-checks the index side.
  The embedded (binary `/help/`) variant excludes deck bundles like it excludes
  MP4s.

Current `demo.embed` features: `slidey-dev-prd-design`,
`slidey-architect-design`, `slidey-decomposition`, `slidey-bugfix`, and
`slidey-open-pr`, all scenes of `docs/decks/dev-story-hybrid.slidey.json`.

Current committed rrweb deck clips:

- `docs/decks/dev-story-hybrid.slidey.json`
- `docs/decks/assets/dev-story-hybrid/report-bug.rrweb.json`
- `docs/decks/assets/dev-story-hybrid/web-inbox.rrweb.json`
- `docs/decks/assets/dev-story-hybrid/pm-idea.rrweb.json`
- `docs/decks/assets/dev-story-hybrid/architect-design.rrweb.json`
- `docs/decks/assets/dev-story-hybrid/decomposition.rrweb.json`
- `docs/decks/assets/dev-story-hybrid/slidey-bugfix.rrweb.json`
- `docs/decks/assets/dev-story-hybrid/feature-refine.rrweb.json`
- `docs/decks/assets/dev-story-hybrid/open-pr.rrweb.json`

The long-term shape should mirror feature demos: a small catalog entry per deck
that names the source story/flow, render command, QA scenarios, and published
artifact paths. Until then, `make media-check` enforces the deck-local rrweb
layout so new deck clips do not sprawl across `docs/decks/`.
