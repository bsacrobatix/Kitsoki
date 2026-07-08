# persist_journal.star — write the durable JSON checkpoint plus a compact
# Markdown review copy for the dogfood marathon.

def _str(v):
    if v == None:
        return ""
    return str(v)

def _dict(v):
    if type(v) == "dict":
        return v
    return {}

def _items(v):
    if type(v) == "list":
        return v
    return []

def _int(v, default):
    if v == None or v == "":
        return default
    return int(v)

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

def _safe_run_id(raw):
    text = _str(raw).strip().lower()
    if text == "":
        text = "dogfood-marathon"
    out = ""
    last_dash = False
    for ch in text.elems():
        ok = (ch >= "a" and ch <= "z") or (ch >= "0" and ch <= "9")
        if ok:
            out += ch
            last_dash = False
        elif not last_dash:
            out += "-"
            last_dash = True
    if out == "":
        return "dogfood-marathon"
    return out

def _run_dir(ctx, run_id):
    supplied = _str(ctx.inputs.get("run_dir", "")).strip()
    if supplied != "":
        return supplied
    return ".artifacts/dogfood-marathon/" + run_id

def _journal_path(ctx, run_dir):
    supplied = _str(ctx.inputs.get("journal_path", "")).strip()
    if supplied != "":
        return supplied
    return run_dir + "/journal.json"

def _markdown_path(ctx, journal_path, run_dir):
    supplied = _str(ctx.inputs.get("journal_markdown_path", "")).strip()
    if supplied != "":
        return supplied
    if journal_path.endswith(".json"):
        return journal_path[:-5] + ".md"
    return run_dir + "/journal.md"

def _summary(stage, backlog, results, exceptions, case_index, cases_processed):
    total = len(_items(_dict(backlog).get("items", [])))
    recorded = len(_items(_dict(results).get("items", [])))
    serious = len(_items(_dict(exceptions).get("items", [])))
    return stage + ": " + str(recorded) + " recorded / " + str(total) + " loaded; next index " + str(case_index) + "; exceptions " + str(serious)

def _markdown(journal):
    backlog = _dict(journal.get("backlog", {}))
    results = _dict(journal.get("results", {}))
    findings = _dict(journal.get("findings", {}))
    exceptions = _dict(journal.get("exceptions", {}))
    rollup = _dict(journal.get("rollup", {}))
    deck = _dict(journal.get("deck_spec", {}))

    lines = []
    lines.append("# Dogfood marathon journal\n\n")
    lines.append("| | |\n|---|---|\n")
    lines.append("| Run | `" + _escape_cell(journal.get("run_id", "")) + "` |\n")
    lines.append("| Stage | `" + _escape_cell(journal.get("last_checkpoint", "")) + "` |\n")
    lines.append("| Backlog | " + str(len(_items(backlog.get("items", [])))) + " |\n")
    lines.append("| Case index | " + str(journal.get("case_index", 0)) + " |\n")
    lines.append("| Cases processed | " + str(journal.get("cases_processed", 0)) + " |\n")
    lines.append("| Results | " + str(len(_items(results.get("items", [])))) + " |\n")
    lines.append("| Findings | " + str(len(_items(findings.get("items", [])))) + " |\n")
    lines.append("| Serious exceptions | " + str(len(_items(exceptions.get("items", [])))) + " |\n")
    if deck.get("spec_path", ""):
        lines.append("| Aggregate deck | `" + _escape_cell(deck.get("spec_path", "")) + "` |\n")
    lines.append("\n")

    if len(_items(results.get("items", []))) > 0:
        lines.append("## Results\n\n")
        lines.append("| Case | Source | Exit | Verify | Cost | Tokens | Time |\n")
        lines.append("|---|---|---|---|---:|---:|---:|\n")
        for r in _items(results.get("items", [])):
            item = _dict(r)
            source = item.get("source_url", "") or item.get("source_path", "") or item.get("source_repo", "")
            lines.append("| `" + _escape_cell(item.get("case_id", "")) + "` | " + _escape_cell(source) + " | `" + _escape_cell(item.get("exit", "")) + "` | `" + _escape_cell(item.get("verify_status", "")) + "` | " + _escape_cell(item.get("cost_usd", 0)) + " | " + _escape_cell(item.get("tokens", 0)) + " | " + _escape_cell(item.get("wall_s", 0)) + " |\n")
        lines.append("\n")

    if len(_items(exceptions.get("items", []))) > 0:
        lines.append("## Serious Exceptions\n\n")
        for e in _items(exceptions.get("items", [])):
            item = _dict(e)
            lines.append("- `" + _escape_cell(item.get("case_id", "")) + "` " + _escape_cell(item.get("severity", "serious")) + ": " + _escape_cell(item.get("summary", "")) + "\n")
        lines.append("\n")

    headline = _str(rollup.get("headline", "")).strip()
    if headline != "":
        lines.append("## Rollup\n\n")
        lines.append(headline + "\n")
    return "".join(lines)

def main(ctx):
    run_id = _safe_run_id(ctx.inputs.get("run_id", ctx.world.get("run_id", "dogfood-marathon")))
    run_dir = _run_dir(ctx, run_id)
    journal_path = _journal_path(ctx, run_dir)
    journal_markdown_path = _markdown_path(ctx, journal_path, run_dir)
    stage = _str(ctx.inputs.get("stage", "checkpoint")).strip() or "checkpoint"

    backlog = _dict(ctx.inputs.get("backlog", ctx.world.get("backlog", {"items": []}))) or {"items": []}
    results = _dict(ctx.inputs.get("results", ctx.world.get("results", {"items": []}))) or {"items": []}
    findings = _dict(ctx.inputs.get("findings", ctx.world.get("findings", {"items": []}))) or {"items": []}
    exceptions = _dict(ctx.inputs.get("exceptions", ctx.world.get("exceptions", {"items": []}))) or {"items": []}
    rollup = _dict(ctx.inputs.get("rollup", ctx.world.get("rollup", {})))
    deck_spec = _dict(ctx.inputs.get("deck_spec", ctx.world.get("deck_spec", {})))
    current_case = _dict(ctx.inputs.get("current_case", ctx.world.get("current_case", {})))
    case_index = _int(ctx.inputs.get("case_index", ctx.world.get("case_index", 0)), 0)
    cases_processed = _int(ctx.inputs.get("cases_processed", ctx.world.get("cases_processed", 0)), 0)

    journal = {
        "schema": "dogfood-marathon.journal.v1",
        "run_id": run_id,
        "run_dir": run_dir,
        "journal_path": journal_path,
        "journal_markdown_path": journal_markdown_path,
        "last_checkpoint": stage,
        "backlog": backlog,
        "results": results,
        "findings": findings,
        "exceptions": exceptions,
        "rollup": rollup,
        "deck_spec": deck_spec,
        "current_case": current_case,
        "case_index": case_index,
        "cases_processed": cases_processed,
    }

    summary = _summary(stage, backlog, results, exceptions, case_index, cases_processed)
    ctx.fs.write(journal_path, json.encode(journal) + "\n")
    ctx.fs.write(journal_markdown_path, _markdown(journal))

    return {
        "run_id": run_id,
        "run_dir": run_dir,
        "journal_path": journal_path,
        "journal_markdown_path": journal_markdown_path,
        "journal_status": "written",
        "journal_summary": summary,
        "last_checkpoint": stage,
    }
