# build_deck.star — write one aggregate slidey deck plus a per-case deck for
# every recorded bug. The decks are derived only from journaled run data.

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

def _safe_slug(raw, fallback):
    text = _str(raw).strip().lower()
    if text == "":
        text = fallback
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
        return fallback
    return out

def _run_id(ctx):
    supplied = _str(ctx.inputs.get("run_id", ctx.world.get("run_id", ""))).strip()
    return _safe_slug(supplied, "dogfood-marathon")

def _run_dir(ctx, run_id):
    supplied = _str(ctx.inputs.get("run_dir", ctx.world.get("run_dir", ""))).strip()
    if supplied != "":
        return supplied
    return ".artifacts/dogfood-marathon/" + run_id

def _out_path(ctx, run_dir):
    supplied = _str(ctx.inputs.get("out_path", "")).strip()
    if supplied != "":
        return supplied
    return run_dir + "/decks/aggregate.slidey.json"

def _card(label, sub, style):
    return {"label": _str(label), "sub": _str(sub), "style": style}

def _join(items, empty):
    vals = []
    for item in _items(items):
        text = _str(item).strip()
        if text != "":
            vals.append(text)
    if len(vals) == 0:
        return empty
    return "\n".join(vals)

def _source(record):
    return record.get("source_url", "") or record.get("source_path", "") or record.get("source_repo", "") or record.get("source_kind", "")

def _case_cards(record):
    return [
        _card("triage", record.get("triage", ""), "default"),
        _card("exit", record.get("exit", ""), "primary" if record.get("exit", "") == "shipped" else "secondary"),
        _card("verify", record.get("verify_status", ""), "primary" if record.get("verify_status", "") == "solved" else "secondary"),
        _card("cost", _str(record.get("cost_usd", 0)) + " USD", "default"),
        _card("tokens", record.get("tokens", 0), "default"),
        _card("time", _str(record.get("wall_s", 0)) + "s", "default"),
        _card("trace", record.get("trace", ""), "default"),
        _card("fix ref", record.get("fix_ref", ""), "default"),
    ]

def _case_deck(record, run_id):
    case_id = _str(record.get("case_id", "case"))
    title = _str(record.get("title", case_id))
    source = _source(record)
    body = _str(record.get("summary", "")).strip()
    if body == "":
        body = _str(record.get("verify_how", "")).strip()
    if body == "":
        body = "No case summary was recorded."
    return {
        "meta": {
            "title": "Dogfood bug " + case_id,
            "resolution": {"width": 1920, "height": 1080},
            "theme": "rose-pine-moon",
        },
        "scenes": [
            {
                "type": "title",
                "eyebrow": "Dogfood marathon · " + run_id,
                "title": case_id,
                "subtitle": title,
                "narration": "Bug " + case_id + " from the dogfood marathon.",
            },
            {
                "type": "cards",
                "variant": "grid",
                "title": "Outcome",
                "narration": "Recorded outcome, verification, cost, and trace.",
                "cards": _case_cards(record),
            },
            {
                "type": "narrative",
                "eyebrow": "Source and details",
                "lede": source,
                "body": body,
                "narration": "Source details and recorded summary for " + case_id + ".",
            },
        ],
    }

