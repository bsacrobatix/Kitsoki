# build_deck.star — deterministically produce the Slidey report deck SPEC from
# the journaled marathon data. The script returns the JSON body and the artifact
# thread where reporting.yaml writes it through host.artifacts_dir. No LLM drafts
# the deck: agent output can appear only as already-recorded structured fields in
# results / rollup / findings.
#
# Interface (authoritative in build_deck.star.yaml):
#   inputs:  results (object), rollup (object), findings (object), out_path (string)
#   world:   deck_spec (object)
#   outputs: deck_spec (object {summary, body, artifact_thread, scenes_preview})

def main(ctx):
    out_path = ctx.inputs.get("out_path", "")
    rollup = ctx.inputs.get("rollup", {})
    results = ctx.inputs.get("results", {})
    findings = ctx.inputs.get("findings", {})

    counts = rollup.get("counts", {}) if type(rollup) == "dict" else {}
    totals = rollup.get("totals", {}) if type(rollup) == "dict" else {}
    worked = rollup.get("worked", []) if type(rollup) == "dict" else []
    didnt = rollup.get("didnt", []) if type(rollup) == "dict" else []
    headline = rollup.get("headline", "") if type(rollup) == "dict" else ""
    items = results.get("items", []) if type(results) == "dict" else []
    finding_items = findings.get("items", []) if type(findings) == "dict" else []

    processed = counts.get("processed", 0)
    solved = counts.get("solved", 0)
    partial = counts.get("partial", 0)
    failed = counts.get("failed", 0)
    skipped = counts.get("skipped", 0)
    needs_human = counts.get("needs_human", 0)

    summary = "Dogfood marathon report — %d case(s), %d solved." % (processed, solved)
    if headline == "":
        headline = "Processed %d case(s): %d solved, %d partial, %d failed." % (processed, solved, partial, failed)

    deck = {
        "version": "1",
        "title": "Dogfood marathon report",
        "theme": "kitsoki-report",
        "scenes": [
            {
                "type": "title",
                "eyebrow": "Kitsoki report",
                "title": "Dogfood marathon report",
                "subtitle": headline,
                "hold": 1800,
            },
            {
                "type": "objectives",
                "title": "Run status",
                "items": [
                    {"label": "Processed", "status": _status(processed > 0), "detail": "%d case(s)" % processed},
                    {"label": "Solved", "status": _status(solved > 0), "detail": "%d solved" % solved},
                    {"label": "Partial / needs-human", "status": _status((partial + needs_human) > 0), "detail": "%d partial, %d needs-human" % (partial, needs_human)},
                    {"label": "Failed", "status": _status(failed == 0), "detail": "%d failed" % failed},
                    {"label": "Skipped", "status": _status(skipped == 0), "detail": "%d skipped" % skipped},
                ],
            },
            {
                "type": "table",
                "title": "Cost and time",
                "columns": ["metric", "value"],
                "rows": [
                    {"metric": "Cost", "value": "$" + str(_num(totals.get("cost_usd", 0)))},
                    {"metric": "Tokens", "value": "%d" % _int(totals.get("tokens", 0))},
                    {"metric": "Wall time", "value": "%ds" % _int(totals.get("wall_s", 0))},
                ],
            },
            {
                "type": "table",
                "title": "Case outcomes",
                "columns": ["case", "exit", "verify", "trace"],
                "rows": _case_rows(items),
            },
            {
                "type": "evidence",
                "title": "Review artifacts",
                "items": _evidence(items),
            },
            {
                "type": "narrative",
                "eyebrow": "What worked",
                "lede": _join_or(worked, "No positive pattern recorded."),
                "body": "Derived from structured run rollup, not generated deck prose.",
            },
            {
                "type": "narrative",
                "eyebrow": "What did not",
                "lede": _join_or(didnt, "No friction recorded."),
                "body": "Findings should become generic workflow hardening, not case-specific overfitting.",
            },
            {
                "type": "table",
                "title": "Findings",
                "columns": ["finding", "case", "status"],
                "rows": _finding_rows(finding_items),
            },
        ],
    }
    body = json.encode(deck)
    suffix = _checksum(body)
    artifact_thread = "dogfood-marathon/%s/deck.slidey.json" % suffix
    if out_path != "":
        artifact_thread = out_path

    scenes_preview = [
        "title: Dogfood marathon report",
        "rollup: counts + cost/tokens/time totals",
        "outcomes: per-case triage / exit / independent-verify",
        "evidence: trace/worktree/deliverable artifact links",
        "what worked / what didn't",
        "findings → no-overfit hardening",
    ]

    return {
        "deck_spec": {
            "spec_path": "",
            "artifact_thread": artifact_thread,
            "body": body,
            "summary": summary,
            "scenes_preview": scenes_preview,
        },
    }

def _case_rows(items):
    rows = []
    for item in items:
        rows.append({
            "case": str(item.get("case_id", item.get("id", ""))),
            "exit": str(item.get("exit", "")),
            "verify": str(item.get("verify_status", item.get("status", ""))),
            "trace": str(item.get("trace", item.get("trace_path", ""))),
        })
    if len(rows) == 0:
        rows.append({"case": "(none)", "exit": "", "verify": "", "trace": ""})
    return rows

def _evidence(items):
    ev = []
    for item in items:
        case_id = str(item.get("case_id", item.get("id", "case")))
        trace = str(item.get("trace", item.get("trace_path", "")))
        worktree = str(item.get("worktree", ""))
        if trace != "":
            ev.append({"label": case_id + " trace", "status": "done", "detail": trace})
        if worktree != "":
            ev.append({"label": case_id + " worktree", "status": "done", "detail": worktree})
    if len(ev) == 0:
        ev.append({"label": "Run journal", "status": "pending", "detail": "No per-case evidence recorded."})
    return ev

def _finding_rows(items):
    rows = []
    for item in items:
        if type(item) == "dict":
            rows.append({
                "finding": str(item.get("summary", item.get("title", item.get("id", "")))),
                "case": str(item.get("case_id", "")),
                "status": str(item.get("status", "open")),
            })
        else:
            rows.append({"finding": str(item), "case": "", "status": "open"})
    if len(rows) == 0:
        rows.append({"finding": "No findings recorded.", "case": "", "status": "done"})
    return rows

def _join_or(items, fallback):
    if type(items) != "list" or len(items) == 0:
        return fallback
    parts = []
    for item in items:
        parts.append(str(item))
    return "; ".join(parts)

def _status(ok):
    return "done" if ok else "pending"

def _int(value):
    if type(value) == "int":
        return value
    if type(value) == "float":
        return int(value)
    return 0

def _num(value):
    if type(value) == "int" or type(value) == "float":
        return value
    return 0

def _checksum(value):
    total = 0
    for i in range(len(value)):
        total = (total + ((i + 1) * ord(value[i]))) % 4294967291
    return "run-" + str(total)
