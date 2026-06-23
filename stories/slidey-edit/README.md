# slidey-edit — author a deck, render it, annotate the frame, refine the scene

A standalone PoC story for the **unified artifact annotation** feature: it
drives the create → render → review → refine loop for a slidey deck, where the
review feedback is **location-tied**. A reviewer points at an exact place on the
rendered deck frame — a named scene element, a drawn region, or a single point —
and that **AnnotationAnchor** flows into a refine pass that edits the precise
scene the anchor targets.

It is the v2+ generalization of `spatial-oracle` (rrweb-only) to arbitrary
rendered artifacts (png / mp4 / rrweb / html / slidey), demonstrated end-to-end
on a slidey deck. It is the slidey cousin of `stories/mockup-video/` (walkthrough
video studio) — same producer (`host.slidey.render`), same media-review shape,
but with an **anchored** refine instead of a chapter/timecode flag.

```
kitsoki run stories/slidey-edit/app.yaml
```

## Rooms

```
idle ──start──▶ drafting ──accept──▶ rendering ──(auto)──▶ reviewing
                  (agent writes deck)   (slidey → mp4 +        media(deck) + seed
                                          .semantic sidecar)    annotation + checkpoint
                                                                  │
        ┌─────────────────────────────────────────────────────────┤
        │ accept→done · rerender→rendering · quit→@exit:abandoned   │ refine
        │                                                           ▼
        └──────────── rendering ◀──(re-render before/after)──── refining
                                                          (agent edits the scene the
                                                           anchor points at)
```

| Room | Split | What it does |
|---|---|---|
| `idle` | deterministic | The deck-to-edit (baked in `world.deck`). `start` re-drafts. |
| `drafting` | interpretive | ONE `host.agent.task` (`drafter`) authors/edits the deck JSON. Workspace-jailed, `once:`. |
| `rendering` | deterministic | `host.slidey.render` → mp4 **+ `.semantic.json` sidecar**; both emitted to `host.artifacts_dir` for stable handles. Auto-advances. |
| `reviewing` | deterministic | `media(deck_handle)` inline + seeds a baked `semantic_element` annotation. Checkpoint: accept / refine / rerender / quit. |
| `refining` | interpretive | ONE `host.agent.task` (`reviser`) consumes the annotation (`{{ args.visual.anchor }}` + the explicit `annotation` arg) and edits the targeted scene, then re-renders the before/after. |
| `done` | gallery | Final deck media + the annotations addressed per cycle. |

## The annotation contract

The reviewing → refining handoff carries an **annotation bundle** in
`world.annotation`:

```yaml
annotation:
  anchor:                       # the AnnotationAnchor union
    kind: semantic_element      # semantic_element | region | point
    source_ref:                 # for semantic_element: which scene element
      kind: slidey
      spec_path: stories/slidey-edit/baked/deck.json
      scene_id: the-loop
      element_id: loop-callout
    label: 'scene the-loop · callout "feedback is location-tied"'
  instruction: "Make the callout bolder…"
  frame_handle: "slidey-edit#1"
```

This mirrors the `args.visual.anchor` shape a capturing surface attaches (the
generalized `VisualAmbient`). The refine prompt prefers the live
`args.visual.anchor` when present and falls back to the explicit `annotation`
arg. An inline `refine feedback="…"` overrides the *instruction* while keeping
the *anchor* (where the operator pointed).

The `.semantic.json` sidecar (`baked/deck.semantic.json`) is the producer-side
map of named scene elements → bounding `region` [x,y,w,h] + `time_range` on the
rendered frame, so a `region`/`point` anchor resolves to the dominant element it
overlaps. It is the semantic cousin of the slidey chapter sidecar.

## Baked demo artifacts

`baked/` holds a tiny pre-rendered deck so rendering/reviewing show **real
media** without invoking slidey live (and so `kitsoki tour`/web can drive the
loop without authoring — the *tour needs a baked world* lesson):

- `deck.json` — the deck spec (3 scenes, named elements).
- `deck.mp4` — pre-rendered deck video (≈5 KB, ffmpeg-generated).
- `deck.poster.png` — a poster still.
- `deck.semantic.json` — the semantic sidecar.

The render host calls are **stubbed** in the flows/cassette to point at these
files; `host.artifacts_dir` runs for real under `kitsoki web --flow` so the
handle resolves through the journal.

## Deterministic, no-LLM testing

```
kitsoki test flows stories/slidey-edit/app.yaml
```

| Fixture | Covers |
|---|---|
| `flows/happy_path.yaml` | idle → drafting → rendering → reviewing → done. |
| `flows/refine_from_anchor.yaml` | the **location-tied loop**: a baked `semantic_element` anchor flows into refine; asserts the anchor reached the refine task + the addressed record. |
| `flows/refine_inline_override.yaml` | inline `refine feedback="…"` overrides the instruction, anchor unchanged. |
| `flows/refine_budget_exhaust.yaml` | refine refused at budget. |
| `flows/quit_at_review.yaml` | `@exit:abandoned`. |
| `flows/demo_web.yaml` | web/tour entry fixture (real baked media, stubbed agent/render). |

`cassettes/deck_review.cassette.yaml` supplies the same no-LLM posture for the
web/tour surface (matched by handler). Pair with `flows/demo_web.yaml`:

```
kitsoki web --flow stories/slidey-edit/flows/demo_web.yaml \
  --host-cassette stories/slidey-edit/cassettes/deck_review.cassette.yaml
```

## Exits

| Exit | `requires:` | When |
|---|---|---|
| `done` | `deck_handle` | accepted at reviewing/done — a real rendered deck exists. |
| `abandoned` | — | quit at idle / drafting / reviewing. |
