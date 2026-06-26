#!/usr/bin/env python3
"""Render an aggregated session-mining report into an actionable brief.

    python3 report.py combined.json [--vocab vocab/core.yaml] [--top N] > BRIEF.md

The aggregate (aggregate.py output) is a ranked *diagnostic*: it tells you which
recurring procedures are most worth automating. This turns that into a
*prescriptive* brief — for each top candidate it states the verdict, the ladder
move, the gates to install (the judgment to preserve), the mechanical skeleton to
script, the evidence, and a concrete first step. Everything below the cut line is
still listed, so nothing is hidden.

Stdlib only. The vocab file is read only to look up each pattern's name,
one-line definition, and default ladder target ("the move"); it is optional.

What the verdict means (thresholds are explicit so two runs are comparable):
  BUILD NOW            high priority, corroborated by >=PROMOTE_MIN contributors,
                       and a crisp gate set (<= CRISP_GATES decisions)
  BUILD (judgment)     high priority + corroborated, but many decision points —
                       worth a story, but keep a model/human at the gates (low ladder target)
  PROMISING            high priority but only one contributor — needs corroboration
  ALREADY MOSTLY SOLVED  very mechanical + painless — low payoff to formalize further
  LATER                everything else, by priority
"""
import argparse
import json
import os
import re
import subprocess
import sys
from pathlib import Path

PRIORITY_CUT = 0.50      # at/above this, a pattern is a "build" candidate
CRISP_GATES = 4          # <= this many (unioned across contributors) reads as a clean gate set
SOLVED_MECH = 0.90       # mechanical_fraction at/above which, with low pain, ROI to formalize is low
PAIN_MARK = {"high": "🔴", "med": "🟠", "low": "⚪"}
ROOT = Path(__file__).resolve().parents[2]


def load_vocab(path):
    """Minimal id -> {name, definition, ladder_target} map. No pyyaml dependency."""
    vocab = {}
    cur = None
    if not path:
        return vocab
    try:
        lines = open(path).read().splitlines()
    except OSError:
        return vocab
    for ln in lines:
        m = re.match(r"\s*-\s*id:\s*(\S+)", ln)
        if m:
            cur = {"id": m.group(1)}
            vocab[cur["id"]] = cur
            continue
        if cur is None:
            continue
        m = re.match(r"\s*name:\s*(.+)", ln)
        if m:
            cur["name"] = m.group(1).strip()
        m = re.match(r"\s*definition:\s*(.+)", ln)
        if m:
            cur["definition"] = m.group(1).strip()
        m = re.match(r"\s*default_ladder_target:\s*(L[0-4])", ln)
        if m:
            cur["ladder_target"] = m.group(1)
    return vocab


def verdict(p, total_contributors, promote_min):
    contributors = p.get("contributors", p.get("sessions_seen", 1))
    corroborated = contributors >= promote_min
    gates = len(p.get("decision_points", []))
    crisp = gates <= CRISP_GATES
    mech = p.get("mechanical_fraction", 0)
    pain = p.get("pain", "low")
    solved = mech >= SOLVED_MECH and pain == "low"
    prio = p.get("determinism_priority", 0)
    if solved:
        return "ALREADY MOSTLY SOLVED", "🔵"
    if prio >= PRIORITY_CUT and corroborated and crisp:
        return "BUILD NOW", "🟢"
    if prio >= PRIORITY_CUT and corroborated:
        return "BUILD (judgment-heavy)", "🟡"
    if prio >= PRIORITY_CUT:
        return "PROMISING (needs corroboration)", "🟣"
    return "LATER", "⚪"


