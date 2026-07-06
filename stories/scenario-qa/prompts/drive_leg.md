You are driving ONE product-journey-qa scenario leg, pinned to a SINGLE
transport. This pin OVERRIDES your usual cheapest-surface heuristic — the
evidence for this leg must come from `{{ args.transport }}`, not
whichever surface looks cheapest.

Leg:

```
{{ args.leg_json }}
```

- Run: `{{ args.run_id }}`
- Run dir: `{{ args.run_dir }}`
- Transport (pinned): `{{ args.transport }}`

Follow `.agents/agents/product-journey-qa-driver.md`'s transport discipline
and this transport's evidence contract (`transport_evidence_contract` on the
leg above). Before capturing, PREFLIGHT this transport's visual tools (e.g.
`visual.open`/`visual.observe` for web/vscode, `render.tui`/`render.tui_png`
for tui). If a visual tool comes back JSON-degraded rather than a genuine
frame, STOP and report `status: "degraded-evidence"` with the exact blocker
— do not fabricate a screenshot or pass a stub frame off as real evidence.

**Live drive authorization.** This is a live scenario check: cost-bearing
live/model work IS authorized for this leg, within the leg's
`live_budget.max_live_minutes` ceiling. When the scenario's flow needs
interpretive behavior a cassette can't replay (free-text routing,
host.agent.converse/decide steps), open the nested story session LIVE:
`session.new` with `harness: "live"` and `profile: "{{ args.live_profile }}"`.
If that profile value is empty, no live profile was supplied — fall back to
replay and report the missing-cassette blocker honestly, as usual. Never
burn live budget on steps a cassette or menu-driven submit can cover; go
live only for the steps that need it.

**Persist every frame you render.** A frame that only existed in your tool
output is not evidence: write each transport frame you capture (preflight
AND key states — `render.tui_png` output files, `visual.snapshot` images)
into the leg's `evidence_dir` and attach it with the leg's attach command
under the matching evidence kind. The judge can only cite files that exist
in the run dir.

**vscode legs: capture the bridge TWICE, not once.** The preflight
`visual.open kind=vscode` proves the bridge is *reachable* before you drive
anything — it does not prove the scenario's outcome, because the rest of the
leg is usually driven through a different surface (`session.submit`, a live
text turn, etc.) that advances the SAME session to a new state (e.g.
`landing` → `s2`). A preflight-only vscode leg is degraded evidence even if
everything else about the scenario worked. So, for `vscode` legs:

1. Preflight (`visual.open kind=vscode` + `visual.observe`) as usual, and
   note which session handle it opened against (e.g. `s1`).
2. Drive the scenario to its target state exactly as you would for any other
   transport — this is normally NOT through vscode tools (e.g. it is driven
   through `session.submit`/`session.drive` on the underlying story session).
3. Once the live session has reached its target state, call `visual.open
   kind=vscode` / `visual.observe` **again**, against the SAME session
   handle you just drove forward, and persist that frame into `evidence_dir`
   tagged as a post-drive capture (e.g. `NN-postdrive-vscode-observe.json`,
   never reusing the preflight's filename). This is the only evidence that
   proves the bridge reflects the driven-forward state, not just the
   starting one.
4. Report the post-drive capture's path/retained id as
   `post_drive_evidence_ref`, and the session handle it was taken against as
   `post_drive_session_handle`. If the post-drive capture could not be taken
   (e.g. the bridge dropped, or you ran out of live budget before returning
   to it), leave `post_drive_evidence_ref` empty and report the honest
   blocker — do not report `"captured"`/`"pass"`-shaped status while
   substituting the preflight frame for it. The record-keeping gate scores
   any vscode leg missing `post_drive_evidence_ref` as `degraded-evidence`
   regardless of what else was captured.

When you finish, **report — do not grade**. Submit:

```json
{
  "status": "attempted | captured | blocked | degraded-evidence",
  "evidence_refs": ["<path-or-retained-id>", "..."],
  "frames_dir": "<directory of captured frames, if any>",
  "post_drive_evidence_ref": "<path-or-retained-id of the POST-drive vscode capture; vscode legs only, empty if not captured>",
  "post_drive_session_handle": "<live session_handle the post-drive capture was taken against; vscode legs only>",
  "blockers": ["<honest blocker, if any>"],
  "summary": "<what you actually attempted for this leg>"
}
```

Do not claim evidence that was not actually captured. `status:
"degraded-evidence"` with an honest blocker is correct when the transport's
visual surface could not produce real evidence — the judge relies on you
reporting the real state, not a hopeful one.
