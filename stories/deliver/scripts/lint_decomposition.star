def _fail(msg):
    return {"route": "fail", "error": msg}


def _ok():
    return {"route": "ok", "error": ""}


def _text(v):
    if v == None:
        return ""
    return str(v).strip()


def _deps(b):
    deps = b.get("deps")
    if deps == None:
        deps = b.get("depends_on")
    if deps == None:
        return []
    return deps


def _has_cycle(ids_order, adj):
    remaining = {}
    for bid in ids_order:
        remaining[bid] = adj[bid]

    for _ in range(len(ids_order)):
        progressed = False
        next_remaining = {}
        for bid in ids_order:
            if bid not in remaining:
                continue
            blocked = False
            for dep in remaining[bid]:
                if dep in remaining:
                    blocked = True
            if blocked:
                next_remaining[bid] = remaining[bid]
            else:
                progressed = True
        remaining = next_remaining
        if len(remaining) == 0:
            return ""
        if not progressed:
            break

    for bid in ids_order:
        if bid in remaining:
            return bid
    return ""


def main(ctx):
    path = ctx.inputs["decomposition_path"]
    if path == "":
        return _fail("usage: lint_decomposition.star <path>")

    doc = yaml.decode(ctx.fs.read(path))
    if type(doc) != "dict":
        return _fail("decomposition file must be a YAML/JSON object, got %s" % type(doc))

    briefs = doc.get("briefs") or []
    if not briefs:
        return _fail("decomposition has no briefs (top-level 'briefs' list is empty or missing)")

    ids = {}
    ids_order = []
    for i, b in enumerate(briefs):
        if type(b) != "dict":
            return _fail("brief at index %d is not an object" % i)
        bid = _text(b.get("id"))
        if bid == "":
            return _fail("brief at index %d has empty or missing 'id'" % i)
        if bid in ids:
            return _fail("duplicate brief id '%s' (index %d duplicates index %d)" % (bid, i, ids[bid]))
        ids[bid] = i
        ids_order.append(bid)

    adj = {}
    for b in briefs:
        bid = _text(b.get("id"))

        brief_text = _text(b.get("brief") or b.get("agent_brief") or "")
        if brief_text == "":
            return _fail("brief '%s' has empty 'brief' (and empty 'agent_brief') field" % bid)

        gate = _text(b.get("gate_command") or b.get("test_plan") or "")
        if gate == "":
            return _fail("brief '%s' has empty 'gate_command' (and empty 'test_plan') field" % bid)

        deps = _deps(b)
        for dep in deps:
            if dep not in ids:
                return _fail("brief '%s' has dep '%s' which is not a known brief id" % (bid, dep))
        adj[bid] = deps

    cycle = _has_cycle(ids_order, adj)
    if cycle != "":
        return _fail("dependency cycle detected involving '%s'" % cycle)

    return _ok()