def brief_block(p, vocab, total, promote_min):
    v = vocab.get(p["id"], {})
    name = v.get("name", p["id"])
    label, icon = verdict(p, total, promote_min)
    target = v.get("ladder_target", "L2")
    gates = p.get("decision_points", [])
    sigs = p.get("example_signatures", [])
    out = []
    out.append(f"### {icon} {name} — {label}")
    if v.get("definition"):
        out.append(f"_{v['definition']}_")
    out.append("")
    out.append(
        f"**Why:** priority **{p['determinism_priority']:.2f}** · "
        f"seen by **{p.get('contributors', p.get('sessions_seen', 1))}/{total}** contributors · "
        f"**{p.get('occurrences', 0)}** occurrences · "
        f"pain {PAIN_MARK.get(p.get('pain','low'),'')} {p.get('pain','?')} · "
        f"{int(round(p.get('mechanical_fraction',0)*100))}% mechanical"
    )
    out.append(f"**The move:** L1 (recurring manual work today) → **{target}** target")
    out.append("")
    if gates:
        out.append(f"**Gates to install ({len(gates)} decision point"
                   f"{'s' if len(gates)!=1 else ''} — the judgment to keep):**")
        for g in gates[:5]:
            out.append(f"- {g}")
        if len(gates) > 5:
            out.append(f"- …and {len(gates)-5} more (consolidate into ≤3 real gates before building)")
        out.append("")
    if sigs:
        out.append("**Skeleton to script (observed tool-call shape):**")
        for s in sigs[:4]:
            out.append(f"- `{s}`")
        if len(sigs) > 4:
            out.append(f"- …and {len(sigs)-4} more variant(s)")
        out.append("")
    out.append("**First step:** script the skeleton above; wrap each gate as a named "
               "decision point (a default rule where one is obvious, else prompt a "
               f"model/human); record every decision so the gate can climb toward {target}.")
    out.append("")
    return "\n".join(out)


def enriched_patterns(d, vocab):
    total = d.get("contributors", 1)
    promote_min = d.get("promote_min_contributors", 2)
    rows = []
    for p in d.get("patterns", []):
        row = dict(p)
        label, icon = verdict(p, total, promote_min)
        row["verdict"] = label
        row["verdict_icon"] = icon
        row["contributors"] = row.get("contributors", row.get("sessions_seen", 1))
        row["occurrences"] = row.get("occurrences", 0)
        row["name"] = vocab.get(p["id"], {}).get("name", p["id"])
        row["definition"] = vocab.get(p["id"], {}).get("definition", "")
        row["ladder_target"] = vocab.get(p["id"], {}).get("ladder_target", p.get("ladder_target", "L2"))
        rows.append(row)
    return rows


def summarize(d, vocab, top=0, source="", markdown_path="", summary_path=""):
    inferred_total = max([p.get("contributors", p.get("sessions_seen", 1)) for p in d.get("patterns", [])] or [1])
    total = d.get("contributors") or inferred_total
    promote_min = d.get("promote_min_contributors", 2)
    patterns = enriched_patterns(d, vocab)
    candidates = [p for p in patterns if p["verdict"].startswith(("BUILD", "PROMISING"))]
    if top:
        candidates = candidates[:top]
    out = dict(d)
    out["_source"] = source
    out["markdown_path"] = markdown_path
    out["summary_path"] = summary_path
    out["contributors"] = total
    out["promote_min_contributors"] = promote_min
    out["patterns"] = patterns
    out["candidates"] = candidates
    return out


