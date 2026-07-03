# gs_manifest.star — write a SINGLE-ITEM punch-list/v1 manifest for one change.
#
# Given a change_id + the decomposition, look up the change and emit a one-item
# punch-list manifest to <work_dir>/manifests/<change_id>.json (JSON is valid YAML,
# which punch-list's loader decodes). The change's pipeline picks the story punch-list
# drives; its gate.cmd becomes the deterministic verify. Returns the manifest path +
# the change object (bound into world.current_change for the integrate projection).
#
# The manifest item also carries structured target-story inputs (`ticket_id`,
# `ticket_title`, and a generic `world_in` block) so punch-list's driver can seed
# the target session's `initial_world` instead of relying on a free-text prompt
# alone — both stories/implementation and stories/bugfix self-provision (workspace/
# branch/workdir) from `world.ticket_id` in their `idle` rooms, but only when a
# caller actually hands one over. See stories/punch-list/prompts/drive_item.md.
#
# LIVE PROOF (twice) showed the free-text maker driving the target session does
# NOT reliably pass `initial_world` to `session.new` even when told to — it opens
# the session with an empty world and drives off the prompt text alone, so
# `world.ticket_id` stays empty and the target story can't self-provision. What
# the maker DOES reliably do is drive the prompt text verbatim as its first turn.
# So the prompt below LEADS with an explicit, verbatim-quotable ticket directive
# ("Work ticket <id> titled \"<title>\". <brief>") that both stories/implementation
# and stories/bugfix's `idle` rooms can extract via a semantic-router template
# (`work_ticket` intent, `{ticket_id}`/`{ticket_title}` captures — see their
# rooms/idle.yaml) even when initial_world never arrives. world_in stays as the
# belt-and-suspenders structured path for a future maker that DOES honor it.
#
# "titled" is a deliberate literal anchor: the router's `{ticket_id}` capture is
# a trailing run absorbing every token up to the next literal, so without an
# anchor right after the id it would swallow the rest of the message (title +
# brief) into ticket_id itself. "titled" bounds it cleanly. The `{ticket_title}`
# capture after it is best-effort only (it trails to end-of-input, so it also
# picks up the brief) — both target stories treat a noisy ticket_title as
# harmless: implementation's review_task_executing immediately re-fetches the
# authoritative title via iface.ticket.get, and bugfix only ever uses it as
# human-facing display text.
#
# stories/implementation additionally calls `iface.ticket.get {id}` (review_task
# room), which — unlike the append-only transport — does NOT self-bootstrap: the
# file-backed default handler (host.local_files.ticket) requires a REAL
# `<root>/issues/<kind>/<id>.md` record to already exist. So this script also
# writes a synthetic ticket file, scoped under the goal's own work_dir (never the
# project's real, now-frozen `issues/` archive — see issues/DEPRECATED.md) and
# points `world_in.tickets_root` at it so the target session's ticket.get resolves
# there via its `root` arg instead of falling back to cwd.

PIPELINE_STORY = {
    "bugfix": "stories/bugfix/app.yaml",
    "implementation": "stories/implementation/app.yaml",
    "docs": "stories/dev-story/app.yaml",
}


def _yaml_dq(s):
    # Minimal YAML double-quoted-scalar escaping — enough for a ticket title;
    # not a general YAML encoder.
    s = _str(s).replace("\\", "\\\\").replace("\"", "\\\"")
    return "\"" + s + "\""


def _str(v):
    if v == None:
        return ""
    return str(v)


def _ticket_body_markdown(change, gate_cmd):
    lines = []
    brief = _str(change.get("agent_brief")).strip()
    if brief != "":
        lines.append(brief)
        lines.append("")
    scope = change.get("scope") or []
    if len(scope) > 0:
        lines.append("## Scope")
        for s in scope:
            lines.append("- " + _str(s))
        lines.append("")
    acceptance = change.get("acceptance") or []
    if len(acceptance) > 0:
        lines.append("## Acceptance")
        for a in acceptance:
            lines.append("- " + _str(a))
        lines.append("")
    if gate_cmd != "":
        lines.append("## Gate")
        lines.append("`" + gate_cmd + "`")
        lines.append("")
    return "\n".join(lines)


def _write_ticket(ctx, tickets_root, change_id, title, change, gate_cmd):
    # issues/<kind>/<id>.md is the on-disk contract host.local_files.ticket
    # scans (findTicketPath); "features" is as good a bucket as any for a
    # generic dispatched change (ticket.get's `type` field is not branched on
    # by either target story).
    slug = change_id.replace("/", "_")
    ticket_path = tickets_root + "/issues/features/" + slug + ".md"
    doc = "---\n"
    doc += "title: " + _yaml_dq(title) + "\n"
    doc += "status: open\n"
    doc += "---\n\n"
    doc += "# " + title + "\n\n"
    doc += _ticket_body_markdown(change, gate_cmd)
    ctx.fs.write(ticket_path, doc)

    # The NL-extraction fallback (idle.yaml's `work_ticket` intent — see the
    # module docstring) captures ticket_id through the semantic router's
    # `{slot}` template mechanism, which lowercases captured string values
    # (internal/lex.Tokenize normalises + lowercases BEFORE the matcher ever
    # sees the text — see docs/architecture/semantic-routing.md §1.3's case-
    # folding caveat). change_id is frequently mixed-case (e.g. "WM.9"), so a
    # live NL-driven ticket_id would arrive lowercased ("wm.9") while this
    # exact-case file is "WM.9.md" — findTicketPath's os.Stat lookup is
    # case-sensitive on Linux (masked by APFS's case-insensitive default on
    # macOS dev boxes, which is why this wouldn't show up locally). Mirror
    # the ticket at the lowercased slug too so ticket.get resolves regardless
    # of which path (structured world_in, exact case; or NL extraction,
    # lowercased) produced world.ticket_id.
    lower_slug = slug.lower()
    if lower_slug != slug:
        ctx.fs.write(tickets_root + "/issues/features/" + lower_slug + ".md", doc)

    return ticket_path


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
    title = str(change.get("title") or change_id)
    agent_brief = str(change.get("agent_brief") or title or change_id)
    # Lead with the verbatim-parseable ticket directive (see the module
    # docstring): "titled" anchors the {ticket_id} template capture in the
    # target story's idle room so it doesn't swallow the rest of this prompt.
    action = "Work ticket " + change_id + " titled \"" + title + "\". " + agent_brief

    tickets_root = work_dir + "/tickets"
    ticket_path = _write_ticket(ctx, tickets_root, change_id, title, change, gate_cmd)

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
            "title": title,
            "priority": 1,
            "story": story,
            "mode": "drive",
            "prompt": action,
            "gate_command": gate_cmd,
            "verify": [{"kind": "command", "cmd": gate_cmd}],
            # Structured target-story inputs the driver forwards as `initial_world`
            # when it opens the target session (see stories/punch-list's
            # prompts/drive_item.md) — the seam that lets a change actually LAND
            # instead of being driven blind off free text alone. punch-list itself
            # never reads these keys; it only carries the item through.
            "ticket_id": change_id,
            "ticket_title": title,
            "world_in": {
                "ticket_id": change_id,
                "ticket_title": title,
                "tickets_root": tickets_root,
            },
        }],
    }

    slug = change_id.replace("/", "_")
    manifest_path = work_dir + "/manifests/" + slug + ".json"
    ctx.fs.write(manifest_path, json.encode(manifest) + "\n")
    return {"manifest_path": manifest_path, "change": change, "gate_cmd": gate_cmd, "ticket_path": ticket_path}
