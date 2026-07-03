WORKSPACE_ROOT = "docs/proposals/.workspace"
PROPOSALS_DIR = "docs/proposals"
MAX_SLUG_WORDS = 6


def _is_slug_char(ch):
    return ("a" <= ch and ch <= "z") or ("0" <= ch and ch <= "9")


def _sanitize(text):
    parts = []
    cur = ""
    for ch in str(text or "").strip().lower().elems():
        if _is_slug_char(ch):
            cur += ch
        elif cur != "":
            parts.append(cur)
            cur = ""
    if cur != "":
        parts.append(cur)
    if len(parts) > MAX_SLUG_WORDS:
        parts = parts[:MAX_SLUG_WORDS]
    if len(parts) == 0:
        return "proposal"
    return "-".join(parts)


def _rstrip_slash(path):
    out = str(path or "")
    for _ in range(len(out)):
        if out.endswith("/"):
            out = out[:-1]
        else:
            break
    return out


def _basename(path):
    parts = str(path or "").split("/")
    if len(parts) == 0:
        return ""
    return parts[-1]


def _stem(path):
    base = _basename(path)
    if base.endswith(".md"):
        return base[:-3]
    return base


def _is_tmp_path(path):
    path = str(path or "")
    return path == "/tmp" or path.startswith("/tmp/")


def _workspace(slug):
    return WORKSPACE_ROOT + "/" + slug


def _published(slug):
    return PROPOSALS_DIR + "/" + slug + ".md"


def _taken(ctx, slug):
    return ctx.fs.exists(_workspace(slug)) or ctx.fs.exists(_published(slug))


def _unique_slug(ctx, base):
    if not _taken(ctx, base):
        return base
    for i in range(2, 1000):
        candidate = base + "-" + str(i)
        if not _taken(ctx, candidate):
            return candidate
    fail("too many collisions for slug: " + base)


def main(ctx):
    target = ctx.inputs.get("target", "")
    proposed_slug = ctx.inputs.get("proposed_slug", "")
    normalized = _rstrip_slash(target)

    if _is_tmp_path(normalized):
        slug = _sanitize(_stem(normalized))
        return {"slug": slug, "workspace": _workspace(slug)}

    if str(target or "").endswith("/") or (normalized != "" and not normalized.endswith(".md")):
        slug = _sanitize(_basename(normalized))
        return {"slug": slug, "workspace": target}

    base = proposed_slug or _stem(normalized)
    slug = _unique_slug(ctx, _sanitize(base))
    return {"slug": slug, "workspace": _workspace(slug)}