def render_markdown(summary, vocab):
    lines = []
    lines.append("# Session-mining action brief")
    lines.append("")
    vv = summary.get("vocab_version", "?")
    lines.append(f"_{summary.get('reports_merged','?')} report(s) · {summary.get('contributors', 1)} contributor(s) · "
                 f"vocab {vv} · promotion threshold {summary.get('promote_min_contributors', 2)} contributors._")
    lines.append("")

    lines.append("## Build these (ranked)")
    lines.append("")
    if summary["candidates"]:
        for p in summary["candidates"]:
            lines.append(brief_block(p, vocab, summary.get("contributors", 1), summary.get("promote_min_contributors", 2)))
    else:
        lines.append("_No pattern cleared the priority cut. Mine more sessions or contributors._")
        lines.append("")

    lines.append("")
    lines.append("## Full ranking")
    lines.append("")
    lines.append("| Pattern | Verdict | Prio | Contrib | Occ | Pain | Gates |")
    lines.append("|---|---|--:|--:|--:|:--:|--:|")
    for p in summary["patterns"]:
        lines.append(f"| {p['id']} | {p['verdict_icon']} {p['verdict']} | {p['determinism_priority']:.2f} "
                     f"| {p.get('contributors', p.get('sessions_seen', 1))}/{summary.get('contributors', 1)} | {p.get('occurrences', 0)} | {p.get('pain','?')} "
                     f"| {len(p.get('decision_points',[]))} |")
    lines.append("")

    quar = summary.get("novel_quarantine", [])
    cand = summary.get("novel_promotion_candidates", [])
    if cand:
        lines.append("## Newly corroborated patterns (promote into the vocabulary)")
        lines.append("")
        for p in cand:
            lines.append(f"- **{p['id']}** — {p.get('contributors', p.get('sessions_seen', 1))} contributors, "
                         f"{p.get('occurrences', 0)} occ. Add to `vocab/core.yaml` (bump `vocab_version`).")
        lines.append("")
    if quar:
        lines.append("## Watch list (novel, not yet corroborated)")
        lines.append("")
        lines.append("Each needs more independent contributors before it counts. Not actionable yet.")
        lines.append("")
        for p in sorted(quar, key=lambda x: x.get("contributors", x.get("sessions_seen", 1)), reverse=True):
            contributors = p.get("contributors", p.get("sessions_seen", 1))
            need = max(0, summary.get("promote_min_contributors", 2) - contributors)
            lines.append(f"- `{p['id']}` — {contributors} contributor(s), "
                         f"needs {need} more to promote")
        lines.append("")
    return "\n".join(lines) + "\n"


def write(path, content):
    directory = os.path.dirname(os.path.abspath(path))
    if directory:
        os.makedirs(directory, exist_ok=True)
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(content)


def write_json(path, value):
    write(path, json.dumps(value, indent=2, sort_keys=True) + "\n")


def write_slidey_spec(path, summary):
    builder = ROOT / "tools" / "report-deck" / "deterministic_deck.py"
    subprocess.run(
        [
            sys.executable,
            str(builder),
            "--kind",
            "session-mining-action",
            "--input-json",
            json.dumps(summary, sort_keys=True),
            "--out",
            path,
        ],
        cwd=ROOT,
        check=True,
        stdout=subprocess.DEVNULL,
    )


def main():
    ap = argparse.ArgumentParser(description="Render an aggregated report into an actionable brief.")
    ap.add_argument("report", help="aggregate.py output JSON")
    ap.add_argument("--vocab", default="vocab/core.yaml", help="controlled vocabulary (for names + ladder targets)")
    ap.add_argument("--top", type=int, default=0, help="limit the action shortlist to N (0 = all build candidates)")
    ap.add_argument("--markdown", help="write Markdown brief here instead of only stdout")
    ap.add_argument("--summary", help="write machine-readable summary with computed verdicts")
    ap.add_argument("--slidey-spec", help="write deterministic Slidey JSON deck spec")
    args = ap.parse_args()

    d = json.load(open(args.report))
    vocab = load_vocab(args.vocab)
    summary = summarize(
        d,
        vocab,
        top=args.top,
        source=args.report,
        markdown_path=args.markdown or "",
        summary_path=args.summary or "",
    )
    md = render_markdown(summary, vocab)
    if args.markdown:
        write(args.markdown, md)
    else:
        sys.stdout.write(md)
    if args.summary:
        write_json(args.summary, summary)
    if args.slidey_spec:
        write_slidey_spec(args.slidey_spec, summary)


if __name__ == "__main__":
    main()
