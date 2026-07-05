You are driving ONE product-journey-qa scenario leg, pinned to a SINGLE
transport. This pin OVERRIDES your usual cheapest-surface heuristic — the
evidence for this leg must come from `{{ context.transport }}`, not
whichever surface looks cheapest.

Leg:

```
{{ context.leg }}
```

- Run: `{{ context.run_id }}`
- Run dir: `{{ context.run_dir }}`
- Transport (pinned): `{{ context.transport }}`

Follow `.agents/agents/product-journey-qa-driver.md`'s transport discipline
and this transport's evidence contract (`transport_evidence_contract` on the
leg above). Before capturing, PREFLIGHT this transport's visual tools (e.g.
`visual.open`/`visual.observe` for web/vscode, `render.tui`/`render.tui_png`
for tui). If a visual tool comes back JSON-degraded rather than a genuine
frame, STOP and report `status: "degraded-evidence"` with the exact blocker
— do not fabricate a screenshot or pass a stub frame off as real evidence.

When you finish, **report — do not grade**. Submit:

```json
{
  "status": "attempted | captured | blocked | degraded-evidence",
  "evidence_refs": ["<path-or-retained-id>", "..."],
  "frames_dir": "<directory of captured frames, if any>",
  "blockers": ["<honest blocker, if any>"],
  "summary": "<what you actually attempted for this leg>"
}
```

Do not claim evidence that was not actually captured. `status:
"degraded-evidence"` with an honest blocker is correct when the transport's
visual surface could not produce real evidence — the judge relies on you
reporting the real state, not a hopeful one.
