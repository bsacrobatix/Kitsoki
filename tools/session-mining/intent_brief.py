#!/usr/bin/env python3
"""intent_brief.py — render the two intent-mining reports into a readable brief.

    python3 intent_brief.py <job-dir> > BRIEF.md
    python3 intent_brief.py --intents intents.json --analysis analysis.json > BRIEF.md

The two JSON reports (intents.json + analysis.json) are the machine deliverable;
this is the human view. It summarizes the corpus, the determinism split, the
grounding rate, the tag distribution, and the recurring clusters, then lists every
intent with its verbatim ask, determinism verdict, recipe, and agent gates.

Deterministic, stdlib only. Reads only the emitted reports (not the traces).
"""
import argparse
import subprocess
import json
import os
import sys
from collections import Counter
from pathlib import Path

DET_ICON = {"deterministic": "🟢", "agent-gated": "🟡", "irreducible-llm": "🔴"}
ROOT = Path(__file__).resolve().parents[2]


def _load(p):
    with open(p) as fh:
        return json.load(fh)


def _oneline(s, n=160):
    s = " ".join((s or "").split())
    return s[:n] + ("…" if len(s) > n else "")


def summarize(intents, analysis, intents_path="", analysis_path="", markdown_path="", summary_path=""):
    inst = {i["instance_id"]: i for i in analysis.get("instances", [])}
    rows = intents.get("intents", [])
    det = Counter(i["determinism"] for i in analysis.get("instances", []))
    cited = sum(i.get("grounding", {}).get("actions_cited", 0) for i in inst.values())
    valid = sum(i.get("grounding", {}).get("actions_validated", 0) for i in inst.values())
    intent_rows = []
    order = {"agent-gated": 0, "irreducible-llm": 1, "deterministic": 2}
    for r in sorted(rows, key=lambda x: (order.get(inst.get(x["instance_id"], {}).get("determinism"), 9), x["instance_id"])):
        an = inst.get(r["instance_id"], {})
        acts = an.get("actions", [])
        gates = [
            "%s — %s" % (g.get("decision"), g.get("validator"))
            for g in an.get("agent_gates", []) or []
        ]
        sig = "  ->  ".join(a.get("signature", a.get("tool", "?")) for a in acts[:8])
        if len(acts) > 8:
            sig += "  ->  ...(%d more)" % (len(acts) - 8)
        intent_rows.append({
            "instance_id": r["instance_id"],
            "user_text": r.get("user_text", ""),
            "tags": r.get("tags", {}),
            "determinism": an.get("determinism", "?"),
            "measured": an.get("measured", {}),
            "recipe": sig,
            "agent_gates": gates,
        })
    return {
        "job": intents.get("job", "?"),
        "total_intents": intents.get("total_intents", len(rows)),
        "clusters": analysis.get("clusters", []),
        "tags": intents.get("tags", {}),
        "determinism_counts": dict(det),
        "grounding": {"valid": valid, "cited": cited, "percent": round(100 * valid / cited) if cited else 100},
        "intents": intent_rows,
        "intents_path": intents_path,
        "analysis_path": analysis_path,
        "markdown_path": markdown_path,
        "summary_path": summary_path,
    }