def _aggregate_deck(run_id, rollup, results, findings, exceptions, case_decks):
    counts = _dict(rollup.get("counts", {}))
    totals = _dict(rollup.get("totals", {}))
    scenes = [
        {
            "type": "title",
            "eyebrow": "Dogfood marathon",
            "title": run_id,
            "subtitle": _str(rollup.get("headline", "")),
            "narration": "A dogfood marathon over local and GitHub bugs.",
        },
        {
            "type": "cards",
            "variant": "grid",
            "title": "Run rollup",
            "narration": "Counts and totals rolled up across every processed case.",
            "cards": [
                _card("processed", counts.get("processed", 0), "default"),
                _card("solved", counts.get("solved", 0), "primary"),
                _card("partial", counts.get("partial", 0), "secondary"),
                _card("failed", counts.get("failed", 0), "secondary"),
                _card("exceptions", counts.get("exceptions", 0), "secondary"),
                _card("cost", _str(totals.get("cost_usd", 0)) + " USD", "default"),
                _card("tokens", totals.get("tokens", 0), "default"),
                _card("time", _str(totals.get("wall_s", 0)) + "s", "default"),
            ],
        },
        {
            "type": "narrative",
            "eyebrow": "What worked",
            "lede": "Evidence that held up.",
            "body": _join(rollup.get("worked", []), "No positive patterns recorded."),
            "narration": "What worked across this run.",
        },
        {
            "type": "narrative",
            "eyebrow": "What did not",
            "lede": "Friction and serious questions.",
            "body": _join(rollup.get("didnt", []), "No failures or serious exceptions recorded."),
            "narration": "Friction and serious exceptions captured for follow-up.",
        },
    ]

    for record in _items(results.get("items", [])):
        item = _dict(record)
        scenes.append({
            "type": "cards",
            "variant": "grid",
            "title": _str(item.get("case_id", "")) + " · " + _str(item.get("title", "")),
            "narration": "Per-case outcome for " + _str(item.get("case_id", "")) + ".",
            "cards": _case_cards(item),
        })

    scenes.append({
        "type": "narrative",
        "eyebrow": "Findings",
        "lede": str(len(_items(findings.get("items", [])))) + " finding(s)",
        "body": "Consequential findings are carried in the journal and should become generic hardening work.",
        "narration": "Findings from the run feed general hardening, not one-off overfitting.",
    })
    scenes.append({
        "type": "narrative",
        "eyebrow": "Per-bug decks",
        "lede": str(len(case_decks)) + " deck(s)",
        "body": _join([deck.get("path", "") for deck in case_decks], "No per-bug decks were generated."),
        "narration": "Each bug also has its own focused deck.",
    })

    return {
        "meta": {
            "title": "Dogfood marathon " + run_id,
            "resolution": {"width": 1920, "height": 1080},
            "theme": "rose-pine-moon",
        },
        "scenes": scenes,
    }

def main(ctx):
    run_id = _run_id(ctx)
    run_dir = _run_dir(ctx, run_id)
    out_path = _out_path(ctx, run_dir)
    results = _dict(ctx.inputs.get("results", {}))
    rollup = _dict(ctx.inputs.get("rollup", {}))
    findings = _dict(ctx.inputs.get("findings", {}))
    exceptions = _dict(ctx.inputs.get("exceptions", {}))

    case_decks = []
    for record in _items(results.get("items", [])):
        item = _dict(record)
        slug = _safe_slug(item.get("case_id", ""), "case-" + str(len(case_decks) + 1))
        path = run_dir + "/decks/" + slug + ".slidey.json"
        spec = _case_deck(item, run_id)
        ctx.fs.write(path, json.encode(spec) + "\n")
        case_decks.append({
            "case_id": item.get("case_id", ""),
            "title": item.get("title", ""),
            "path": path,
            "source": _source(item),
        })

    aggregate = _aggregate_deck(run_id, rollup, results, findings, exceptions, case_decks)
    ctx.fs.write(out_path, json.encode(aggregate) + "\n")

    counts = _dict(rollup.get("counts", {}))
    processed = counts.get("processed", len(_items(results.get("items", []))))
    solved = counts.get("solved", 0)
    summary = "Dogfood marathon " + run_id + " - " + str(processed) + " case(s), " + str(solved) + " solved, " + str(len(case_decks)) + " per-bug deck(s)."

    return {
        "deck_spec": {
            "spec_path": out_path,
            "summary": summary,
            "case_decks": case_decks,
            "scenes_preview": [
                "title: " + run_id,
                "rollup: counts + cost/tokens/time",
                "outcomes: per-case cards",
                "findings and serious exceptions",
                "per-bug deck index",
            ],
        },
    }
