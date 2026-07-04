# report_deck.star - deterministic PM report/deck hierarchy for goal-seeker.
#
# Reads the current <work_dir>/ledger.json (written by gs_ledger) and
# <work_dir>/log.jsonl, then emits:
#   * <work_dir>/reports/exec-summary.md
#   * <work_dir>/reports/exec-summary.slidey.json
#   * <work_dir>/reports/changes/<change_id>.md
#   * <work_dir>/reports/changes/<change_id>.slidey.json
#
# This is intentionally a projection over the append-only log + folded ledger:
# no LLM, no shell, no rendering, and no hidden state.


def _str(v):
    if v == None:
        return ""
    return str(v)


def _slug(s):
    out = []
    for ch in _str(s).elems():
        if ch.isalnum() or ch == "-" or ch == "_":
            out.append(ch)
        else:
            out.append("_")
    if len(out) == 0:
        return "change"
    return "".join(out)


def _read_log(ctx, work_dir):
    path = work_dir + "/log.jsonl"
    if not ctx.fs.exists(path):
        return []
    out = []
    for line in ctx.fs.read(path).split("\n"):
        line = line.strip()
        if line == "":
            continue
        out.append(json.decode(line))
    return out


def _latest_log_for(log, change_id):
    latest = None
    latest_seq = -1
    for e in log:
        if _str(e.get("change_id")) != change_id:
            continue
        seq = e.get("seq", 0)
        if seq >= latest_seq:
            latest = e
            latest_seq = seq
    return latest


def _goal_title(ctx, goal_dir, fallback):
    path = goal_dir.rstrip("/") + "/GOAL.md"
    if not ctx.fs.exists(path):
        return fallback
    for line in ctx.fs.read(path).split("\n"):
        stripped = line.strip()
        if stripped.startswith("#"):
            return stripped.lstrip("#").strip()
    return fallback


def _status_table(changes):
    lines = []
    lines.append("| Change | State | Gate | SHA | Title |")
    lines.append("|---|---|---|---|---|")
    for c in changes:
        lines.append("| " + _str(c.get("change_id")) + " | " + _str(c.get("state")) + " | " +
                     _str(c.get("gate_status")) + " | " + _str(c.get("sha") or "") + " | " +
                     _str(c.get("title")) + " |")
    return "\n".join(lines)


def _change_markdown(goal_title, change, log_entry, deck_path):
    cid = _str(change.get("change_id"))
    lines = []
    lines.append("# Change " + cid + " - " + _str(change.get("title")))
    lines.append("")
    lines.append("- Goal: " + goal_title)
    lines.append("- State: `" + _str(change.get("state")) + "`")
    lines.append("- Gate: `" + _str(change.get("gate_status")) + "`")
    lines.append("- SHA: `" + _str(change.get("sha") or "") + "`")
    lines.append("- Deck: `" + deck_path + "`")
    lines.append("")
    lines.append("## Scope")
    scopes = change.get("scope_globs") or []
    if len(scopes) == 0:
        lines.append("(none)")
    for s in scopes:
        lines.append("- `" + _str(s) + "`")
    lines.append("")
    lines.append("## Evidence")
    if log_entry == None:
        lines.append("- No log entry was found for this integrated change.")
    else:
        lines.append("- Log seq: `" + _str(log_entry.get("seq")) + "`")
        lines.append("- Actor: `" + _str(log_entry.get("actor")) + "`")
        lines.append("- Summary: " + _str(log_entry.get("summary")))
    return "\n".join(lines) + "\n"


def _summary_markdown(goal_title, ledger, integrated, change_paths):
    counts = ledger.get("counts") or {}
    lines = []
    lines.append("# " + goal_title + " - execution summary")
    lines.append("")
    lines.append("- Goal slug: `" + _str(ledger.get("goal_slug")) + "`")
    lines.append("- Remaining: `" + _str(ledger.get("remaining")) + "`")
    lines.append("- Integrated: `" + _str(counts.get("integrated", 0)) + "`")
    lines.append("- Ready: `" + _str(counts.get("ready", 0)) + "`")
    lines.append("- Blocked: `" + _str(counts.get("blocked", 0)) + "`")
    lines.append("- Parked: `" + _str(counts.get("parked", 0)) + "`")
    lines.append("")
    lines.append("## Status")
    lines.append(_status_table(ledger.get("changes") or []))
    lines.append("")
    lines.append("## Integrated change decks")
    if len(integrated) == 0:
        lines.append("(none yet)")
    for c in integrated:
        cid = _str(c.get("change_id"))
        p = change_paths.get(cid) or {}
        lines.append("- " + cid + ": `" + _str(p.get("deck")) + "` (`" + _str(p.get("markdown")) + "`)")
    return "\n".join(lines) + "\n"


