# seed_annotation.star — seed a default LOCATION-TIED annotation when none is
# attached, so the refine loop is drivable from a baked anchor (kitsoki tour/web
# with no live point). A live surface that attaches args.visual.anchor lands its
# own bundle in world.annotation and this is skipped (the room's `once:` is keyed
# on annotation).
#
# The seeded anchor is a `semantic_element` on a named scene element — the v2
# generalization of the rrweb element anchor. The AnnotationAnchor union also
# admits {kind: region, bbox:[x,y,w,h]} and {kind: point, point:{x,y}}; the
# seeded default uses semantic_element so the refine pass demonstrates the
# element→scene resolution.
#
# Interface (authoritative in seed_annotation.star.yaml):
#   inputs:  spec_path (string?), frame_handle (string?)
#   world:   annotation (object)
#   outputs: annotation (object)

def main(ctx):
    existing = ctx.world.get("annotation") or {}
    if type(existing) == "dict" and existing.get("anchor"):
        # A live bundle is already attached — leave it.
        return {"annotation": existing}

    spec_path = ctx.inputs.get("spec_path", "")
    anchor = {
        "kind": "semantic_element",
        "source_ref": {
            "kind": "slidey",
            "spec_path": spec_path,
            "scene_id": "the-loop",
            "element_id": "loop-callout",
        },
        "label": "scene the-loop · callout \"feedback is location-tied\"",
    }
    return {
        "annotation": {
            "anchor": anchor,
            "instruction": "Make the callout bolder and add a one-line example beneath it.",
            "frame_handle": ctx.inputs.get("frame_handle", ""),
        },
    }
