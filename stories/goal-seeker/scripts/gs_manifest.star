# gs_manifest.star — write a SINGLE-ITEM punch-list/v1 manifest for one change.
#
# Given a change_id + the decomposition, look up the change and emit a one-item
# punch-list manifest to <work_dir>/manifests/<change_id>.json (JSON is valid YAML,
# which punch-list's loader decodes). The change's pipeline picks the story punch-list
# drives; its gate.cmd becomes the deterministic verify. Returns the manifest path +
# the change object (bound into world.current_change for the integrate projection).

PIPELINE_STORY = {
    "bugfix": "stories/bugfix/app.yaml",
    "implementation": "stories/implementation/app.yaml",
    "docs": "stories/dev-story/app.yaml",
}


def main(ctx):
    goal_dir = ctx.inputs["goal_dir"].rstrip("/")
    work_dir = ctx.inputs["work_dir"].rstrip("/")
    change_id = ctx.inputs["change_id"].strip()
    if change_id == "":
        fail("gs_manifest: change_id is required")

    decomp_path = goal_dir + "/decomposition.yaml"
    if not ctx.fs.exists(decomp_path):
        fail("gs_manifest: no decomposition.yaml at " + decomp_path)
    doc = yaml.decode(ctx.fs.read(decomp_path))
    changes = doc.get("changes") or []

    change = None
    for c in changes:
        if str(c["id"]) == change_id:
            change = c
    if change == None:
        fail("gs_manifest: change_id '" + change_id + "' not in decomposition")

    gate = change.get("gate") or {}
    gate_cmd = str(gate.get("cmd") or "true")
    pipeline = str(change.get("pipeline") or "implementation")
    story = PIPELINE_STORY.get(pipeline, "stories/implementation/app.yaml")
    action = str(change.get("agent_brief") or change.get("title") or change_id)

    manifest = {
        "version": "punch-list/v1",
        "defaults": {
            "harness": "live",
            "profile": "codex-native",
            "model": "gpt-5.5",
            "trace_root": work_dir + "/traces",
        },
        "items": [{
            "id": change_id,
            "title": str(change.get("title") or change_id),
            "priority": 1,
            "story": story,
            "mode": "drive",
            "prompt": action,
            "gate_command": gate_cmd,
            "verify": [{"kind": "command", "cmd": gate_cmd}],
        }],
    }

    slug = change_id.replace("/", "_")
    manifest_path = work_dir + "/manifests/" + slug + ".json"
    ctx.fs.write(manifest_path, json.encode(manifest) + "\n")
    return {"manifest_path": manifest_path, "change": change, "gate_cmd": gate_cmd}