def _exec_deck(goal_title, ledger, summary_path, integrated, change_paths):
    counts = ledger.get("counts") or {}
    rows = []
    for c in ledger.get("changes") or []:
        rows.append({
            "Change": _str(c.get("change_id")),
            "State": _str(c.get("state")),
            "Gate": _str(c.get("gate_status")),
            "SHA": _str(c.get("sha") or ""),
        })
    links = []
    for c in integrated:
        cid = _str(c.get("change_id"))
        p = change_paths.get(cid) or {}
        links.append(cid + ": " + _str(p.get("deck")))
    return {
        "meta": {
            "title": goal_title + " execution summary",
            "mode": "report",
            "source": "goal-seeker",
        },
        "scenes": [
            {
                "type": "title",
                "title": goal_title,
                "subtitle": "Goal-seeker execution summary",
            },
            {
                "type": "cards",
                "title": "Current ledger",
                "cards": [
                    {"title": "Integrated", "body": _str(counts.get("integrated", 0))},
                    {"title": "Remaining", "body": _str(ledger.get("remaining"))},
                    {"title": "Ready", "body": _str(counts.get("ready", 0))},
                    {"title": "Parked", "body": _str(counts.get("parked", 0))},
                ],
            },
            {
                "type": "table",
                "title": "Change status",
                "rows": rows,
            },
            {
                "type": "narrative",
                "eyebrow": "Artifacts",
                "body": "Summary: " + summary_path,
                "lede": "\n".join(links),
            },
        ],
    }


def _change_deck(goal_title, change, markdown_path, log_entry):
    cid = _str(change.get("change_id"))
    evidence = "No log entry found."
    if log_entry != None:
        evidence = "seq " + _str(log_entry.get("seq")) + " by " + _str(log_entry.get("actor")) + ": " + _str(log_entry.get("summary"))
    return {
        "meta": {
            "title": "Change " + cid + ": " + _str(change.get("title")),
            "mode": "report",
            "source": "goal-seeker",
        },
        "scenes": [
            {
                "type": "title",
                "title": "Change " + cid,
                "subtitle": _str(change.get("title")),
            },
            {
                "type": "narrative",
                "eyebrow": goal_title,
                "body": "State " + _str(change.get("state")) + " with gate " + _str(change.get("gate_status")),
                "lede": evidence,
            },
            {
                "type": "narrative",
                "eyebrow": "Artifact",
                "body": markdown_path,
            },
        ],
    }


def main(ctx):
    goal_slug = _str(ctx.inputs["goal_slug"]).strip()
    goal_dir = ctx.inputs["goal_dir"].rstrip("/")
    work_dir = ctx.inputs["work_dir"].rstrip("/")
    ledger_path = work_dir + "/ledger.json"
    if not ctx.fs.exists(ledger_path):
        fail("report_deck: no ledger.json at " + ledger_path + " (run gs_ledger first)")

    ledger = json.decode(ctx.fs.read(ledger_path))
    goal_title = _goal_title(ctx, goal_dir, goal_slug if goal_slug != "" else "goal")
    report_dir = work_dir + "/reports"
    changes_dir = report_dir + "/changes"
    summary_path = report_dir + "/exec-summary.md"
    deck_path = report_dir + "/exec-summary.slidey.json"

    log = _read_log(ctx, work_dir)
    integrated = []
    for c in ledger.get("changes") or []:
        if c.get("state") == "integrated":
            integrated.append(c)

    change_paths = {}
    for c in integrated:
        cid = _str(c.get("change_id"))
        slug = _slug(cid)
        md_path = changes_dir + "/" + slug + ".md"
        change_deck_path = changes_dir + "/" + slug + ".slidey.json"
        entry = _latest_log_for(log, cid)
        change_paths[cid] = {"markdown": md_path, "deck": change_deck_path}
        ctx.fs.write(md_path, _change_markdown(goal_title, c, entry, change_deck_path))
        ctx.fs.write(change_deck_path, json.encode(_change_deck(goal_title, c, md_path, entry)) + "\n")

    ctx.fs.write(summary_path, _summary_markdown(goal_title, ledger, integrated, change_paths))
    ctx.fs.write(deck_path, json.encode(_exec_deck(goal_title, ledger, summary_path, integrated, change_paths)) + "\n")

    return {
        "status": "written",
        "summary_path": summary_path,
        "deck_path": deck_path,
        "change_count": len(integrated),
        "change_paths": change_paths,
    }
