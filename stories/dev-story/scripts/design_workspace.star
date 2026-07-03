WORKSPACE_ROOT = "docs/proposals/.workspace"
PROPOSALS_DIR = "docs/proposals"
MAX_SLUG_WORDS = 6

STOPWORDS = {
    "a": True,
    "able": True,
    "add": True,
    "allow": True,
    "an": True,
    "and": True,
    "as": True,
    "at": True,
    "author": True,
    "be": True,
    "called": True,
    "claude": True,
    "can": True,
    "could": True,
    "create": True,
    "design": True,
    "docs": True,
    "feature": True,
    "for": True,
    "from": True,
    "i": True,
    "in": True,
    "introduced": True,
    "is": True,
    "it": True,
    "kitsoki": True,
    "let": True,
    "make": True,
    "md": True,
    "of": True,
    "on": True,
    "or": True,
    "per": True,
    "prd": True,
    "recently": True,
    "realize": True,
    "runtime": True,
    "should": True,
    "session": True,
    "style": True,
    "that": True,
    "the": True,
    "this": True,
    "to": True,
    "user": True,
    "where": True,
    "with": True,
    "would": True,
    "want": True,
    "writes": True,
    "virtual": True,
}


def _is_slug_char(ch):
    return ("a" <= ch and ch <= "z") or ("0" <= ch and ch <= "9")


def _words(text):
    out = []
    cur = ""
    for ch in str(text or "").strip().lower().elems():
        if _is_slug_char(ch):
            cur += ch
        elif cur != "":
            out.append(cur)
            cur = ""
    if cur != "":
        out.append(cur)
    return out


def _singular(word):
    if len(word) > 3 and word.endswith("s"):
        return word[:-1]
    return word


def _sanitize(text):
    raw_words = _words(text)
    words = []
    for word in raw_words:
        if word not in STOPWORDS:
            words.append(word)
    if len(words) == 0:
        words = raw_words

    deduped = []
    seen = {}
    for word in words:
        key = _singular(word)
        if key in seen:
            continue
        seen[key] = True
        deduped.append(key)
        if len(deduped) >= MAX_SLUG_WORDS:
            break
    if len(deduped) == 0:
        return "proposal"
    return "-".join(deduped)


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
    proposed = ctx.inputs.get("proposed", "")
    slug = _unique_slug(ctx, _sanitize(proposed))
    return {"slug": slug, "workspace": _workspace(slug)}
