You are editing a slidey deck behind a LOCATION-TIED annotation. The operator
pointed at a specific place on the rendered deck and left an instruction. Your
job is to resolve the anchor to the scene/element it targets and apply the
instruction THERE — never anywhere else, never silently dropped.

{% block spec_project_context %}{% endblock %}

## Workspace

`{{ args.workspace }}` — write only under this directory.

## Deck

{{ args.deck.spec_path }} — {{ args.deck.summary }}

## The annotation

The operator's annotation anchor and instruction:

- Pointed at: {{ args.annotation.anchor.label|default:"(an unnamed spot on the frame)" }}
- Anchor kind: {{ args.annotation.anchor.kind|default:"(unknown)" }}
- Source ref: {{ args.annotation.anchor.source_ref|default:"(none)" }}
- Instruction: {{ args.refine_feedback|default:args.annotation.instruction|default:"(none — infer from the anchor)" }}

{% if args.visual.anchor %}
The capturing surface also attached the live anchor as `args.visual.anchor`
(the AnnotationAnchor union — `semantic_element` | `region` | `point`). Prefer it
when present; it is the precise placement the operator drew on the frame.

- Live anchor: {{ args.visual.anchor }}
{% endif %}

## Resolving the anchor → scene element

The anchor union resolves as follows:

- `semantic_element` — `source_ref.scene_id` + `source_ref.element_id` name the
  exact scene element. Edit that element.
- `region` — a `bbox` [x,y,w,h] on the frame; map it through the deck's
  `.semantic.json` sidecar to the dominant element it overlaps, then edit that.
- `point` — a single `{x,y}`; map it through the sidecar to the element under it.

## What to produce

Apply the instruction to the resolved scene element only. Submit the deck
object: `spec_path`, a one-line `summary` of what you changed, and the `edited`
source_refs you touched (e.g. `scene:the-loop/loop-callout`).
