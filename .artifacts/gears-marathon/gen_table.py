#!/usr/bin/env python3
"""Deterministic marathon status table.

Reads cases.yaml + attempts.jsonl (append-only) and regenerates STATUS.md.
No LLM calls, no network. Re-runnable: same inputs -> byte-identical output.

attempts.jsonl record shape (one JSON object per line):
  {"bug","candidate","treatment","baseline_red","drive_exit","verify",
   "tokens","cost_usd","wall_s","trace","notes"}
  verify  : "PASS" | "FAIL" | "PENDING"
The LATEST record per (bug,candidate,treatment) wins; ordering is by file order.
"""
import json, os, sys

HERE = os.path.dirname(os.path.abspath(__file__))

def load_cases():
    # tiny hand parser (no yaml dep): collect id/title/package/fix_sha/baseline_sha
    cases, cur = [], None
    for line in open(os.path.join(HERE, "cases.yaml")):
        s = line.strip()
        if s.startswith("- id:"):
            if cur: cases.append(cur)
            cur = {"id": s.split("- id:")[1].strip()}
        elif cur is not None and ":" in s and not s.startswith("#"):
            k = s.split(":", 1)[0].strip()
            v = s.split(":", 1)[1]
            if " #" in v:            # strip inline comment
                v = v.split(" #", 1)[0]
            v = v.strip().strip('"')
            if k in ("title", "package", "fix_sha", "baseline_sha", "confirmed_red"):
                cur[k] = v
    if cur: cases.append(cur)
    return cases

def load_attempts():
    p = os.path.join(HERE, "attempts.jsonl")
    # latest attempt per bug, by file order (append-only journal).
    latest = {}
    if os.path.exists(p):
        for line in open(p):
            line = line.strip()
            if not line:
                continue
            r = json.loads(line)
            latest[r["bug"]] = r
    return latest

def main():
    cases = load_cases()
    latest = load_attempts()
    shipped = sum(1 for r in latest.values() if r.get("verify") == "PASS")
    rows = []
    for c in cases:
        bug = c["id"]
        r = latest.get(bug)
        red = c.get("confirmed_red", "?")
        if r:
            verify = r.get("verify", "PENDING")
            cand = r.get("candidate", "")
            tok = r.get("tokens", "")
            cost = r.get("cost_usd", "")
            wall = r.get("wall_s", "")
            exitr = r.get("drive_exit", "")
            notes = r.get("notes", "")
        else:
            verify = cand = tok = cost = wall = exitr = notes = ""
        rows.append((bug, c.get("title", "")[:54], c.get("fix_sha", ""), red,
                     cand, exitr, verify, tok, cost, wall, notes[:40]))
    out = []
    out.append("# gears-rust bugfix marathon — status\n")
    out.append(f"**Shipped (independent-verify PASS): {shipped} / 10**\n")
    out.append("Generated deterministically by `gen_table.py` from `cases.yaml` + `attempts.jsonl`.\n")
    hdr = ["bug", "title", "fix_sha", "RED?", "cand", "exit", "verify",
           "tokens", "cost$", "wall_s", "notes"]
    out.append("| " + " | ".join(hdr) + " |")
    out.append("|" + "|".join(["---"] * len(hdr)) + "|")
    for row in rows:
        out.append("| " + " | ".join(str(x) for x in row) + " |")
    open(os.path.join(HERE, "STATUS.md"), "w").write("\n".join(out) + "\n")
    print("\n".join(out))
    emit_deck(cases, latest, shipped)

def emit_deck(cases, latest, shipped):
    """Deterministic slidey-deck SOURCE (markdown), one slide per bug + summary.
    Rendered to HTML/MP4 by the slidey pipeline; regenerates with zero re-spend."""
    d = []
    d.append("# gears-rust bugfix marathon\n")
    d.append("## Fully-autonomous kitsoki dev-story over real merged-fix baselines\n")
    d.append(f"**{shipped} / 10 bugs shipped** — each independently verified against "
             "the real PR's hidden regression-test oracle.\n")
    d.append("---\n")
    d.append("## Method\n")
    d.append("- Baseline = the real fix's PARENT commit; bug confirmed RED there.\n"
             "- Drive `stories/bugfix` LIVE via the kitsoki studio MCP "
             "(`kitsoki-mcp-driver`); zero human edits.\n"
             "- Independent verify: the real PR's regression test (HIDDEN from the maker) "
             "must turn GREEN.\n")
    d.append("---\n")
    for c in cases:
        r = latest.get(c["id"])
        if not r:
            continue
        v = r.get("verify", "PENDING")
        badge = {"PASS": "✅ SHIPPED", "FAIL": "❌ FAILED"}.get(v, "… in progress")
        d.append(f"## {c['id']} — {badge}\n")
        d.append(f"**{c.get('title','')}**\n")
        d.append(f"- baseline RED: `{c.get('confirmed_red','?')}`  ·  "
                 f"candidate: `{r.get('candidate','')}`  ·  exit: `{r.get('drive_exit','')}`\n")
        d.append(f"- fix: `{r.get('fix_sha','')}`  ·  tokens: `{r.get('tokens','')}`  ·  "
                 f"wall: `{r.get('wall_s','')}s`\n")
        d.append(f"- {r.get('notes','')}\n")
        d.append("---\n")
    open(os.path.join(HERE, "slidey", "deck.md"), "w").write("\n".join(d) + "\n")

if __name__ == "__main__":
    main()
