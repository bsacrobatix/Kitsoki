#!/usr/bin/env python3
"""Build a static, tour-recordable demo page from an arena run directory.

The page is a pure rendering of run artifacts (`summary.json`, `rollup.json`,
`cells/*.json`). It does not invent outcomes, does not call Docker, and does not
call an LLM. Playwright records this page as the arena showdown tour.
"""

from __future__ import annotations

import argparse
import html
import json
from pathlib import Path
from typing import Any


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--run-dir", required=True, help="arena output directory containing summary.json")
    parser.add_argument("--out-dir", default=".artifacts/arena-showdown-demo")
    parser.add_argument("--title", default="CodeAct vs Codex Arena Showdown")
    args = parser.parse_args()

    run_dir = Path(args.run_dir).resolve()
    out_dir = Path(args.out_dir).resolve()
    out_dir.mkdir(parents=True, exist_ok=True)
    summary = load_summary(run_dir)
    cells = list(summary.get("cells") or load_cells(run_dir))
    payload = build_payload(args.title, run_dir, summary, cells)

    (out_dir / "arena-showdown.html").write_text(render_html(payload), encoding="utf-8")
    (out_dir / "arena-showdown-data.json").write_text(
        json.dumps(payload, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    (out_dir / "qa-feature.md").write_text(render_feature(payload), encoding="utf-8")
    (out_dir / "qa-scenarios.yaml").write_text(render_scenarios(payload), encoding="utf-8")
    print(out_dir / "arena-showdown.html")
    return 0


def load_summary(run_dir: Path) -> dict[str, Any]:
    path = run_dir / "summary.json"
    if path.exists():
        return json.loads(path.read_text(encoding="utf-8"))
    rollup = run_dir / "rollup.json"
    if not rollup.exists():
        raise SystemExit(f"run directory needs summary.json or rollup.json: {run_dir}")
    data = json.loads(rollup.read_text(encoding="utf-8"))
    return {
        "kind": "arena_run_summary",
        "run_id": run_dir.name,
        "summary": data.get("summary", {}),
        "by_treatment": data.get("by_variant", {}),
        "by_target": data.get("by_target", {}),
        "by_job_type": data.get("by_job_type", {}),
        "permission_compliance": {"codeact_cells": 0, "compliant_cells": 0, "rate": None},
        "cost_rollups": {},
        "artifact_refs": {
            "rollup_json": "rollup.json",
            "rollup_md": "rollup.md",
            "cells_dir": "cells/",
        },
        "cells": data.get("cells", []),
    }


def load_cells(run_dir: Path) -> list[dict[str, Any]]:
    cells_dir = run_dir / "cells"
    if not cells_dir.exists():
        return []
    return [
        json.loads(path.read_text(encoding="utf-8"))
        for path in sorted(cells_dir.glob("*.json"))
    ]


def build_payload(title: str, run_dir: Path, summary: dict[str, Any], cells: list[dict[str, Any]]) -> dict[str, Any]:
    stats = summary.get("summary") or {}
    infra_cells = [cell for cell in cells if str(cell.get("health", "")).startswith("infra:")]
    codeact_cells = [
        cell for cell in cells
        if "codeact" in str(cell.get("variant_id", "")).lower()
        or "codeact" in str((cell.get("metrics") or {}).get("action_surface", "")).lower()
    ]
    run_yaml = (run_dir / "run.yaml").read_text(encoding="utf-8") if (run_dir / "run.yaml").exists() else ""
    live = "live: true" in run_yaml
    return {
        "title": title,
        "run_id": summary.get("run_id") or run_dir.name,
        "run_dir": str(run_dir),
        "mode": "live" if live else "no-LLM replay/arming",
        "cells_total": stats.get("n", len(cells)),
        "win_rate": stats.get("win_rate"),
        "infra_failures": stats.get("infra_failures", len(infra_cells)),
        "permission": summary.get("permission_compliance") or {},
        "by_treatment": summary.get("by_treatment") or {},
        "cost_rollups": summary.get("cost_rollups") or {},
        "artifact_refs": summary.get("artifact_refs") or {},
        "cells": cells,
        "infra_notes": [str(cell.get("notes") or "") for cell in infra_cells if cell.get("notes")][:3],
        "codeact_cell_count": len(codeact_cells),
    }


def render_html(p: dict[str, Any]) -> str:
    rows = []
    for name, bucket in (p.get("by_treatment") or {}).items():
        verdicts = ", ".join(f"{k}:{v}" for k, v in sorted((bucket.get("verdicts") or {}).items()))
        rows.append(
            f"<tr data-treatment='{e(name)}'>"
            f"<td><strong>{e(name)}</strong></td>"
            f"<td>{bucket.get('n', '')}</td>"
            f"<td>{fmt_rate(bucket.get('win_rate'))}</td>"
            f"<td>{fmt_money(bucket.get('avg_cost_usd'))}</td>"
            f"<td>{e(verdicts)}</td>"
            "</tr>"
        )
    cell_rows = []
    for cell in p.get("cells") or []:
        metrics = cell.get("metrics") or {}
        note = str(cell.get("notes") or "")
        cell_rows.append(
            f"<tr class='verdict-{e(str(cell.get('verdict', '')))}'>"
            f"<td>{e(str(cell.get('variant_id', '')))}</td>"
            f"<td>{e(str((cell.get('axis') or {}).get('task', '')))}</td>"
            f"<td>{e(str(cell.get('verdict', '')))}</td>"
            f"<td>{e(str(cell.get('health', '')))}</td>"
            f"<td>{e(str(metrics.get('action_surface', '')))}</td>"
            f"<td>{e(short(note, 130))}</td>"
            "</tr>"
        )
    infra_note = p.get("infra_notes", [""])[0] if p.get("infra_notes") else ""
    permission = p.get("permission") or {}
    artifacts = "".join(
        f"<li><span>{e(key)}</span><code>{e(str(value))}</code></li>"
        for key, value in sorted((p.get("artifact_refs") or {}).items())
    )
    return f"""<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>{e(p['title'])}</title>
  <style>
    :root {{
      --bg:#0b1020; --panel:#151b2c; --panel2:#111827; --text:#eef2ff;
      --muted:#aab4c8; --line:#2c3448; --teal:#35d0ba; --amber:#f4bd50;
      --rose:#ff6b88; --green:#7bd88f; --violet:#b8a1ff;
    }}
    * {{ box-sizing:border-box; }}
    body {{
      margin:0; min-height:100vh; background:var(--bg); color:var(--text);
      font:18px/1.45 Inter, ui-sans-serif, system-ui, -apple-system, Segoe UI, sans-serif;
      letter-spacing:0;
    }}
    body::before {{ content:""; position:fixed; left:0; right:0; top:0; height:12px; background:var(--bg); z-index:9990; pointer-events:none; }}
    main {{ width:min(1480px, calc(100vw - 80px)); margin:0 auto; padding:46px 0 72px; }}
    section {{ margin:0 0 34px; }}
    .hero {{
      min-height:320px; display:grid; grid-template-columns:1.2fr .8fr; gap:32px; align-items:center;
      border-bottom:1px solid var(--line); padding-bottom:30px;
    }}
    .kicker {{ color:var(--teal); text-transform:uppercase; font-weight:800; font-size:15px; }}
    h1 {{ font-size:58px; line-height:1.02; margin:10px 0 18px; letter-spacing:0; }}
    h2 {{ font-size:30px; margin:0 0 16px; letter-spacing:0; }}
    p {{ color:var(--muted); margin:0; max-width:840px; }}
    .run-card, .panel {{ background:var(--panel); border:1px solid var(--line); border-radius:8px; padding:22px; }}
    .run-card dl {{ display:grid; grid-template-columns:auto 1fr; gap:12px 18px; margin:0; }}
    dt {{ color:var(--muted); }} dd {{ margin:0; font-weight:800; }}
    .stats {{ display:grid; grid-template-columns:repeat(4, 1fr); gap:16px; }}
    .stat {{ background:var(--panel2); border:1px solid var(--line); border-radius:8px; padding:18px; min-height:116px; }}
    .stat b {{ display:block; font-size:36px; color:var(--text); }}
    .stat span {{ color:var(--muted); font-size:15px; }}
    .stat.ok b {{ color:var(--green); }} .stat.warn b {{ color:var(--amber); }} .stat.bad b {{ color:var(--rose); }}
    table {{ width:100%; border-collapse:collapse; overflow:hidden; border-radius:8px; }}
    th, td {{ text-align:left; padding:13px 14px; border-bottom:1px solid var(--line); vertical-align:top; }}
    th {{ color:#d8def0; background:#20283a; font-size:15px; text-transform:uppercase; }}
    td {{ color:#e9edf7; overflow-wrap:anywhere; }}
    tr:nth-child(odd) td {{ background:#12192a; }}
    tr:nth-child(even) td {{ background:#0f1626; }}
    code {{ font-family:ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; color:#d9e4ff; overflow-wrap:anywhere; }}
    .split {{ display:grid; grid-template-columns:1fr 1fr; gap:20px; }}
    .note {{ border-left:4px solid var(--amber); background:#2a2114; color:#ffe2a8; padding:16px 18px; border-radius:8px; overflow-wrap:anywhere; }}
    .artifacts ul {{ display:grid; grid-template-columns:1fr 1fr; gap:12px; list-style:none; padding:0; margin:0; }}
    .artifacts li {{ display:flex; justify-content:space-between; gap:16px; background:#101727; border:1px solid var(--line); border-radius:8px; padding:14px; }}
    .artifacts span {{ color:var(--muted); }}
    .verdict-blocked td, .verdict-failed td {{ color:#ffd3dc; }}
    @media (max-width: 900px) {{ main {{ width:calc(100vw - 28px); }} .hero,.split,.stats,.artifacts ul {{ grid-template-columns:1fr; }} h1 {{ font-size:40px; }} }}
  </style>
</head>
<body>
  <main>
    <section class="hero" data-testid="arena-demo-hero">
      <div>
        <div class="kicker">Arena treatment demo</div>
        <h1>{e(p['title'])}</h1>
        <p>This page is generated from a real arena run bundle. It preserves the actual verdicts, health classes, costs, permission evidence, and artifact paths from <code>{e(p['run_dir'])}</code>.</p>
      </div>
      <aside class="run-card" data-testid="arena-run-card">
        <dl>
          <dt>Run</dt><dd>{e(str(p['run_id']))}</dd>
          <dt>Mode</dt><dd>{e(str(p['mode']))}</dd>
          <dt>Cells</dt><dd>{p.get('cells_total')}</dd>
          <dt>CodeAct cells</dt><dd>{p.get('codeact_cell_count')}</dd>
        </dl>
      </aside>
    </section>

    <section class="stats" data-testid="arena-status-cards">
      <div class="stat"><b>{p.get('cells_total')}</b><span>planned and recorded cells</span></div>
      <div class="stat ok"><b>{fmt_rate(p.get('win_rate'))}</b><span>arena win rate</span></div>
      <div class="stat {'bad' if p.get('infra_failures') else 'ok'}"><b>{p.get('infra_failures')}</b><span>infra failures</span></div>
      <div class="stat warn"><b>{fmt_rate(permission.get('rate'))}</b><span>CodeAct permission compliance</span></div>
    </section>

    <section class="panel" data-testid="arena-treatment-table">
      <h2>Treatment Leaderboard</h2>
      <table>
        <thead><tr><th>Treatment</th><th>Cells</th><th>Win rate</th><th>Avg cost</th><th>Verdicts</th></tr></thead>
        <tbody>{''.join(rows)}</tbody>
      </table>
    </section>

    <section class="split">
      <div class="panel" data-testid="arena-permission-evidence">
        <h2>CodeAct Surface Proof</h2>
        <p>The run tracks CodeAct-specific cells separately and reports whether their launch plans proved the expected restricted tool surface.</p>
        <div class="stats" style="grid-template-columns:repeat(3,1fr); margin-top:18px">
          <div class="stat"><b>{permission.get('codeact_cells', 0)}</b><span>CodeAct cells</span></div>
          <div class="stat"><b>{permission.get('compliant_cells', 0)}</b><span>compliant cells</span></div>
          <div class="stat"><b>{fmt_rate(permission.get('rate'))}</b><span>compliance rate</span></div>
        </div>
      </div>
      <div class="panel" data-testid="arena-infra-note">
        <h2>Honest Health</h2>
        <p class="note">{e(infra_note or 'No infrastructure blocker was recorded in this run bundle.')}</p>
      </div>
    </section>

    <section class="panel" data-testid="arena-cell-table">
      <h2>Cell Evidence</h2>
      <table>
        <thead><tr><th>Variant</th><th>Task</th><th>Verdict</th><th>Health</th><th>Surface</th><th>Notes</th></tr></thead>
        <tbody>{''.join(cell_rows)}</tbody>
      </table>
    </section>

    <section class="panel artifacts" data-testid="arena-artifacts">
      <h2>Reusable Output Bundle</h2>
      <ul>{artifacts}</ul>
    </section>
  </main>
</body>
</html>
"""


def render_feature(p: dict[str, Any]) -> str:
    return f"""# Arena CodeAct Showdown Demo

This visual QA checks the generated arena showdown video. The video must show a
real arena run bundle, not a hand-authored mock: run id `{p.get('run_id')}`,
mode `{p.get('mode')}`, and source directory `{p.get('run_dir')}`.

The required proof is the productized operator experience:

- the demo identifies the run bundle and explains the CodeAct-vs-Codex treatment matrix;
- the treatment leaderboard and cell table are visible and readable;
- CodeAct permission-compliance evidence is surfaced explicitly;
- infrastructure health is honest, including blockers such as Docker context or daemon failures;
- output artifacts are shown as reusable files (`summary.json`, `report.md`, `deck.slidey.json`, `cells/`).
"""


def render_scenarios(_p: dict[str, Any]) -> str:
    return """feature: Arena CodeAct showdown demo

scenarios:
  - id: run-identity
    title: The video identifies the real arena run bundle
    required: true
    steps:
      - The title says CodeAct vs Codex Arena Showdown or equivalent
      - A run id or run directory is visible
      - The mode and number of cells are visible

  - id: treatment-matrix
    title: The treatment matrix is readable
    required: true
    steps:
      - A treatment leaderboard table is visible
      - Raw Codex and at least one CodeAct treatment are visible in the table
      - Verdicts or win-rate values are visible for the treatments

  - id: permission-evidence
    title: CodeAct permission evidence is surfaced
    required: true
    steps:
      - A CodeAct surface or permission-compliance section is visible
      - CodeAct cell counts or compliance rate are visible
      - The section distinguishes CodeAct from the raw Codex surface

  - id: health-honesty
    title: Run health is honest and not disguised as a fake win
    required: true
    steps:
      - The video shows infra failures or a no-infra status explicitly
      - If there is an infra blocker, the blocker text is visible
      - The cell table shows health values for individual cells

  - id: reusable-artifacts
    title: The output bundle is shown as reusable artifacts
    required: true
    steps:
      - A reusable output bundle or artifacts section is visible
      - At least two artifact names such as summary.json, report.md, deck.slidey.json, rollup.json, or cells/ are visible
"""


def fmt_rate(value: Any) -> str:
    if value is None:
        return "n/a"
    if isinstance(value, (int, float)):
        return f"{float(value) * 100:.0f}%"
    return str(value)


def fmt_money(value: Any) -> str:
    if value is None:
        return "n/a"
    if isinstance(value, (int, float)):
        return f"${float(value):.4f}"
    return str(value)


def short(value: str, n: int) -> str:
    return value if len(value) <= n else value[: n - 1] + "…"


def e(value: object) -> str:
    return html.escape(str(value), quote=True)


if __name__ == "__main__":
    raise SystemExit(main())
