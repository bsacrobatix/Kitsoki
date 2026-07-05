# build_report.star — fold leg_results into report.md under the run dir: the
# per-transport verdict table. Mirrors stories/fix-tests's write_report.star
# (markdown assembly + ctx.fs.write). leg_results is a templated INPUT (not
# read via ctx.world) so the engine's dispatch-time re-render of this
# invoke's `inputs:` picks up the very last leg's recording even when this
# room is entered in the same cascading turn as that leg's record_leg_result
# (see plan.yaml's header comment for the general gotcha).
#
# Interface (authoritative in build_report.star.yaml):
#   inputs:  run_id (string), run_dir (string, repo-relative),
#            scenario_ref (string), scenario_description (string),
#            leg_results (object {items:[...]})
#   outputs: report_path (string), report_summary (string)

def _str(v):
    if v == None:
        return ""
    return str(v)


def _items(v):
    if type(v) == "dict":
        return v.get("items", [])
    return []


def _escape_cell(text):
    out = ""
    for ch in _str(text).elems():
        if ch == "|":
            out += "\\|"
        elif ch == "\n":
            out += "<br>"
        else:
            out += ch
    return out


def _row(item):
    # evidence_level labels vscode legs "bridge-level" (the IDE bridge
    # stub/recording path, never a genuine editor) alongside tui/web's
    # "frame-level" -- see record_leg_result.star's _evidence_level(), which
    # is where this field is populated.
    return (
        "| " + _escape_cell(item.get("transport", "")) +
        " | " + _escape_cell(item.get("scenario", "")) +
        " | " + _escape_cell(item.get("evidence_level", "")) +
        " | " + _escape_cell(item.get("driver_status", "")) +
        " | " + _escape_cell(item.get("verdict", "")) +
        " | " + _escape_cell(item.get("verdict_summary", "")) +
        " |\n"
    )


def main(ctx):
    run_id = _str(ctx.inputs.get("run_id", ""))
    run_dir = _str(ctx.inputs.get("run_dir", ""))
    scenario_ref = _str(ctx.inputs.get("scenario_ref", ""))
    scenario_description = _str(ctx.inputs.get("scenario_description", ""))
    name = scenario_ref if scenario_ref != "" else scenario_description

    leg_results = ctx.inputs.get("leg_results") or {"items": []}
    items = _items(leg_results)

    passes = 0
    fails = 0
    degraded = 0
    for item in items:
        v = item.get("verdict", "")
        if v == "pass":
            passes += 1
        elif v == "degraded-evidence":
            degraded += 1
        elif v != "" and v != "unjudged":
            fails += 1

    lines = []
    lines.append("# Scenario QA report\n\n")
    lines.append("- Scenario: `" + name + "`\n")
    lines.append("- Run: `" + run_id + "`\n\n")
    lines.append("| Transport | Scenario | Level | Driver | Verdict | Notes |\n")
    lines.append("|---|---|---|---|---|---|\n")
    for item in items:
        lines.append(_row(item))

    summary = _str(passes) + " / " + _str(len(items)) + " transport legs passed"
    if fails > 0:
        summary += ", " + _str(fails) + " failed"
    if degraded > 0:
        summary += ", " + _str(degraded) + " degraded-evidence"
    summary += "."
    lines.append("\n" + summary + "\n")

    if run_dir != "":
        path = run_dir + "/report.md"
    else:
        path = ".artifacts/scenario-qa/" + (run_id if run_id != "" else "adhoc") + "/report.md"

    written = ctx.fs.write(path, "".join(lines))
    return {"report_path": written, "report_summary": summary}