def render_summary(summary):
    out = []
    w = out.append
    det = Counter(summary.get("determinism_counts", {}))
    cited = summary.get("grounding", {}).get("cited", 0)
    valid = summary.get("grounding", {}).get("valid", 0)

    w("# Session-mining intent brief\n")
    w("_job `%s` · %d intents · %d clusters · grounding %d/%d actions validated (%d%%)_\n"
      % (summary.get("job", "?"), summary.get("total_intents", len(summary.get("intents", []))),
         len(summary.get("clusters", [])), valid, cited,
         summary.get("grounding", {}).get("percent", 100)))

    w("\n## Reproducibility split\n")
    w("| verdict | count | meaning |")
    w("|---|--:|---|")
    w("| 🟢 deterministic | %d | pure tool sequence, all params grounded, no judgment fork |"
      % det.get("deterministic", 0))
    w("| 🟡 agent-gated | %d | reproducible except at named gates, each with a strict validator |"
      % det.get("agent-gated", 0))
    w("| 🔴 irreducible-llm | %d | output genuinely needs open-ended generation |"
      % det.get("irreducible-llm", 0))

    w("\n## Tag distribution\n")
    for dim in ("action", "surface", "scope"):
        tg = summary.get("tags", {}).get(dim, {})
        if not tg:
            continue
        top = sorted(tg.items(), key=lambda x: -x[1])
        w("- **%s** — %s" % (dim, ", ".join("`%s` %d" % (k, v) for k, v in top[:12])))

    clusters = sorted(summary.get("clusters", []), key=lambda c: -c["count"])
    recurring = [c for c in clusters if c["count"] > 1]
    if recurring:
        w("\n## Recurring intent shapes (clusters seen >1×)\n")
        for c in recurring:
            w("- **%d×** `%s`" % (c["count"], _oneline(c["key"], 110)))

    w("\n## Intents\n")
    # group by determinism for readability: gated/irreducible first (the actionable ones)
    for r in summary.get("intents", []):
        d = r.get("determinism", "?")
        w("\n### %s %s" % (DET_ICON.get(d, ""), r["instance_id"]))
        w("> %s" % _oneline(r.get("user_text", ""), 220))
        tags = r.get("tags", {})
        w("- **tags:** action=%s%s" % (
            tags.get("action", []),
            (" · surface=%s" % tags.get("surface")) if tags.get("surface") else ""))
        m = r.get("measured", {})
        w("- **measured:** %d tool calls · %d edit→rerun · %d retries"
          % (m.get("tool_calls", 0), m.get("edit_rerun_cycles", 0), m.get("retries", 0)))
        if r.get("recipe"):
            w("- **recipe:** %s" % r["recipe"])
        for g in r.get("agent_gates", []) or []:
            w("- **gate:** %s" % g)
    return "\n".join(out) + "\n"


def render(intents, analysis):
    return render_summary(summarize(intents, analysis))


def write(path, content):
    directory = os.path.dirname(os.path.abspath(path))
    if directory:
        os.makedirs(directory, exist_ok=True)
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(content)


def write_slidey_spec(path, summary):
    builder = ROOT / "tools" / "report-deck" / "deterministic_deck.py"
    subprocess.run(
        [
            sys.executable,
            str(builder),
            "--kind",
            "session-mining-intent",
            "--input-json",
            json.dumps(summary, sort_keys=True),
            "--out",
            path,
        ],
        cwd=ROOT,
        check=True,
        stdout=subprocess.DEVNULL,
    )


def main(argv=None):
    ap = argparse.ArgumentParser(description="Render intent-mining reports into a readable brief.")
    ap.add_argument("job_dir", nargs="?", help="job dir holding intents.json + analysis.json")
    ap.add_argument("--intents", help="intents.json (overrides job_dir)")
    ap.add_argument("--analysis", help="analysis.json (overrides job_dir)")
    ap.add_argument("--markdown", help="write Markdown brief here instead of stdout")
    ap.add_argument("--summary", help="write machine-readable summary JSON")
    ap.add_argument("--slidey-spec", help="write deterministic Slidey JSON deck spec")
    args = ap.parse_args(argv)

    if args.intents and args.analysis:
        ip, ap_ = args.intents, args.analysis
    elif args.job_dir:
        ip = os.path.join(args.job_dir, "intents.json")
        ap_ = os.path.join(args.job_dir, "analysis.json")
    else:
        ap.error("pass a job dir, or both --intents and --analysis")
    summary = summarize(
        _load(ip),
        _load(ap_),
        intents_path=ip,
        analysis_path=ap_,
        markdown_path=args.markdown or "",
        summary_path=args.summary or "",
    )
    md = render_summary(summary)
    if args.markdown:
        write(args.markdown, md)
    else:
        sys.stdout.write(md)
    if args.summary:
        write(args.summary, json.dumps(summary, indent=2, sort_keys=True) + "\n")
    if args.slidey_spec:
        write_slidey_spec(args.slidey_spec, summary)
    return 0


if __name__ == "__main__":
    sys.exit(main())
