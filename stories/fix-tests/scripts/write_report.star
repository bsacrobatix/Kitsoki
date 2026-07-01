def _str(v):
    if v == None:
        return ""
    return str(v)


def _bool(v):
    if v == True:
        return True
    if type(v) == "string":
        lowered = v.lower()
        return lowered == "true" or lowered == "1" or lowered == "yes"
    return False


def _items(v):
    if type(v) == "list":
        return v
    return []


def _artifact(v):
    if type(v) == "dict":
        return v
    return {}


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


def _bullets(items, empty = "(none)"):
    vals = []
    for item in _items(items):
        text = _str(item).strip()
        if text:
            vals.append(text)
    if len(vals) == 0:
        return "- " + empty + "\n"
    out = ""
    for text in vals:
        out += "- " + text + "\n"
    return out


def _headline(outcome):
    if outcome == "clean":
        return "All tests passed on the first run; nothing to fix."
    if outcome == "fixed":
        return "Tests are green after auto-fixing."
    if outcome == "exhausted":
        return "Tests still failing after the fix budget was exhausted."
    if outcome == "blocked":
        return "Blocked; the fixer needs a human decision."
    return "Outcome: " + outcome


def _latest_log(ctx):
    matches = ctx.fs.glob(".artifacts/test-reports/test-*.log")
    if len(matches) == 0:
        return ""
    return matches[len(matches) - 1]


def _render(ctx, outcome, cycle, max_cycles, artifact, tests_passed):
    lines = []
    lines.append("# fix-tests report\n\n")
    lines.append("**" + _headline(outcome) + "**\n\n")
    lines.append("| | |\n|---|---|\n")
    lines.append("| Outcome | `" + _escape_cell(outcome) + "` |\n")
    lines.append("| Final status | " + ("green" if tests_passed else "red") + " |\n")
    lines.append("| Fix cycles used | " + _str(cycle) + " / " + _str(max_cycles) + " |\n")
    test_log = _latest_log(ctx)
    if test_log:
        lines.append("| Full test log | `" + _escape_cell(test_log) + "` |\n")
    lines.append("\n")

    if artifact:
        lines.append("## What the fixer did (final cycle)\n\n")
        title = _str(artifact.get("summary_title", "")).strip()
        if title:
            lines.append("**" + title + "**\n\n")
        summary = _str(artifact.get("summary_markdown", "")).strip()
        if summary:
            lines.append(summary + "\n\n")
        lines.append("### Files changed\n\n")
        lines.append(_bullets(artifact.get("files_changed", []), "(no files changed)"))
        lines.append("\n### Tests fixed\n\n")
        lines.append(_bullets(artifact.get("fixed_tests", [])))
        lines.append("\n### Remaining failures\n\n")
        lines.append(_bullets(artifact.get("remaining_failures", [])))
        open_questions = artifact.get("open_questions", [])
        if len(_items(open_questions)) > 0:
            lines.append("\n## Open questions (need a human answer)\n\n")
            lines.append(_bullets(open_questions))
        lines.append("\n")
    elif outcome != "clean":
        lines.append("## What the fixer did\n\n")
        lines.append("No fixer artifact was recorded; the fixer or a test run errored before producing one. See the full test log above.\n\n")

    if outcome == "exhausted" or outcome == "blocked":
        lines.append("---\n\nRe-run `make fix-tests` after resolving the above.\n")

    return "".join(lines)


def main(ctx):
    outcome = _str(ctx.inputs.get("outcome", "unknown")) or "unknown"
    cycle = ctx.inputs.get("cycle", 0)
    max_cycles = ctx.inputs.get("max_cycles", 0)
    artifact = _artifact(ctx.inputs.get("fix_artifact", {}))
    tests_passed = _bool(ctx.inputs.get("tests_passed", False))

    path = ".artifacts/fix-tests/report-" + outcome + ".md"
    written = ctx.fs.write(path, _render(ctx, outcome, cycle, max_cycles, artifact, tests_passed))
    return {"report_path": written}
